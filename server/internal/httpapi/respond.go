package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
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
// 只在配置了可信代理时读 X-Forwarded-For ——
// 无条件信任会让任何人伪造来源 IP，审计日志就失去意义了。
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// XFF 是逗号分隔的链，取第一段（最接近客户端的那个）
			if i := strings.IndexByte(xff, ','); i > 0 {
				return strings.TrimSpace(xff[:i])
			}
			return strings.TrimSpace(xff)
		}
		if xr := r.Header.Get("X-Real-IP"); xr != "" {
			return strings.TrimSpace(xr)
		}
	}
	// RemoteAddr 带端口，去掉
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return host
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
