package visibility

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/m202471895/probeone/server/internal/model"
)

type ctxKey struct{}

// 引擎注入请求上下文的键。
type engineKey struct{}

// WithEngine 把脱敏引擎注入上下文。
func WithEngine(ctx context.Context, e *Engine) context.Context {
	return context.WithValue(ctx, engineKey{}, e)
}

// EngineFrom 从上下文取引擎。
func EngineFrom(ctx context.Context) (*Engine, bool) {
	e, ok := ctx.Value(engineKey{}).(*Engine)
	return e, ok
}

// scopeKey 标记当前请求应当使用的脱敏范围。
type scopeKey struct{}

// WithScope 设置当前请求的脱敏范围。
func WithScope(ctx context.Context, s string) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// ScopeFrom 读取当前请求的脱敏范围。未设置时返回空串，表示不脱敏
//（即已登录且具备权限的 full 路径）。
func ScopeFrom(ctx context.Context) string {
	s, _ := ctx.Value(scopeKey{}).(string)
	return s
}

// Middleware 是脱敏中间件，处理两种职责：
//
//  1. 兜底扫描：序列化响应后扫描 JSON，命中硬禁止字段则置空并记 WARN。
//     必须在脱敏之后执行——否则已脱敏的 "203.0.x.x" 会被误判。
//  2. 标记 scope：未认证请求由上游设置 scope，已登录用户不设 scope（即不过滤）。
func Middleware(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			// 仅对成功的 JSON 响应做兜底扫描
			if rec.status >= 200 && rec.status < 300 && rec.body.Len() > 0 {
				if cleaned, changed := sanitizeJSON(rec.body.Bytes(), log); changed {
					rec.body.Reset()
					rec.body.Write(cleaned)
				}
			}
			// 复制到真实 writer
			for k, vs := range rec.header {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(rec.status)
			_, _ = w.Write(rec.body.Bytes())
		})
	}
}

// sanitizeJSON 扫描并清洗 JSON 中的硬禁止字段。
// 返回清洗后的内容与是否发生了修改。
func sanitizeJSON(body []byte, log *slog.Logger) ([]byte, bool) {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		// 不是合法 JSON（例如纯文本），不处理
		return body, false
	}
	changed := false
	var walk func(node any) any
	walk = func(node any) any {
		switch n := node.(type) {
		case map[string]any:
			for k, val := range n {
				if IsHardDenied(k) {
					if log != nil {
						log.Warn("响应中命中硬禁止字段，已置空",
							slog.String("field", k),
							slog.String("hint", "该字段不应出现在任何对外响应中，请检查 DTO 定义"))
					}
					delete(n, k)
					changed = true
					continue
				}
				n[k] = walk(val)
			}
			return n
		case []any:
			for i, val := range n {
				n[i] = walk(val)
			}
			return n
		default:
			return n
		}
	}
	v = walk(v)
	if !changed {
		return body, false
	}
	out, err := json.Marshal(v)
	if err != nil {
		return body, false
	}
	return out, true
}

// responseRecorder 捕获响应，用于兜底扫描后再输出。
type responseRecorder struct {
	http.ResponseWriter
	status int
	body   *bytes.Buffer
	header http.Header
}

func (r *responseRecorder) Header() http.Header {
	if r.header == nil {
		r.header = make(http.Header)
	}
	return r.header
}

func (r *responseRecorder) WriteHeader(code int) {
	r.status = code
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if r.body == nil {
		r.body = &bytes.Buffer{}
	}
	return r.body.Write(b)
}

// Flush 实现 http.Flusher，保证 SSE/WebSocket 场景不被中间件阻塞。
func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// 建议在 handler 中使用的辅助函数

// SanitizeMap 便捷方法：对 map 施加当前上下文的 scope。
// 若 scope 为空（已登录 full 路径），原样返回。
//
// 未知 scope 一律返回空 map（fail-closed），绝不返回原始数据。
func SanitizeMap(ctx context.Context, fields map[string]any) map[string]any {
	scope := ScopeFrom(ctx)
	if scope == "" {
		return fields
	}
	e, ok := EngineFrom(ctx)
	if !ok {
		return map[string]any{}
	}
	var s model.VisibilityScope
	switch strings.TrimSpace(scope) {
	case string(model.ScopePublicStatus):
		s = model.ScopePublicStatus
	case string(model.ScopeAPIUnauth):
		s = model.ScopeAPIUnauth
	case string(model.ScopeExport):
		s = model.ScopeExport
	default:
		// 未知 scope：不认识就不给数据
		return map[string]any{}
	}
	return e.Apply(s, fields).Fields
}

// ModelScope 把字符串转为 model.VisibilityScope，未知值返回空串。
func ModelScope(s string) model.VisibilityScope {
	switch strings.TrimSpace(s) {
	case string(model.ScopePublicStatus):
		return model.ScopePublicStatus
	case string(model.ScopeAPIUnauth):
		return model.ScopeAPIUnauth
	case string(model.ScopeExport):
		return model.ScopeExport
	default:
		return ""
	}
}
