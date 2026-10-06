package store

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/m202471895/probeone/server/internal/auth"
)

// HashPassword 是 auth.HashPassword 的薄封装。
// 放在 store 包内是为了让仓储实现不必在每个文件里重复引用 auth 包，
// 同时保持密码学逻辑集中在 auth 一处（PRD：凭据相关只有一个来源）。

// defaultTokenPepper 是令牌哈希的 pepper。
//
// 默认值只为让开发环境能跑；生产环境必须通过
// PROBEONE_TOKEN_PEPPER 覆盖（见 config 校验）。
const defaultTokenPepper = "probeone-dev-pepper"

func HashPassword(password string) (string, error) { return auth.HashPassword(password) }

// VerifyPassword 见 auth.VerifyPassword。
func VerifyPassword(password, encoded string) bool { return auth.VerifyPassword(password, encoded) }

// HashToken 计算访问令牌的存储哈希。
//
// 为什么令牌也要哈希：密码哈希是为了防拖库后离线破解（慢哈希），
// 而令牌是 256 位随机值，本身不可枚举——但一旦明文入库，
// 拖库者拿到的令牌**立即可用**，没有二次破解成本。
// 哈希存储让拖库也拿不到可用凭据。
//
// 用 SHA-256 而非 argon2id：argon2id 的价值在于让穷举变慢，
// 而 256 位随机令牌不存在穷举可能。SHA-256 快且够用。
//
// 代价是：无法从哈希反推令牌，因此也无法在会话表中
// 按令牌查"这个令牌是否曾存在"——那本来也不是需求。
func HashToken(token string) string {
	return HashTokenWithSecret(token, defaultTokenPepper)
}

// HashTokenWithSecret 用指定 pepper 计算令牌哈希。
// pepper 从环境变量注入：即使数据库整体被脱敏，
// 没有 pepper 也无法离线比对出令牌。
func HashTokenWithSecret(token, pepper string) string {
	h := sha256.New()
	// 先写 pepper 长度，避免 "ab"+"c" 与 "a"+"bc" 拼出相同输入
	_, _ = h.Write([]byte(pepper))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(token))
	return hex.EncodeToString(h.Sum(nil))
}
