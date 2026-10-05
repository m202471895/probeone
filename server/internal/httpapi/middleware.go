// Package httpapi 提供 HTTP 中间件与响应封装。
package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/m202471895/probeone/server/internal/apperr"
	"github.com/m202471895/probeone/server/internal/util"
)

// ---------- panic 恢复 ----------

// Recovery 捕获 panic，返回 500 且不泄露内部细节。
// 堆栈只进日志，不进响应体。
func Recovery(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					rid := requestID(r.Context())
					log.Error("请求处理发生 panic",
						slog.Any("panic", rec),
						slog.String("request_id", rid),
						slog.String("method", r.Method),
						slog.String("path", r.URL.Path),
						slog.String("remote_addr", r.RemoteAddr),
						slog.String("stack", string(debug.Stack())),
					)
					WriteError(w, r, apperr.Internal(nil, "服务器内部错误").WithCause(nil))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// ---------- 访问日志 ----------

// AccessLog 记录访问日志，携带贯穿全链路的 request_id。
func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			// 为每个请求分配 ID
			rid := r.Header.Get(util.RequestIDHeader)
			if rid == "" {
				var err error
				rid, err = util.RandomHex(8)
				if err != nil {
					rid = "unknown"
				}
			}
			ctx := withRequestID(r.Context(), rid)
			r = r.WithContext(ctx)
			w.Header().Set(util.RequestIDHeader, rid)

			next.ServeHTTP(rec, r)

			// 健康检查不记访问日志，避免刷屏
			if r.URL.Path == "/healthz" {
				return
			}
			log.Info("请求完成",
				slog.String("request_id", rid),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int64("bytes", rec.bytes),
				slog.Duration("duration", time.Since(start)),
				slog.String("ip", r.RemoteAddr),
				slog.String("ua", truncate(r.UserAgent(), 128)),
			)
		})
	}
}

// ---------- 安全头 ----------

// SecurityHeaders 设置安全响应头。
//
// CSP 中 script-src 'self' 禁止内联脚本，是防 XSS 的关键一环。
// style-src 允许 'unsafe-inline' 是因为 ECharts 与部分组件库动态注入样式。
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"font-src 'self' data:; "+
				"connect-src 'self' ws: wss:; "+
				"frame-ancestors 'none'; "+
				"base-uri 'self'; "+
				"form-action 'self'")
		// 生产环境应启用 HSTS。反代终止 TLS 时由 Nginx 下发，这里也加一份
		// 自定义头让反代能取到
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

// ---------- 响应封装 ----------

// 统一响应结构。成功 code=0，失败 code 为业务错误码。
type envelope struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// WriteJSON 写成功响应。
func WriteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if data == nil {
		_, _ = w.Write([]byte(`{"code":0}`))
		return
	}
	b, err := json.Marshal(envelope{Code: 0, Data: data})
	if err != nil {
		// 序列化失败属于内部问题，不能把原始错误暴露给客户端
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":50000,"message":"响应序列化失败"}`))
		return
	}
	_, _ = w.Write(b)
}

// WriteError 写错误响应。
//
// 客户端只看到稳定错误码与安全文案，绝不返回堆栈、SQL 错误或文件路径。
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	e, ok := apperr.As(err)
	if !ok {
		e = apperr.Internal(err, "服务器内部错误")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(e.HTTPStatus)
	b, mErr := json.Marshal(envelope{Code: e.HTTPStatus, Message: e.ClientMessage()})
	if mErr != nil {
		_, _ = w.Write([]byte(`{"code":50000,"message":"服务器内部错误"}`))
		return
	}
	_, _ = w.Write(b)
}

// ---------- 内部辅助 ----------

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ConstantTimeEqual 常量时间比较，避免时序侧信道。
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
