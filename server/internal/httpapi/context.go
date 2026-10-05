package httpapi

import "context"

type requestIDKey struct{}

// withRequestID 把 request_id 注入上下文。
func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// requestID 从上下文取 request_id。
func requestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// RequestIDFrom 是 requestID 的导出版本，供其他包使用。
func RequestIDFrom(ctx context.Context) string { return requestID(ctx) }
