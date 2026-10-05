// Package logging 提供统一的结构化日志。
//
// 全部日志走 log/slog，JSON 格式，字段名固定（ts/level/msg）。
// 严禁打印密码、令牌、密钥等敏感信息；调用方需用util.Mask 掩码后再传。
package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// New 按配置创建 logger。
// format 为 json 时输出结构化日志（生产），text 时输出人类可读格式（开发）。
func New(level, format string) *slog.Logger {
	return NewWithWriter(os.Stdout, level, format)
}

func NewWithWriter(w io.Writer, level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: parseLevel(level),
	}

	var h slog.Handler
	if strings.EqualFold(format, "text") {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
