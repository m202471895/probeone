package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/m202471895/probeone/server/internal/apperr"
)

// Response 是统一的响应信封。
//
// 前端 client.ts 依赖这个形状解包：{ code, message, data }。
// code 为 0 表示成功，非 0 是业务错误码。
type Response struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
	// RequestID 便于用户报障时对照服务端日志
	RequestID string `json:"request_id,omitempty"`
}

// PageData 是分页列表的统一载荷。
type PageData struct {
	Items any `json:"items"`
	Total int `json:"total"`
	Page  int `json:"page"`
	Size  int `json:"size"`
}

// writeJSON 输出 JSON 响应。
func writeJSON(w http.ResponseWriter, r *http.Request, status int, body Response) {
	body.RequestID = RequestIDFrom(r.Context())
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// 认证类响应禁止被缓存
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// 走到这里说明响应头已发出，只能记日志
		slog.Error("写响应失败",
			slog.String("request_id", body.RequestID),
			slog.String("error", err.Error()))
	}
}

// ok 返回成功响应。
func ok(w http.ResponseWriter, r *http.Request, data any) {
	writeJSON(w, r, http.StatusOK, Response{Code: "0", Data: data})
}

// created 返回 201。
func created(w http.ResponseWriter, r *http.Request, data any) {
	writeJSON(w, r, http.StatusCreated, Response{Code: "0", Data: data})
}

// noContent 返回 204。
func noContent(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// fail 把任意错误转成规范响应。
//
// 关键约束（PRD 9 章）：内部错误只写日志，
// 客户端只能看到 ClientMessage()，绝不泄漏堆栈/SQL/文件路径。
func fail(w http.ResponseWriter, r *http.Request, err error) {
	appErr, ok := apperr.As(err)
	if !ok {
		// 传进来的不是 apperr —— 视为内部错误
		slog.Error("未分类的错误",
			slog.String("request_id", RequestIDFrom(r.Context())),
			slog.String("path", r.URL.Path),
			slog.String("error", err.Error()))
		writeJSON(w, r, http.StatusInternalServerError, Response{
			Code:    "INTERNAL",
			Message: "服务器内部错误",
		})
		return
	}

	// 5xx 记完整错误（含 cause），4xx 只记摘要
	if appErr.HTTPStatus >= 500 {
		slog.Error("请求处理失败",
			slog.String("request_id", RequestIDFrom(r.Context())),
			slog.String("path", r.URL.Path),
			slog.String("code", appErr.Code),
			slog.String("error", err.Error()))
	} else {
		slog.Debug("请求被拒绝",
			slog.String("request_id", RequestIDFrom(r.Context())),
			slog.String("code", appErr.Code),
			slog.String("path", r.URL.Path))
	}

	writeJSON(w, r, appErr.HTTPStatus, Response{
		Code:    appErr.Code,
		Message: appErr.ClientMessage(),
	})
}

// badRequest 构造 400。
func badRequest(msg string) *apperr.Error {
	return apperr.BadRequest(msg)
}

// decodeJSON 解析请求体。
//
// 限制 1MB：接口不该接收大 body，超了直接拒绝而不是读进内存。
func decodeJSON(r *http.Request, dst any) error {
	const maxBody = 1 << 20
	r.Body = http.MaxBytesReader(nil, r.Body, maxBody)

	dec := json.NewDecoder(r.Body)
	// 拒绝未知字段：客户端拼错字段名时立刻报错，
	// 而不是静默忽略导致"设置没生效却看不出原因"
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return badRequest("请求体过大（上限 1MB）")
		}
		if strings.Contains(err.Error(), "unknown field") {
			return badRequest("请求包含未知字段")
		}
		return badRequest("请求体不是合法 JSON")
	}
	// 拒绝尾随内容：`{}{}` 这种应视为非法
	if dec.More() {
		return badRequest("请求体只能包含一个 JSON 对象")
	}
	return nil
}

// ---------- 查询参数 ----------

// queryInt 读整型查询参数。
func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// pageQuery 解析分页参数，并夹住上界。
//
// 上界必须夹：不夹的话 ?size=999999 会让数据库一次拉全表。
func pageQuery(r *http.Request) (page, size int) {
	page = queryInt(r, "page", 1)
	size = queryInt(r, "size", 50)
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 50
	}
	if size > 200 {
		size = 200
	}
	return page, size
}

// queryBool 读布尔查询参数。
func queryBool(r *http.Request, key string, def bool) bool {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	default:
		return def
	}
}

// pathInt64 读路径参数中的 int64。
func pathInt64(r *http.Request, key string) (int64, error) {
	v := r.PathValue(key)
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, badRequest("路径参数 " + key + " 不是合法数字")
	}
	return n, nil
}

// clientIP 取真实客户端 IP。
//
// 两条安全约束（都来自实际可被利用的绕过路径）：
//
//  1. 只在配置了可信代理时读 X-Forwarded-For。
//     无条件信任等于让任何人伪造来源 IP，审计日志随之失去意义。
//
//  2. XFF 逐段校验，畸形输入一律回退到 RemoteAddr。
//     攻击者能自己构造这个头。若把 ",1.2.3.4" 整串当 IP 存进
//     失败计数表，每次伪造不同前缀就落到不同"IP"上，
//     登录锁定阈值永远达不到。
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// XFF 是"客户端, 代理1, 代理2..."，最左段最接近真实客户端。
			// 逐段找第一个合法 IP：畸形段跳过而不是整体丢弃，
			// 因为有些反代会在最前面塞未知占位。
			for _, part := range strings.Split(xff, ",") {
				if ip := parseIP(strings.TrimSpace(part)); ip != "" {
					return ip
				}
			}
		}
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
			if ip := parseIP(xr); ip != "" {
				return ip
			}
		}
	}

	// RemoteAddr 形如 "1.2.3.4:5678" 或 "[::1]:5678"。
	// 必须用 net.SplitHostPort：它知道 IPv6 的方括号，
	// 手写 LastIndexByte(':') 会把 "[::1]" 当成host 剥不干净。
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if ip := parseIP(host); ip != "" {
			return ip
		}
	}
	// SplitHostPort 失败（可能没有端口），退而验证整串
	if ip := parseIP(r.RemoteAddr); ip != "" {
		return ip
	}
	return ""
}

// parseIP 校验并规范化 IP 字符串。
// 非法输入返回空串——调用方据此回退，不把脏数据写进审计与计数表。
func parseIP(s string) string {
	s = strings.TrimSpace(s)
	// 明确剥离 IPv6 的方括号，避免 "[::1]" 被当作合法 IP
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	// 剥掉 zone id（"fe80::1%eth0" 里的 "%eth0"）。
	// net.ParseIP 不接受 zone 后缀，但审计表与锁定计数不需要它——
	// 保留反而会让同一地址的带区/不带区写法落到不同 key。
	if i := strings.IndexByte(s, '%'); i > 0 {
		s = s[:i]
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return ""
	}
	return ip.String()
}

// ========== 供子包处理器使用的导出包装 ==========
// 子包（node / monitor / alert 等）需要这些能力，
// 但它们是实现细节，不该在 httpapi 包里到处导出符号。

// OK 输出 200。
func OK(w http.ResponseWriter, r *http.Request, data any) { ok(w, r, data) }

// Created 输出 201。
func Created(w http.ResponseWriter, r *http.Request, data any) { created(w, r, data) }

// NoContent 输出 204。
func NoContent(w http.ResponseWriter, r *http.Request) { noContent(w, r) }

// Fail 输出错误响应。
func Fail(w http.ResponseWriter, r *http.Request, err error) { fail(w, r, err) }

// BadRequest 构造 400 错误。
func BadRequest(msg string) *apperr.Error { return badRequest(msg) }

// DecodeJSON 解析请求体。
func DecodeJSON(r *http.Request, dst any) error { return decodeJSON(r, dst) }

// PageQuery 解析分页参数。
func PageQuery(r *http.Request) (int, int) { return pageQuery(r) }

// QueryInt 读整型查询参数。
func QueryInt(r *http.Request, key string, def int) int { return queryInt(r, key, def) }

// ClientIP 取客户端 IP。
func ClientIP(r *http.Request, trustProxy bool) string { return clientIP(r, trustProxy) }

// TimeRange 解析 from/to 查询参数，缺省为最近 24 小时。
func TimeRange(r *http.Request) (from, to time.Time) {
	if v := r.URL.Query().Get("from"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			from = time.Unix(n, 0).UTC()
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			to = time.Unix(n, 0).UTC()
		}
	}
	now := time.Now().UTC()
	if to.IsZero() {
		to = now
	}
	if from.IsZero() {
		from = to.Add(-24 * time.Hour)
	}
	if !from.Before(to) {
		// 区间非法（from >= to）时退回默认窗口，
		// 而不是返回空结果让人以为"这段时间没数据"
		from = to.Add(-24 * time.Hour)
	}
	return from, to
}
