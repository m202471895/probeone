package store

import "github.com/m202471895/probeone/server/internal/auth"

// HashPassword 是auth.HashPassword 的薄封装。
// 放在store 包内是为了让仓储实现不必在每个文件里重复引用 auth 包，
// 同时保持密码学逻辑集中在 auth 一处（PRD：凭据相关只有一个来源）。
func HashPassword(password string) (string, error) { return auth.HashPassword(password) }

// VerifyPassword 见 auth.VerifyPassword。
func VerifyPassword(password, encoded string) bool { return auth.VerifyPassword(password, encoded) }
