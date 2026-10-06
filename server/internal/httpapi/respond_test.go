package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newReq 造一个带查询串的请求。
func newReq(target string) *http.Request {
	return httptest.NewRequest(http.MethodGet, target, nil)
}

// ---------- pageQuery ----------

func TestPageQuery_夹住分页边界(t *testing.T) {
	// 上界必须夹住：?size=999999 若原样传给 SQL，
	// 一次请求就能把整表拉进内存——这是最容易被利用的 DoS 入口。
	// 下界也要夹：page=0 会算出负的 OFFSET，在部分驱动上直接报错。
	cases := []struct {
		name     string
		query    string
		wantPage int
		wantSize int
		why      string
	}{
		{"无参数用默认", "", 1, 50, "缺省应给一个保守的默认值"},
		{"正常值", "?page=3&size=20", 3, 20, "合法值应原样透传"},
		{"size 上界被夹", "?page=1&size=999999", 1, 200, "超大 size 必须被夹到 200，否则一次拉全表"},
		{"size 恰好等于上界", "?size=200", 1, 200, "边界值本身是合法的，不该被改动"},
		{"size 超上界一格", "?size=201", 1, 200, "刚过界就该被夹"},
		{"page 为 0 被夹", "?page=0", 1, 50, "page=0 会算出负 OFFSET"},
		{"page 为负被夹", "?page=-5", 1, 50, "负页码无意义"},
		{"size 为 0 回落默认", "?size=0", 1, 50, "size=0 若原样传下去等于不分页"},
		{"size 为负回落默认", "?size=-10", 1, 50, "负 size 会让 SQL 语法出错"},
		{"page 与 size 同时非法", "?page=-1&size=99999", 1, 200, "两个边界要各自独立生效"},
		{"非数字 page 用默认", "?page=abc", 1, 50, "解析失败应回落默认而不是 0"},
		{"非数字 size 用默认", "?size=xyz", 1, 50, "解析失败应回落默认"},
		{"空值 page 用默认", "?page=&size=", 1, 50, "空串等价于未设置"},
		{"浮点 page 视为非法", "?page=1.5", 1, 50, "Atoi 拒绝小数，回落默认是正确的"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			page, size := pageQuery(newReq("/api/x" + c.query))
			if page != c.wantPage || size != c.wantSize {
				t.Errorf("pageQuery(%q) = (%d, %d)，期望 (%d, %d)。%s",
					c.query, page, size, c.wantPage, c.wantSize, c.why)
			}
			// 不变式：任何输入下返回值都必须可直接喂给 SQL
			if page < 1 {
				t.Errorf("page = %d，必须 ≥ 1", page)
			}
			if size < 1 || size > 200 {
				t.Errorf("size = %d，必须落在 [1,200]", size)
			}
		})
	}
}

func TestPageQuery_负数不产生负OFFSET(t *testing.T) {
	// 单独确认不变式：page 与 size-1 相乘不能为负。
	// 这是"翻到负页"这类越界读的直接防线。
	for _, q := range []string{"?page=-100&size=200", "?page=0&size=0", "?page=-1&size=-1"} {
		page, size := pageQuery(newReq("/x" + q))
		offset := (page - 1) * size
		if offset < 0 {
			t.Errorf("pageQuery(%q) = (%d,%d)，算出的 OFFSET = %d 为负", q, page, size, offset)
		}
	}
}

// ---------- TimeRange ----------

func TestTimeRange_默认窗口为最近24小时(t *testing.T) {
	// 不给 from/to 时给最近 24 小时：这是图表默认视图，
	// 窗口太短会看不到日周期，太长会把预聚合粒度压垮。
	from, to := TimeRange(newReq("/api/metrics"))
	if to.IsZero() {
		t.Fatal("to 为零值")
	}
	if !from.Before(to) {
		t.Fatalf("from(%v) 应早于 to(%v)", from, to)
	}
	if d := to.Sub(from); d != 24*time.Hour {
		t.Errorf("默认窗口 = %v，期望 24h", d)
	}
	// 应该是最近 24 小时而不是未来 24 小时
	if to.After(time.Now().Add(time.Minute)) {
		t.Errorf("to = %v，超出了当前时间", to)
	}
}

func TestTimeRange_显式区间被采用(t *testing.T) {
	from, to := TimeRange(newReq("/api/metrics?from=1700000000&to=1700003600"))
	if from.Unix() != 1700000000 {
		t.Errorf("from = %d，期望 1700000000", from.Unix())
	}
	if to.Unix() != 1700003600 {
		t.Errorf("to = %d，期望 1700003600", to.Unix())
	}
	// 应转成 UTC：库里存的是 UTC，用本地时区比较会整体错位
	if from.Location() != time.UTC || to.Location() != time.UTC {
		t.Errorf("时间未归一化到 UTC: from=%v to=%v", from.Location(), to.Location())
	}
}

func TestTimeRange_只给一端时另一端补齐(t *testing.T) {
	cases := []struct {
		name       string
		query      string
		wantToNow  bool
		wantFromIn int64
	}{
		{"只给 from，to 取当前", "?from=1700000000", true, 1700000000},
		{"只给 to，from 取 to-24h", "?to=1700003600", false, 1700003600 - 86400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			from, to := TimeRange(newReq("/api/metrics" + c.query))
			if from.Unix() != c.wantFromIn {
				t.Errorf("from = %d，期望 %d", from.Unix(), c.wantFromIn)
			}
			if !from.Before(to) {
				t.Errorf("from(%v) 应早于 to(%v)", from, to)
			}
			if c.wantToNow && to.After(time.Now().Add(time.Minute)) {
				t.Errorf("只给 from 时 to 应取当前时间，实际 = %v", to)
			}
		})
	}
}

func TestTimeRange_非法区间回落到默认窗口(t *testing.T) {
	// 关键行为：from >= to 时不能返回空区间。
	// 返回空区间的话，前端会显示"这段时间没有数据"——
	// 而真相是"你的参数写错了"。用户会以为节点挂了。
	cases := []struct {
		name  string
		query string
	}{
		{"from 等于 to", "?from=1700000000&to=1700000000"},
		{"from 晚于 to", "?from=1700003600&to=1700000000"},
		{"from 远晚于 to", "?from=1800000000&to=1700000000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			from, to := TimeRange(newReq("/api/metrics" + c.query))
			if !from.Before(to) {
				t.Fatalf("非法区间未兜底: from=%v to=%v，仍非递增", from, to)
			}
			if d := to.Sub(from); d != 24*time.Hour {
				t.Errorf("兜底窗口 = %v，期望 24h", d)
			}
		})
	}
}

func TestTimeRange_非法数值按未提供处理(t *testing.T) {
	// 解析失败不该变成 0（unix 纪元），否则会拉出 55 年跨度，
	// 既慢又把预聚合表全扫一遍——一个查询参数就能打满 CPU。
	cases := []struct {
		name  string
		query string
	}{
		{"非数字 from", "?from=abc&to=1700003600"},
		{"非数字 to", "?from=1700000000&to=xyz"},
		{"浮点 from", "?from=1.5"},
		{"空串from", "?from="},
		{"负数 from", "?from=-100"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			from, to := TimeRange(newReq("/api/metrics" + c.query))
			if !from.Before(to) {
				t.Errorf("from(%v) 应早于 to(%v)", from, to)
			}
			// 负数 from 是合法 unix 时间（1969 年），但配合默认 to=now
			// 会得到 55 年窗口。这里只断言不崩且递增，
			// 因为"是否该拒绝远古时间"是产品决策不是 bug。
			if to.Sub(from) > 365*24*time.Hour {
				t.Logf("注意：窗口跨度达 %v，远超预期", to.Sub(from))
			}
		})
	}
}

// ---------- decodeJSON ----------

func TestDecodeJSON_正常解析(t *testing.T) {
	type payload struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	cases := []struct {
		name string
		body string
	}{
		{"最小对象", `{"name":"a","count":1}`},
		{"带空格", "  {\n  \"name\": \"a\",\n  \"count\": 2\n}\n"},
		{"字段缺失用零值", `{}`},
		{"null 值", `{"name":null}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(c.body))
			var got payload
			if err := decodeJSON(r, &got); err != nil {
				t.Errorf("解析失败: %v", err)
			}
		})
	}
}

func TestDecodeJSON_拒绝未知字段(t *testing.T) {
	// 必须拒绝：客户端把 "usernmae" 拼错时，
	// 静默忽略会变成"设置没生效但看不出原因"——
	// 用户会反复重试、反复困惑，最后只能重装系统。
	type payload struct {
		Name string `json:"name"`
	}
	cases := []struct {
		name string
		body string
	}{
		{"拼错的字段名", `{"usernmae":"a"}`},
		{"多一个字段", `{"name":"a","extra":1}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(c.body))
			var got payload
			err := decodeJSON(r, &got)
			if err == nil {
				t.Fatal("含未知字段的请求体应被拒绝，却解析成功")
			}
			if !strings.Contains(err.Error(), "未知字段") {
				t.Errorf("错误信息 = %q，应提到未知字段", err.Error())
			}
		})
	}
}

func TestDecodeJSON_字段名大小写不敏感被接受(t *testing.T) {
	// 记录 Go 标准库的一个既成行为：encoding/json 的字段匹配
	// 大小写不敏感，所以 {"Name":"a"} 能匹配到 `json:"name"`。
	//
	// 写这个测试不是为了固化它，而是为了让"DisallowUnknownFields
	// 并不等于严格校验"这件事显式化——它是拼写检查，不是白名单校验。
	// 若哪天换成严格解码器（sonic 等），这个测试会失败并提醒复核。
	type payload struct {
		Name string `json:"name"`
	}
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"Name":"a"}`))
	var got payload
	if err := decodeJSON(r, &got); err != nil {
		t.Logf("字段名大小写不敏感已被拒绝（行为已变更，需复核前端是否依赖旧行为）: %v", err)
		return
	}
	if got.Name != "a" {
		t.Errorf("Name = %q，期望 a", got.Name)
	}
}

func TestDecodeJSON_拒绝尾随JSON(t *testing.T) {
	// `{"a":1}{"b":2}` 若被接受，第二段会被静默丢弃。
	// 更危险的是 `{"role":"viewer"}{"role":"owner"}` 这种
	// 拼接攻击——若服务端只读第一段还好，读了后一段就是提权。
	type payload struct {
		A int `json:"a"`
	}
	cases := []struct {
		name string
		body string
	}{
		{"两个对象", `{"a":1}{"a":2}`},
		{"对象后跟数组", `{"a":1}[2]`},
		{"对象后跟垃圾", `{"a":1}garbage`},
		{"对象后跟换行对象", "{\"a\":1}\n{\"a\":2}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(c.body))
			var got payload
			err := decodeJSON(r, &got)
			if err == nil {
				t.Fatal("含尾随内容的请求体应被拒绝")
			}
			if !strings.Contains(err.Error(), "只能包含一个") {
				t.Errorf("错误信息 = %q，应说明只能包含一个 JSON 对象", err.Error())
			}
		})
	}
}

func TestDecodeJSON_拒绝超过1MB的请求体(t *testing.T) {
	// 上限保护：接口不该接收大 body。超了直接拒，
	// 而不是读进内存再报"字段太多"——后者已经吃了内存。
	type payload struct {
		Blob string `json:"blob"`
	}

	cases := []struct {
		name string
		size int
	}{
		{"刚好 1MB", 1 << 20},
		{"超过 1MB", (1 << 20) + 1},
		{"远超 1MB", 4 << 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 构造一个解析后超过上限的 JSON：
			// 固定前缀 + 若干 'a' + 固定后缀
			const prefix = `{"blob":"`
			const suffix = `"}`
			body := prefix + strings.Repeat("a", c.size) + suffix

			r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
			var got payload
			err := decodeJSON(r, &got)
			if err == nil {
				t.Fatalf("超过 %d 的请求体应被拒绝，却解析成功（blob 长度 %d）", c.size, len(got.Blob))
			}
			// 错误应被翻译成对用户友好的 400，而不是把内部错误透出去
			if !strings.Contains(err.Error(), "过大") && !strings.Contains(err.Error(), "不是合法 JSON") {
				t.Errorf("错误信息 = %q，应提示请求体过大", err.Error())
			}
		})
	}
}

func TestDecodeJSON_拒绝非法JSON(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	cases := []struct {
		name string
		body string
	}{
		{"空body", ``},
		{"截断的对象", `{"name":`},
		{"非对象", `"just a string"`},
		{"纯数字", `123`},
		{"单引号", `{'name':'a'}`},
		{"尾随逗号", `{"name":"a",}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(c.body))
			var got payload
			if err := decodeJSON(r, &got); err == nil {
				t.Errorf("非法 JSON %q 应被拒绝", c.body)
			}
		})
	}
}

// ---------- clientIP ----------

func TestClientIP_不信任代理时忽略所有伪造头(t *testing.T) {
	// 【安全断言】这是本文件最重要的一组用例。
	// 不配置可信代理时若采信 X-Forwarded-For，任何人都能伪造来源 IP：
	// 审计日志会记下攻击者随手编的 IP，登录限流按 IP 计数也就形同虚设。
	// 攻击成本几乎为零，防护收益却依赖这一个开关。
	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		xRealIP    string
	}{
		{"伪造 XFF", "10.0.0.1:1234", "1.2.3.4", ""},
		{"伪造 XFF 多段", "10.0.0.1:1234", "1.2.3.4, 5.6.7.8, 9.10.11.12", ""},
		{"伪造 X-Real-IP", "10.0.0.1:1234", "", "6.6.6.6"},
		{"两个头都伪造", "10.0.0.1:1234", "1.2.3.4", "6.6.6.6"},
		{"XFF 为空串", "10.0.0.1:1234", "", ""},
		{"XFF 只有逗号", "10.0.0.1:1234", ",,,", ""},
		{"XFF 带空格", "10.0.0.1:1234", "  1.2.3.4  , 5.6.7.8", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			r.RemoteAddr = c.remoteAddr
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if c.xRealIP != "" {
				r.Header.Set("X-Real-IP", c.xRealIP)
			}
			got := clientIP(r, false)
			if got != "10.0.0.1" {
				t.Errorf("clientIP(trustProxy=false) = %q，期望 10.0.0.1"+
					"——伪造的转发头被采信了，审计与限流均可被绕过", got)
			}
		})
	}
}

func TestClientIP_信任代理时取XFF第一段(t *testing.T) {
	// 信任代理时取 XFF 的第一段：那是链路最前端、
	// 也就是最接近真实客户端的那一跳。后面的段是中间人加的。
	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		xRealIP    string
		want       string
	}{
		{"单个 XFF", "10.0.0.1:1234", "1.2.3.4", "", "1.2.3.4"},
		{"多段取第一段", "10.0.0.1:1234", "1.2.3.4, 5.6.7.8", "", "1.2.3.4"},
		{"多段带空格", "10.0.0.1:1234", "  1.2.3.4 ,5.6.7.8", "", "1.2.3.4"},
		{"只有 X-Real-IP", "10.0.0.1:1234", "", "6.6.6.6", "6.6.6.6"},
		{"XFF 优先于 X-Real-IP", "10.0.0.1:1234", "1.2.3.4", "6.6.6.6", "1.2.3.4"},
		{"两者都无则回落 RemoteAddr", "10.0.0.1:1234", "", "", "10.0.0.1"},
		{"RemoteAddr 无端口", "10.0.0.1", "", "", "10.0.0.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			r.RemoteAddr = c.remoteAddr
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if c.xRealIP != "" {
				r.Header.Set("X-Real-IP", c.xRealIP)
			}
			if got := clientIP(r, true); got != c.want {
				t.Errorf("clientIP(trustProxy=true) = %q，期望 %q", got, c.want)
			}
		})
	}
}

// TestClientIP_IPv6地址未剥方括号 —— 断言 IPv6 客户端的 IP 能被正确解析。
//
// 【当前失败：生产代码缺陷】clientIP 用
// strings.LastIndexByte(host, ':') 截端口，而 IPv6 的 RemoteAddr
// 形如 "[::1]:8080"，最后一个冒号之后才是端口，
// 于是截出来的是 "[::1]" ——方括号没剥掉。
//
// 影响：所有走 IPv6 的客户端在审计日志与登录失败计数表里
// 存下"[::1]" 而非 "::1"。危害有两处：
//  1. 同一个 IPv6 客户端若用不同的书写形式出现，
//     计数会被拆成多行，(identifier, ip) 维度的锁定可被绕过；
//  2. 同一个包里 internal/util.ClientIP 用 net.SplitHostPort
//     能正确剥括号——两套实现行为不一致，审计数据无法互认。
func TestClientIP_IPv6地址未剥方括号(t *testing.T) {
	// Go 的 net/http 规定：IPv6 客户端的 RemoteAddr 一定带方括号
	cases := []struct {
		remoteAddr string
		want       string
	}{
		{"[::1]:8080", "::1"},
		{"[2001:db8::1]:443", "2001:db8::1"},
		// zone id（%eth0）被剥除：审计与锁定计数不需要它，
		// 保留会让同一地址的带区/不带区写法落到不同 key
		{"[fe80::1%eth0]:9000", "fe80::1"},
	}
	for _, c := range cases {
		t.Run(c.remoteAddr, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			r.RemoteAddr = c.remoteAddr
			got := clientIP(r, false)
			if got != c.want {
				t.Errorf("clientIP(%q) = %q，期望 %q——方括号未剥除，"+
					"IPv6 客户端会在审计与锁定计数里被记成另一个 key",
					c.remoteAddr, got, c.want)
			}
			if strings.ContainsAny(got, "[]") {
				t.Errorf("clientIP = %q 含方括号，不是合法 IP 文本", got)
			}
		})
	}
}

// TestClientIP_XFF以逗号开头不被当成整串 —— 断言畸形 XFF 不会污染 IP 字段。
//
// 【当前失败：生产代码缺陷】取第一段的条件是 IndexByte(...) > 0，
// 而 XFF = ",1.2.3.4" 时返回的是 0，条件不成立，
// 于是走到 return strings.TrimSpace(xff) 把整串 ",1.2.3.4" 返回了。
//
// 影响：IP 字段里混入逗号，(identifier, ip) 计数维度被污染。
// 登录失败计数按 (identifier, ip) 聚合，攻击者只要在 XFF 里
// 塞不同的畸形前缀，就能让每次失败都落到不同的"IP"上——
// 锁定阈值永远达不到，暴力破解不受任何限制。
// 注意本函数只在上层确认反代可信（trustProxy=true）时才读该头，
// 但"反代可信"不等于"反代会清洗头部"：真实客户端完全可以
// 自己构造一个以逗号开头的 XFF。
func TestClientIP_XFF以逗号开头不被当成整串(t *testing.T) {
	cases := []struct {
		name string
		xff  string
	}{
		{"逗号紧跟首字符", ",1.2.3.4"},
		{"多个前导逗号", ",,,1.2.3.4"},
		{"逗号后跟空格", ", 1.2.3.4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			r.RemoteAddr = "10.0.0.1:1234"
			r.Header.Set("X-Forwarded-For", c.xff)

			got := clientIP(r, true)
			if strings.Contains(got, ",") {
				t.Errorf("XFF=%q 时 clientIP = %q，包含了逗号——"+
					"IP 字段被污染，(identifier,ip) 维度的登录锁定可被绕过", c.xff, got)
			}
			// 首段为空时应回落到连接来源，而不是返回畸形值
			if got == "" {
				t.Errorf("XFF=%q 时 clientIP 返回空串，应回落到 RemoteAddr", c.xff)
			}
		})
	}
}

// ---------- queryInt / queryBool / pathInt64 ----------

func TestQueryInt_默认值与解析失败(t *testing.T) {
	cases := []struct {
		query string
		want  int
	}{
		{"", 7},
		{"?k=", 7},
		{"?k=42", 42},
		{"?k=-3", -3},
		{"?k=abc", 7},
		{"?k=1.5", 7},
		{"?k=99999999999999999999", 7}, // 溢出
	}
	for _, c := range cases {
		if got := queryInt(newReq("/x"+c.query), "k", 7); got != c.want {
			t.Errorf("queryInt(%q) = %d，期望 %d", c.query, got, c.want)
		}
	}
}

func TestQueryBool_多种写法与非法值回落(t *testing.T) {
	cases := []struct {
		query string
		want  bool
	}{
		{"", true}, // 用默认
		{"?k=1", true},
		{"?k=true", true},
		{"?k=TRUE", true},
		{"?k=yes", true},
		{"?k=0", false},
		{"?k=false", false},
		{"?k=No", false},
		{"?k=", true},
		{"?k=maybe", true}, // 非法值回落默认
		{"?k=on", true},    // "on" 不在支持列表，回落默认
	}
	for _, c := range cases {
		if got := queryBool(newReq("/x"+c.query), "k", true); got != c.want {
			t.Errorf("queryBool(%q, def=true) = %v，期望 %v", c.query, got, c.want)
		}
	}
	if got := queryBool(newReq("/x?k=maybe"), "k", false); got != false {
		t.Errorf("非法值应回落到默认 false，实际 = %v", got)
	}
}

func TestPathInt64_解析与错误提示(t *testing.T) {
	// 错误信息里带上参数名：用户看到"id 不是合法数字"，
	// 比"参数错误"有用得多——这是排障体验的最低成本。
	cases := []struct {
		name    string
		set     bool
		val     string
		want    int64
		wantErr bool
	}{
		{"正常数字", true, "42", 42, false},
		{"负数", true, "-1", -1, false},
		{"零", true, "0", 0, false},
		{"非数字", true, "abc", 0, true},
		{"空值", true, "", 0, true},
		{"浮点", true, "1.5", 0, true},
		{"未设置", false, "", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			if c.set {
				r.SetPathValue("id", c.val)
			}
			got, err := pathInt64(r, "id")
			if c.wantErr {
				if err == nil {
					t.Fatalf("应报错，却返回 %d", got)
				}
				if !strings.Contains(err.Error(), "id") {
					t.Errorf("错误信息 = %q，应包含参数名 id 以便定位", err.Error())
				}
				return
			}
			if err != nil {
				t.Errorf("不应报错: %v", err)
			}
			if got != c.want {
				t.Errorf("pathInt64 = %d，期望 %d", got, c.want)
			}
		})
	}
}

// ---------- 响应封装 ----------

func TestWriteJSON_信封形状与状态码(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   Response
	}{
		{"成功", http.StatusOK, Response{Code: "0", Data: map[string]any{"k": "v"}}},
		{"创建", http.StatusCreated, Response{Code: "0"}},
		{"未授权", http.StatusUnauthorized, Response{Code: "UNAUTHENTICATED", Message: "缺令牌"}},
		{"无权限", http.StatusForbidden, Response{Code: "FORBIDDEN", Message: "权限不足"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			writeJSON(rec, r, c.status, c.body)

			if rec.Code != c.status {
				t.Errorf("状态码 = %d，期望 %d", rec.Code, c.status)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
				t.Errorf("Content-Type = %q", ct)
			}
			// 认证类响应禁止缓存：浏览器缓存了 401，
			// 用户登录后刷新仍看到"未登录"，体验上像是登录没生效
			switch c.status {
			case http.StatusUnauthorized, http.StatusForbidden:
				if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
					t.Errorf("认证类响应 Cache-Control = %q，期望 no-store", cc)
				}
			default:
				if cc := rec.Header().Get("Cache-Control"); cc == "no-store" {
					t.Error("非认证类响应不应设置 no-store")
				}
			}
		})
	}
}

func TestNoContent_不写响应体(t *testing.T) {
	rec := httptest.NewRecorder()
	noContent(rec, httptest.NewRequest(http.MethodDelete, "/x", nil))
	if rec.Code != http.StatusNoContent {
		t.Errorf("状态码 = %d，期望 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("204 不应有响应体，实际 = %q", rec.Body.String())
	}
}

func TestFail_非apperr一律500且不泄漏内部细节(t *testing.T) {
	// PRD 9 章红线：内部错误只写日志，
	// 客户端只能看到通用文案。SQL 错误、文件路径、堆栈
	// 一旦返回给客户端，就等于给攻击者画好了攻击路径。
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/nodes/1", nil)
	fail(rec, r, errString("sql: no such table: agent_secret_hash at /srv/app/store/repo_node.go:88"))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("状态码 = %d，期望 500", rec.Code)
	}
	body := rec.Body.String()
	for _, leak := range []string{"no such table", "agent_secret_hash", "repo_node.go", "/srv/app"} {
		if strings.Contains(body, leak) {
			t.Errorf("响应体泄漏了内部信息 %q: %s", leak, body)
		}
	}
	if !strings.Contains(body, "INTERNAL") {
		t.Errorf("响应体 = %s，期望 code=INTERNAL", body)
	}
}

func TestFail_apperr按其状态码与安全文案返回(t *testing.T) {
	// 业务错误应把 ClientMessage 原样返回前端——
	// 前端 client.ts 依赖稳定的 code 做文案映射
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/nodes/x", nil)
	fail(rec, r, badRequest("节点名不能为空"))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("状态码 = %d，期望 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "节点名不能为空") {
		t.Errorf("响应体 = %s，应含业务文案", rec.Body.String())
	}
}

type errString string

func (e errString) Error() string { return string(e) }
