// Package node 处理节点相关的 HTTP 接口。
package node

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/m202471895/probeone/server/internal/apperr"
	"github.com/m202471895/probeone/server/internal/auth"
	"github.com/m202471895/probeone/server/internal/httpapi"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/store"
)

// Handler 是节点接口的处理器。
type Handler struct {
	db         *store.DB
	trustProxy bool
	// serverHost 是 HTTP 监听地址（含端口），从 PROBEONE_PUBLIC_URL 提取。
	serverHost string
	// publicURL 保留完整 URL（含协议），用于判断该用 http 还是 https。
	// 只存 host 会丢掉协议信息，导致安装命令写死 https。
	publicURL string
	// grpcAddr 是 Agent 连接地址，缺省回退到 serverHost。
	grpcAddr string
}

// New 创建处理器。
//
// publicURL 用于生成安装命令——必须传**含协议**的完整 URL，
// 因为安装命令里用 http 还是 https 取决于它。传 host:port 会丢协议，
// 导致明文部署时生成 https 命令，用户执行报 TLS 错误。
func New(db *store.DB, trustProxy bool, publicURL string) *Handler {
	h := &Handler{db: db, trustProxy: trustProxy, publicURL: publicURL}
	h.serverHost = hostOnly(publicURL)
	return h
}

// SetGRPCAddr 设置 Agent 连接的 gRPC 地址。
// 不配置时回退到 HTTP 地址（单端口部署场景）。
func (h *Handler) SetGRPCAddr(addr string) { h.grpcAddr = addr }

// hostOnly 从 URL 里取出 host:port。
func hostOnly(raw string) string {
	if raw == "" {
		return ""
	}
	s := raw
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			break
		}
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

// Register 把节点路由挂到 mux。
//
// 鉴权用 RequireAuth + RequireRole 两层：
// 前者确认"是谁"，后者确认"能不能"。顺序不能反——
// 先查角色会在未登录时把"未登录"误报成"权限不足"。
func (h *Handler) Register(mux *http.ServeMux, authn *httpapi.Authenticator) {
	authed := func(hf http.HandlerFunc) http.Handler { return authn.RequireAuth(hf) }
	admin := func(hf http.HandlerFunc) http.Handler {
		return authn.RequireAuth(httpapi.RequireRole(model.RoleAdmin)(hf))
	}

	mux.Handle("GET /api/nodes", authed(h.list))
	mux.Handle("POST /api/nodes", admin(h.create))
	mux.Handle("GET /api/nodes/{uid}", authed(h.get))
	mux.Handle("PUT /api/nodes/{uid}", admin(h.update))
	mux.Handle("DELETE /api/nodes/{uid}", admin(h.delete))
	mux.Handle("POST /api/nodes/{uid}/rotate-secret", admin(h.rotateSecret))
	mux.Handle("GET /api/nodes/{uid}/metrics", authed(h.metrics))

	mux.Handle("GET /api/groups", authed(h.listGroups))
	mux.Handle("POST /api/groups", admin(h.createGroup))
	// 分组的改名与删除：前端分组管理界面需要。
	// 后端此前只有"列出"与"新建"，改名只能删了重建——
	// 那会丢掉 sort 之外的东西，也让界面没法做行内编辑。
	mux.Handle("PUT /api/groups/{id}", admin(h.updateGroup))
	mux.Handle("DELETE /api/groups/{id}", admin(h.deleteGroup))
}

// nodeView 是节点对外的 JSON 形状。
//
// 刻意不复用 model.Node：model 里有 secret_hash 等内部字段，
// 独立 DTO 能在编译期保证它们漏不出去（PRD 3.6.4 第 2 条）。
type nodeView struct {
	ID       int64            `json:"id"`
	UID      string           `json:"uid"`
	Name     string           `json:"name"`
	GroupID  *int64           `json:"group_id"`
	Status   model.NodeStatus `json:"status"`
	Remark   string           `json:"remark"`
	IsPublic bool             `json:"is_public"`
	Created  string           `json:"created_at"`

	// A 类：身份信息
	Hostname     string `json:"hostname"`
	OSType       string `json:"os_type"`
	OSVersion    string `json:"os_version"`
	Arch         string `json:"arch"`
	AgentVersion string `json:"agent_version"`

	// B 类：硬件规格
	CPUModel string           `json:"cpu_model"`
	CPUCores int              `json:"cpu_cores"`
	MemTotal int64            `json:"mem_total"`
	DiskInfo []model.DiskInfo `json:"disk_info"`

	// C 类：位置与网络
	GeoCountry string   `json:"geo_country"`
	GeoCity    string   `json:"geo_city"`
	GeoLat     *float64 `json:"geo_lat"`
	GeoLon     *float64 `json:"geo_lon"`
	PublicIP   string   `json:"public_ip"`

	LastSeenAt      string `json:"last_seen_at"`
	LastReportAt    string `json:"last_report_at"`
	HardwareChanged string `json:"hardware_changed_at"`
}

const tsLayout = "2006-01-02T15:04:05Z"

func viewOf(n *model.Node) nodeView {
	v := nodeView{
		ID:           n.ID,
		UID:          n.UID,
		Name:         n.Name,
		GroupID:      n.GroupID,
		Status:       n.Status,
		Remark:       n.Remark,
		IsPublic:     n.IsPublic,
		Created:      n.CreatedAt.Format(tsLayout),
		Hostname:     n.Hostname,
		OSType:       n.OSType,
		OSVersion:    n.OSVersion,
		Arch:         n.Arch,
		AgentVersion: n.AgentVersion,
		CPUModel:     n.CPUModel,
		CPUCores:     n.CPUCores,
		MemTotal:     n.MemTotal,
		DiskInfo:     n.DiskInfo,
		GeoCountry:   n.GeoCountry,
		GeoCity:      n.GeoCity,
		GeoLat:       n.GeoLat,
		GeoLon:       n.GeoLon,
		PublicIP:     n.PublicIP,
	}
	if n.LastSeenAt != nil {
		v.LastSeenAt = n.LastSeenAt.Format(tsLayout)
	}
	if n.LastReportAt != nil {
		v.LastReportAt = n.LastReportAt.Format(tsLayout)
	}
	if n.HardwareChangedAt != nil {
		v.HardwareChanged = n.HardwareChangedAt.Format(tsLayout)
	}
	return v
}

// list 返回节点列表。
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	page, size := httpapi.PageQuery(r)

	f := store.NodeListFilter{Keyword: r.URL.Query().Get("q")}
	if s := r.URL.Query().Get("status"); s != "" {
		f.Status = model.NodeStatus(s)
	}
	if g := r.URL.Query().Get("group_id"); g != "" {
		if gid, err := strconv.ParseInt(g, 10, 64); err == nil {
			f.GroupID = &gid
		}
	}

	nodes, total, err := h.db.Nodes.List(r.Context(), f)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	items := make([]nodeView, 0, len(nodes))
	for i := range nodes {
		items = append(items, viewOf(&nodes[i]))
	}
	httpapi.OK(w, r, httpapi.PageData{
		Items: items, Total: total, Page: page, Size: size,
	})
}

// create 创建节点。
//
// 明文密钥只在这次响应里出现一次，之后永远取不回来（PRD 9.4）。
// 前端必须明确提示用户立即保存。
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		GroupID  *int64 `json:"group_id"`
		Remark   string `json:"remark"`
		IsPublic bool   `json:"is_public"`
	}
	if err := httpapi.DecodeJSON(r, &in); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		httpapi.Fail(w, r, apperr.BadRequest("请填写节点名称"))
		return
	}
	if n := len([]rune(in.Name)); n > 64 {
		httpapi.Fail(w, r, apperr.BadRequest(fmt.Sprintf("节点名称过长（%d 字符，上限 64）", n)))
		return
	}
	if n := len([]rune(in.Remark)); n > 256 {
		httpapi.Fail(w, r, apperr.BadRequest(fmt.Sprintf("备注过长（%d 字符，上限 256）", n)))
		return
	}

	uid, err := auth.NewUUID()
	if err != nil {
		httpapi.Fail(w, r, apperr.Internal(err, "生成节点标识失败"))
		return
	}
	secret, err := auth.NewAgentSecret()
	if err != nil {
		httpapi.Fail(w, r, apperr.Internal(err, "生成节点密钥失败"))
		return
	}

	id, err := h.db.Nodes.Create(r.Context(), store.CreateNodeInput{
		UID:        uid,
		Name:       in.Name,
		GroupID:    in.GroupID,
		SecretHash: store.HashToken(secret),
		Remark:     in.Remark,
		IsPublic:   in.IsPublic,
	})
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}

	h.audit(r, "create_node", "node", uid, map[string]any{
		"name": in.Name, "is_public": in.IsPublic,
	})

	httpapi.Created(w, r, map[string]any{
		"id":              id,
		"uid":             uid,
		"secret":          secret,
		"install_command": h.installCommand(uid, secret),
	})
}

// installCommand 生成一键安装命令。
/*
 * installCommand 生成一键安装命令。
 *
 * 协议必须跟着配置走，不能写死 https。
 *
 * 之前硬编码 https 的后果很实际：服务端只监听明文 HTTP 时，
 * 用户执行安装命令得到
 *   curl: (35) TLS connect error: wrong version number
 * ——curl 在明文端口上做 TLS 握手，服务端回的是 HTTP，
 * curl 解析出"版本号不对"就报这个错。看起来像网络问题，
 * 实际是命令里协议写错了。
 *
 * 另外 --server 给的是 gRPC 端口（安装脚本用它连Agent），
 * 而 install.sh 从 HTTP 端口拿——两者端口不同，别混用。
 */
func (h *Handler) installCommand(uid, secret string) string {
	scheme, grpcHost := h.endpoints()
	return fmt.Sprintf(
		"curl -fsSL %s://%s/install.sh | sh -s -- --server %s --uuid %s --secret %s",
		scheme, h.httpHost(), grpcHost, uid, secret)
}

// endpoints 拆出 (HTTP 协议, gRPC 地址)。
//
// grpcHost 优先用gRPC 专用地址，未配置时回退到 HTTP 地址——
// 单端口部署下两者相同。
func (h *Handler) endpoints() (scheme, grpcHost string) {
	scheme = "http"
	if strings.HasPrefix(h.publicURL, "https://") {
		scheme = "https"
	}
	grpcHost = h.grpcAddr
	if grpcHost == "" {
		grpcHost = h.httpHost()
	}
	return scheme, grpcHost
}

// httpHost 是 HTTP 监听地址（含端口）。
func (h *Handler) httpHost() string {
	if h.serverHost != "" {
		return h.serverHost
	}
	return "PROBEONE_HOST:8000"
}

// get 返回单个节点。
func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	n, err := h.db.Nodes.GetByUID(r.Context(), r.PathValue("uid"))
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	httpapi.OK(w, r, viewOf(n))
}

// update 更新节点。
//
// 只接受白名单字段：客户端不能改 status、secret_hash 等
// 服务端自己维护的列——否则改个状态就能把离线节点"变在线"。
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	n, err := h.db.Nodes.GetByUID(r.Context(), r.PathValue("uid"))
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	var in struct {
		Name       string   `json:"name"`
		GroupID    *int64   `json:"group_id"`
		Remark     string   `json:"remark"`
		IsPublic   *bool    `json:"is_public"`
		GeoCountry string   `json:"geo_country"`
		GeoCity    string   `json:"geo_city"`
		GeoLat     *float64 `json:"geo_lat"`
		GeoLon     *float64 `json:"geo_lon"`
	}
	if err := httpapi.DecodeJSON(r, &in); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	if n := len([]rune(in.Remark)); n > 256 {
		httpapi.Fail(w, r, apperr.BadRequest(fmt.Sprintf("备注过长（%d 字符，上限 256）", n)))
		return
	}

	if in.Name != "" {
		n.Name = strings.TrimSpace(in.Name)
	}
	n.GroupID = in.GroupID
	n.Remark = in.Remark
	if in.IsPublic != nil {
		n.IsPublic = *in.IsPublic
	}
	// 地理位置允许手工修正：IP 解析可能失败或不准确
	if in.GeoCountry != "" {
		n.GeoCountry = strings.ToUpper(strings.TrimSpace(in.GeoCountry))
	}
	n.GeoCity = in.GeoCity
	if in.GeoLat != nil {
		if *in.GeoLat < -90 || *in.GeoLat > 90 {
			httpapi.Fail(w, r, apperr.BadRequest("纬度超出范围（-90 ~ 90）"))
			return
		}
		n.GeoLat = in.GeoLat
	}
	if in.GeoLon != nil {
		if *in.GeoLon < -180 || *in.GeoLon > 180 {
			httpapi.Fail(w, r, apperr.BadRequest("经度超出范围（-180 ~ 180）"))
			return
		}
		n.GeoLon = in.GeoLon
	}

	if err := h.db.Nodes.Update(r.Context(), n); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	h.audit(r, "update_node", "node", n.UID, nil)
	httpapi.OK(w, r, viewOf(n))
}

// delete 删除节点。
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	n, err := h.db.Nodes.GetByUID(r.Context(), r.PathValue("uid"))
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	if err := h.db.Nodes.Delete(r.Context(), n.ID); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	h.audit(r, "delete_node", "node", n.UID, map[string]any{"name": n.Name})
	httpapi.NoContent(w, r)
}

// rotateSecret 重置密钥。
//
// 旧密钥立即失效：已连着的 Agent 会开始失败，这是预期行为——
// 轮换密钥的目的就是让可能已泄露的旧密钥作废。
func (h *Handler) rotateSecret(w http.ResponseWriter, r *http.Request) {
	n, err := h.db.Nodes.GetByUID(r.Context(), r.PathValue("uid"))
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	secret, err := auth.NewAgentSecret()
	if err != nil {
		httpapi.Fail(w, r, apperr.Internal(err, "生成密钥失败"))
		return
	}
	if err := h.db.Nodes.RotateSecret(r.Context(), n.ID, store.HashToken(secret)); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	h.audit(r, "rotate_secret", "node", n.UID, nil)

	httpapi.OK(w, r, map[string]any{
		"uid":             n.UID,
		"secret":          secret,
		"install_command": h.installCommand(n.UID, secret),
	})
}

// metricPoint 是图表用的时序点。
type metricPoint struct {
	T       int64   `json:"t"`
	CPU     float64 `json:"cpu"`
	Mem     float64 `json:"mem"`
	NetRx   float64 `json:"net_rx"`
	NetTx   float64 `json:"net_tx"`
	DiskMax float64 `json:"disk_max"`
}

// metrics 返回节点的时序指标。
//
// 优先读预聚合表：24 小时以上的数据原始表已被清理，
// 直接查原始会返回空数组，前端会误以为"节点没在工作"。
func (h *Handler) metrics(w http.ResponseWriter, r *http.Request) {
	n, err := h.db.Nodes.GetByUID(r.Context(), r.PathValue("uid"))
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	from, to := httpapi.TimeRange(r)
	span := to.Sub(from)

	// 窗口越长越应该读预聚合
	bucket := "1m"
	rollup := false
	switch {
	case span > 7*24*3600e9:
		bucket, rollup = "1d", true
	case span > 24*3600e9:
		bucket, rollup = "1h", true
	}

	points := make([]metricPoint, 0, 256)
	if rollup {
		rows, err := h.db.Metrics.Rollup(r.Context(), n.ID, bucket, from, to)
		if err != nil {
			httpapi.Fail(w, r, err)
			return
		}
		for _, p := range rows {
			points = append(points, metricPoint{
				T: p.BucketAt.Unix(), CPU: p.CPUAvg, Mem: p.MemAvg,
				NetRx: float64(p.NetRxAvg), NetTx: float64(p.NetTxAvg),
				DiskMax: p.DiskUsageMax,
			})
		}
	} else {
		rows, err := h.db.Metrics.Range(r.Context(), n.ID, from, to, 2000)
		if err != nil {
			httpapi.Fail(w, r, err)
			return
		}
		for _, m := range rows {
			pt := metricPoint{
				T: m.CollectedAt.Unix(), CPU: m.CPUUsage, Mem: m.MemUsage,
			}
			for _, nio := range m.NetIO {
				pt.NetRx += float64(nio.RxBps)
				pt.NetTx += float64(nio.TxBps)
			}
			for _, d := range m.Disks {
				if d.Usage > pt.DiskMax {
					pt.DiskMax = d.Usage
				}
			}
			points = append(points, pt)
		}
	}

	httpapi.OK(w, r, map[string]any{
		"items": points, "bucket": bucket, "from": from.Unix(), "to": to.Unix(),
	})
}

// ---------- 分组 ----------

func (h *Handler) listGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.db.Nodes.ListGroups(r.Context())
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	httpapi.OK(w, r, groups)
}

func (h *Handler) createGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
		Sort int    `json:"sort"`
	}
	if err := httpapi.DecodeJSON(r, &in); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		httpapi.Fail(w, r, apperr.BadRequest("请填写分组名称"))
		return
	}
	id, err := h.db.Nodes.CreateGroup(r.Context(), in.Name, in.Sort)
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	h.audit(r, "create_group", "group", strconv.FormatInt(id, 10), map[string]any{"name": in.Name})
	httpapi.Created(w, r, map[string]any{"id": id, "name": in.Name, "sort": in.Sort})
}

// updateGroup 改名与调整排序。
//
// 此前后端只有"列出"与"新建"，改名得删了重建——
// 界面没法做行内编辑，也无法保留原sort。
func (h *Handler) updateGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	var in struct {
		Name string `json:"name"`
		Sort int    `json:"sort"`
	}
	if err := httpapi.DecodeJSON(r, &in); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		httpapi.Fail(w, r, apperr.BadRequest("请填写分组名称"))
		return
	}
	if err := h.db.Nodes.UpdateGroup(r.Context(), id, in.Name, in.Sort); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	h.audit(r, "update_group", "group", strconv.FormatInt(id, 10),
		map[string]any{"name": in.Name})
	httpapi.OK(w, r, map[string]any{"id": id, "name": in.Name, "sort": in.Sort})
}

// deleteGroup 删除分组。
//
// 分组删除后**节点本身不删**——节点的 group_id 置空后继续存在。
// "删分组等于删节点"是不可逆的，且误操作代价太大。
func (h *Handler) deleteGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	if err := h.db.Nodes.DeleteGroup(r.Context(), id); err != nil {
		httpapi.Fail(w, r, err)
		return
	}
	h.audit(r, "delete_group", "group", strconv.FormatInt(id, 10), nil)
	httpapi.NoContent(w, r)
}

// pathID 解析路径参数中的 int64。
func pathID(r *http.Request, key string) (int64, error) {
	n, err := strconv.ParseInt(r.PathValue(key), 10, 64)
	if err != nil {
		return 0, apperr.BadRequest("路径参数 " + key + " 不是合法数字")
	}
	return n, nil
}

// audit 写审计日志。
//
// 审计失败只记日志不影响主流程：审计是辅助记录，
// 不该因为它写不进去就拒绝用户的操作。
func (h *Handler) audit(r *http.Request, action, targetType, targetID string, detail map[string]any) {
	if h.db.Audit == nil {
		return
	}
	_ = h.db.Audit.Write(r.Context(), &model.AuditLog{
		Username:   httpapi.Username(r.Context()),
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		IP:         httpapi.ClientIP(r, h.trustProxy),
		Detail:     detail,
	})
}
