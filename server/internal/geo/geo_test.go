package geo

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsPublicIP(t *testing.T) {
	// 公网：应当放行并送去解析
	public := []string{
		"43.250.175.188",
		"8.8.8.8",
		"1.1.1.1",
		"2001:4860:4860::8888",
	}
	for _, ip := range public {
		if !isPublicIP(ip) {
			t.Errorf("公网地址 %s 应被放行", ip)
		}
	}

	// 内网与保留段：必须拒绝
	// 内网 IP 发给第三方等于泄露网络拓扑，且解析无地理意义。
	private := []struct {
		ip  string
		why string
	}{
		{"127.0.0.1", "环回"},
		{"10.0.0.5", "10/8 私有"},
		{"172.16.0.1", "172.16/12 私有"},
		{"172.20.10.1", "172.16/12 私有（20 落在范围内）"},
		{"192.168.1.1", "192.168/16 私有"},
		{"169.254.1.1", "链路本地"},
		{"100.64.0.1", "CGNAT 段（标准库不算私有）"},
		{"240.0.0.1", "保留段"},
		{"::1", "IPv6 环回"},
		{"fe80::1", "IPv6 链路本地"},
		{"fd00::1", "IPv6 ULA"},
		{"", "空串"},
		{"not-an-ip", "非法格式"},
	}
	for _, c := range private {
		got := isPublicIP(c.ip)
		// 172.32.0.1 是边界外的公网地址，期望 true
		if c.ip == "172.32.0.1" {
			if !got {
				t.Errorf("%s 应被放行（172.32 不在 16-31 内）", c.ip)
			}
			continue
		}
		if got {
			t.Errorf("%s（%s）应被拒绝", c.ip, c.why)
		}
	}
}

// TestLookup_解析成功
func TestLookup_解析成功(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("fields"); got == "" {
			t.Error("应带 fields 参数以减小响应体")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","country":"Japan","countryCode":"jp",
			"regionName":"Tokyo","city":"Tokyo","lat":35.6901,"lon":139.6917,
			"timezone":"Asia/Tokyo"}`))
	}))
	defer srv.Close()

	r := newTestResolver(t, srv.URL)
	loc, err := r.Lookup(context.Background(), "43.250.175.188")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	// 国家代码必须大写：前端 CountryFlag 按大写查映射
	if loc.CountryCode != "JP" {
		t.Errorf("CountryCode = %q，期望 JP（必须大写）", loc.CountryCode)
	}
	if loc.City != "Tokyo" || loc.Lat == 0 || loc.Lon == 0 {
		t.Errorf("经纬度或城市缺失: %+v", loc)
	}
}

// TestLookup_业务错误
//
// 第三方用 status 字段表达业务错误，HTTP 仍是 200。
// 不检查 status 会把失败当成功，地理位置写成空值。
func TestLookup_业务错误(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"fail","message":"private range"}`))
	}))
	defer srv.Close()

	r := newTestResolver(t, srv.URL)
	if _, err := r.Lookup(context.Background(), "1.2.3.4"); err == nil {
		t.Error("status=fail 时应返回错误，不能当成功")
	}
}

// TestLookup_缓存命中
//
// ip-api.com 免费额度 45 次/分钟。没缓存的话节点重连就会触限。
func TestLookup_缓存命中(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"status":"success","countryCode":"HK","city":"Hong Kong","lat":22.3,"lon":114.1}`))
	}))
	defer srv.Close()

	r := newTestResolver(t, srv.URL)
	for i := 0; i < 5; i++ {
		if _, err := r.Lookup(context.Background(), "1.1.1.1"); err != nil {
			t.Fatalf("第 %d 次查询失败: %v", i+1, err)
		}
	}
	if calls != 1 {
		t.Errorf("5 次查询应只发1 次请求（缓存生效），实际发了 %d 次", calls)
	}
}

// TestLookup_失败退避
//
// 第三方挂掉后必须暂停查询，否则每个节点都会去重试，
// 相当于帮着把它彻底打垮。
func TestLookup_失败退避(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := newTestResolver(t, srv.URL)
	// 连续失败 3 次
	for i := 0; i < 3; i++ {
		_, _ = r.Lookup(context.Background(), "1.1.1.1")
	}
	if calls != 3 {
		t.Fatalf("退避前应发出 %d 次请求，实际 %d 次", 3, calls)
	}
	// 第 4 次应被退避拦住
	_, err := r.Lookup(context.Background(), "1.1.1.1")
	if err == nil {
		t.Fatal("退避期内应返回错误")
	}
	if calls != 3 {
		t.Errorf("退避期内不应再发请求，实际发了 %d 次", calls)
	}
	_, _, backingOff := r.Stats()
	if !backingOff {
		t.Error("Stats 应报告处于退避状态")
	}
}

// TestLookup_禁用时不出网
func TestLookup_禁用时不出网(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"status":"success","countryCode":"HK"}`))
	}))
	defer srv.Close()

	r := newTestResolver(t, srv.URL)
	r.enabled = false
	if _, err := r.Lookup(context.Background(), "1.1.1.1"); err == nil {
		t.Error("禁用时应返回错误")
	}
	if calls != 0 {
		t.Errorf("禁用时不应发出任何请求，实际发了 %d 次", calls)
	}
}

// TestLookup_内网IP不外发
func TestLookup_内网IP不外发(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"status":"success","countryCode":"HK"}`))
	}))
	defer srv.Close()

	r := newTestResolver(t, srv.URL)
	for _, ip := range []string{"192.168.1.1", "10.0.0.1", "127.0.0.1"} {
		if _, err := r.Lookup(context.Background(), ip); err == nil {
			t.Errorf("内网地址 %s 应被拒绝", ip)
		}
	}
	if calls != 0 {
		t.Errorf("内网地址不应发给第三方，实际发了 %d 次", calls)
	}
}

// newTestResolver 构造指向测试服务器的解析器。
//
// 关键：把 baseURL 指向 httptest，这样"解析成功""缓存命中"
// "失败退避"这些路径都能被真实覆盖——
// 只测 isPublicIP 等纯函数的话，HTTP 调用链上的问题测不出来。
func newTestResolver(t *testing.T, baseURL string) *Resolver {
	t.Helper()
	r := New(true, 2*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.baseURL = baseURL + "/"
	return r
}
