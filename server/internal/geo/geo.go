// Package geo 通过第三方 API 把公网 IP 解析为地理信息。
//
// 为什么用第三方 API 而不是本地 IP 库：
//   - 本地库要定期更新数据文件，运维成本高
//   - 节点数量有限（几十个量级），查询频率极低——
//     每个新节点查一次就够，缓存策略简单
//   - 换IP 库不用重新部署
//
// 隐私与合规：
//   - 只把**节点自己的公网 IP** 发给第三方，不发任何其他信息
//   - 这个 IP 本来就是公开的（能连到它的服务器都知道）
//   - 关闭开关（PROBEONE_GEO_ENABLED=false）后完全不发外部请求
//
// 精度限制（重要）：
//
//	第三方库对**机房 IP** 的定位常常不准——同一机房的 IP 可能
//	全部落在同一个默认坐标上（如东京）。所以结果仅用于
//	世界地图上的大致打点，**不能用于定位具体机房**。
//	用户可以在面板上手动修正，manual 标记会阻止自动覆盖。
package geo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Location 是解析结果。
type Location struct {
	CountryCode string  // ISO 3166-1 alpha-2，如 HK
	CountryName string  // 英文名，仅用于日志
	City        string  // 城市名（可能为空）
	Lat         float64 // 纬度
	Lon         float64 // 经度
	Timezone    string
	// Source 标明数据来源，便于排查"为什么位置不对"
	Source string
	// Manual 为 true 表示用户手工设置过，自动解析不应覆盖。
	Manual bool
}

// Resolver 解析 IP 到地理位置。
type Resolver struct {
	client  *http.Client
	log     *slog.Logger
	enabled bool
	// baseURL 是第三方接口地址。抽成字段是为了能在测试里指向
	// httptest 服务器——真实实现里只有一个值，但写死会让
	// "成功路径"无法被测试覆盖。
	baseURL string

	// cache 按 IP 缓存结果。
	//
	// 为什么需要缓存：ip-api.com 的免费额度是 45次/分钟。
	// 节点重连时会重复上报同一个 IP，没有缓存会很快触限。
	// TTL 长一点也没关系——IP 归属不会频繁变化。
	mu    sync.RWMutex
	cache map[string]cacheEntry

	// failUntil 记录连续失败后的退避时间。
	// 第三方挂了就全站失败，必须退避而不是每个节点都去重试。
	failUntil time.Time
	fails     int
}

type cacheEntry struct {
	loc     Location
	expires time.Time
}

// DefaultAPI 是默认的第三方接口。
//
// 选它的原因：无需注册与key，单次请求有额度（45次/分钟），
// 且 fields 参数能只返回地理信息。
// 额度对 ProbeUse场景足够——节点数量有限，缓存后请求更少。
//
// 注意它是**明文 HTTP**：请求里只有节点自己的公网 IP，
// 不含任何凭据或敏感信息。即便如此，如果部署环境要求
// 全链路加密，可通过 config 覆盖为 HTTPS 版本或自建服务。
const DefaultAPI = "http://ip-api.com/json/"

const (
	// cacheTTL 是缓存有效期。IP 归属很少变，1 小时足够。
	cacheTTL = time.Hour
	// failThreshold 是进入退避所需的连续失败次数。
	// 之前是 1 次就退避，会把偶发网络抖动放大成整分钟不可用。
	failThreshold = 3
	// failBackoffBase 是失败退避基数。
	failBackoffBase = 1 * time.Minute
	// failBackoffMax 是退避上限。
	failBackoffMax = 30 * time.Minute
	// maxBody 限制响应体大小：第三方返回不该超过几 KB。
	maxBody = 16 << 10
)

// New 创建解析器。
//
// enabled 为 false 时所有查询直接返回错误，不会发出任何外部请求——
// 这个开关的存在是为了让"完全不出网"的部署有明确选项。
func New(enabled bool, timeout time.Duration, log *slog.Logger) *Resolver {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Resolver{
		client:  &http.Client{Timeout: timeout},
		log:     log,
		enabled: enabled,
		baseURL: DefaultAPI,
		cache:   make(map[string]cacheEntry),
	}
}

// ipApiResponse 是 ip-api.com 的响应子集。
type ipApiResponse struct {
	Status      string  `json:"status"`
	Message     string  `json:"message"`
	Country     string  `json:"country"`
	CountryCode string  `json:"countryCode"`
	RegionName  string  `json:"regionName"`
	City        string  `json:"city"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	Timezone    string  `json:"timezone"`
}

// SetAPI 覆盖第三方接口地址。
//
// 用于指向自建服务：公网 API 不可用或不想出网时，
// 可以部署自己的解析服务，保持相同的响应格式即可。
func (r *Resolver) SetAPI(base string) {
	if base == "" {
		return
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	r.baseURL = base
}

// APIURL 返回当前使用的接口地址（启动日志用）。
func (r *Resolver) APIURL() string { return r.baseURL }

// Lookup 解析 IP。
//
// 返回错误时调用方应**保持原有字段不变**，而不是清空——
// 第三方服务临时不可用不该让已有的地理位置消失。
func (r *Resolver) Lookup(ctx context.Context, ip string) (Location, error) {
	ip = strings.TrimSpace(ip)
	if !r.enabled {
		return Location{}, fmt.Errorf("地理解析已禁用（PROBEONE_GEO_ENABLED=false）")
	}
	if !isPublicIP(ip) {
		// 内网 IP 没有地理意义，且**不能发给第三方**——
		// 那是内网拓扑信息。
		return Location{}, fmt.Errorf("IP %s 不是公网地址，跳过解析", ip)
	}

	// 缓存
	r.mu.RLock()
	if e, ok := r.cache[ip]; ok && time.Now().Before(e.expires) {
		r.mu.RUnlock()
		return e.loc, nil
	}
	r.mu.RUnlock()

	// 退避：连续失败后暂停查询，避免第三方挂掉时把它彻底打垮
	r.mu.RLock()
	if now := time.Now(); now.Before(r.failUntil) {
		wait := r.failUntil.Sub(now).Round(time.Second)
		r.mu.RUnlock()
		return Location{}, fmt.Errorf("地理解析服务连续失败，%s 后重试", wait)
	}
	r.mu.RUnlock()

	loc, err := r.query(ctx, ip)
	if err != nil {
		r.recordFailure(ip)
		return Location{}, err
	}
	r.recordSuccess(ip, loc)
	return loc, nil
}

// query 调用第三方 API。
func (r *Resolver) query(ctx context.Context, ip string) (Location, error) {
	// fields 参数能显著减小响应体——默认返回几十 KB 的
	// ISP 与货币信息，我们只要地理信息。
	url := r.baseURL + ip + "?fields=status,country,countryCode,regionName,city,lat,lon,timezone"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Location{}, fmt.Errorf("构造请求失败: %w", err)
	}
	// 显式声明 UA：某些 API 对空 UA 直接拒绝
	req.Header.Set("User-Agent", "ProbeOne/1.0 (self-hosted monitoring)")

	resp, err := r.client.Do(req)
	if err != nil {
		return Location{}, fmt.Errorf("请求失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Location{}, fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Location{}, fmt.Errorf("第三方返回 HTTP %d", resp.StatusCode)
	}

	var parsed ipApiResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Location{}, fmt.Errorf("解析响应失败: %w", err)
	}
	// 该 API 用 status 字段表达业务错误，HTTP 仍是 200
	if parsed.Status != "success" {
		return Location{}, fmt.Errorf("第三方返回错误: %s", parsed.Message)
	}
	if parsed.CountryCode == "" {
		return Location{}, fmt.Errorf("第三方未返回国家代码")
	}

	return Location{
		CountryCode: strings.ToUpper(parsed.CountryCode),
		CountryName: parsed.Country,
		City:        parsed.City,
		Lat:         parsed.Lat,
		Lon:         parsed.Lon,
		Timezone:    parsed.Timezone,
		Source:      "ip-api.com",
	}, nil
}

// recordSuccess 写缓存并清零失败计数。
func (r *Resolver) recordSuccess(ip string, loc Location) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[ip] = cacheEntry{loc: loc, expires: time.Now().Add(cacheTTL)}
	r.fails = 0
	r.failUntil = time.Time{}
}

// recordFailure 记录失败并设置退避。
func (r *Resolver) recordFailure(ip string) {
	r.mu.Lock()
	r.fails++
	fails := r.fails
	if fails < failThreshold {
		/*
		 * 前几次失败不进入退避。
		 *
		 * 网络抖动、第三方瞬时 5xx 都很常见，立刻退避 1 分钟
		 * 会让"偶发失败"变成"整分钟不可用"——
		 * 而节点接入是低频事件，误判成本很高。
		 * 连续失败到阈值才认为对方真的出问题了。
		 */
		r.mu.Unlock()
		r.log.Warn("地理解析失败",
			slog.String("ip", ip),
			slog.Int("consecutive_fails", fails),
			slog.Int("threshold", failThreshold))
		return
	}
	// 指数退避：failThreshold 次之后开始，每再失败一次翻倍
	backoff := failBackoffBase
	for i := failThreshold; i < fails && backoff < failBackoffMax; i++ {
		backoff *= 2
	}
	if backoff > failBackoffMax {
		backoff = failBackoffMax
	}
	r.failUntil = time.Now().Add(backoff)
	// 失败也要清缓存：可能是上次的结果已经过期且本地数据不准
	delete(r.cache, ip)
	r.mu.Unlock()

	r.log.Warn("地理解析失败",
		slog.String("ip", ip),
		slog.Int("consecutive_fails", fails),
		slog.Duration("backoff", backoff))
}

// isPublicIP 判断是否是公网地址。
//
// 严格排除私有段与保留段。内网 IP 发给第三方等于泄露网络拓扑，
// 而且解析出来也没有地理意义。
//
// 用 net.IP 的 IsPrivate / IsLoopback / IsLinkLocal* 而非手写前缀匹配：
// 手写容易漏（172.16/12 的判断就曾写错成字符串长度比较），
// 标准库把这些网段都定义好了。
func isPublicIP(raw string) bool {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return false
	}
	// 100.64.0.0/10 是 CGNAT 段（运营商 NAT），
	// 标准库不把它算作 Private，但同样不是公网地址。
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return false
		}
		// 240.0.0.0/4 保留段
		if v4[0] >= 240 {
			return false
		}
	}
	return true
}

// Stats 返回解析器状态，用于诊断。
func (r *Resolver) Stats() (cached int, fails int, backingOff bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.cache), r.fails, time.Now().Before(r.failUntil)
}
