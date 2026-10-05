// Package apperr 提供类型化错误体系。
//
// 原则（PRD 9 章）：
//   - 服务端内部错误一律包装为 Internal，客户端只看到通用文案
//   - 业务错误有稳定错误码，前端可据此做可读映射，不解析字符串
//   - 绝不把堆栈、SQL 错误、文件路径返回给客户端
package apperr

import (
	"errors"
	"fmt"
	"net/http"
)

// Error 是 ProbeOne 的统一错误类型。
type Error struct {
	// Code 是稳定的业务错误码，形如 NODE_NOT_FOUND。
	// 前端据此映射文案，不要依赖 Message 内容。
	Code string
	// Message 是对用户安全的描述。
	Message string
	// HTTPStatus 决定返回的 HTTP 状态码。
	HTTPStatus int
	// cause 是内部原因，只写日志，不返回客户端。
	cause error
	// internal 为 true 时客户端只能看到通用文案（Message 会被替换）。
	internal bool
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.cause }

// WithCause 附加内部原因。返回新对象，不修改原值。
func (e *Error) WithCause(err error) *Error {
	n := *e
	n.cause = err
	return &n
}

// ClientMessage 返回可安全返回给客户端的文案。
func (e *Error) ClientMessage() string {
	if e.internal {
		return "服务器内部错误"
	}
	return e.Message
}

// ---------- 构造函数 ----------

// New 构造业务错误。
func New(code, message string, httpStatus int) *Error {
	return &Error{Code: code, Message: message, HTTPStatus: httpStatus}
}

// Internal 构造内部错误，客户端只见通用文案。
func Internal(cause error, message string) *Error {
	return &Error{
		Code:       "INTERNAL_ERROR",
		Message:    message,
		HTTPStatus: http.StatusInternalServerError,
		cause:      cause,
		internal:   true,
	}
}

// BadRequest 参数校验失败。
func BadRequest(message string) *Error {
	return &Error{Code: "BAD_REQUEST", Message: message, HTTPStatus: http.StatusBadRequest}
}

// Unauthenticated 未登录或凭据无效。
func Unauthenticated(message string) *Error {
	return &Error{Code: "UNAUTHENTICATED", Message: message, HTTPStatus: http.StatusUnauthorized}
}

// Forbidden 权限不足。
func Forbidden(message string) *Error {
	return &Error{Code: "FORBIDDEN", Message: message, HTTPStatus: http.StatusForbidden}
}

// NotFound 资源不存在。
func NotFound(code, message string) *Error {
	return &Error{Code: code, Message: message, HTTPStatus: http.StatusNotFound}
}

// Conflict 资源冲突，如名称重复。
func Conflict(code, message string) *Error {
	return &Error{Code: code, Message: message, HTTPStatus: http.StatusConflict}
}

// TooManyRequests 触发限流。
func TooManyRequests(message string) *Error {
	return &Error{Code: "RATE_LIMITED", Message: message, HTTPStatus: http.StatusTooManyRequests}
}

// ---------- 常用错误码 ----------

const (
	CodeNodeNotFound    = "NODE_NOT_FOUND"
	CodeNodeExists      = "NODE_EXISTS"
	CodeMonitorNotFound = "MONITOR_NOT_FOUND"
	CodeChannelNotFound = "CHANNEL_NOT_FOUND"
	CodeRuleNotFound    = "ALERT_RULE_NOT_FOUND"
	CodeUserNotFound    = "USER_NOT_FOUND"
	CodeUserExists      = "USER_EXISTS"
	CodeInvalidCreds    = "INVALID_CREDENTIALS"
	CodeSessionExpired  = "SESSION_EXPIRED"
	CodeValidation      = "VALIDATION_FAILED"
	CodeSSRFBlocked     = "TARGET_BLOCKED"
	CodeHardLocked      = "AGENT_HARD_LOCKED"
)

// ---------- 判定辅助 ----------

// As 提取 *Error。
func As(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// IsCode 判断错误码是否匹配。
func IsCode(err error, code string) bool {
	e, ok := As(err)
	return ok && e.Code == code
}

// Is 判断是否为目标错误码。
func Is(err error, code string) bool { return IsCode(err, code) }
