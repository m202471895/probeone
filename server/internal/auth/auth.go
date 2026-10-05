// Package auth 提供密码哈希与凭据生成。
//
// 算法选择：argon2id。
// 理由（PRD 9.4）：
//   - 内存困难（memory-hard），GPU 并行破解收益低
//   - OWASP 推荐的现代选择，bcrypt 已在被逐步淘汰
//   - Go 标准库 x/crypto 提供，无需外部依赖
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id 参数。
//
// 调参依据（OWASP 推荐最低配置）：
//   - memory 64MB：抵抗 GPU 并行
//   - iterations 3：平衡耗时与成本
//   - parallelism 4：利用多核
//   - keyLen 32 / saltLen 16：标准长度
//
// 代价：每次哈希约 50-100ms。这是刻意的——登录是低频操作，
// 但密码库泄露后的离线破解是高频的。
const (
	argonMemory      uint32 = 64 * 1024 // 64MB
	argonIterations  uint32 = 3
	argonParallelism uint8  = 4
	argonKeyLen      uint32 = 32
	argonSaltLen            = 16
)

// HashPassword 生成 PHC 格式的 argon2id 哈希。
// 返回格式：$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
func HashPassword(password string) (string, error) {
	if len(password) < 10 {
		return "", fmt.Errorf("密码长度不足 10 位")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成盐值失败: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonIterations, argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword 校验密码。
// 使用常量时间比较，避免时序侧信道。
func VerifyPassword(password, encoded string) bool {
	salt, key, _, mem, iter, par, err := decodeHash(encoded)
	if err != nil {
		return false
	}
	candidate := argon2.IDKey([]byte(password), salt, iter, mem, par, uint32(len(key)))
	return subtle.ConstantTimeCompare(candidate, key) == 1
}

// NeedsRehash 判断哈希是否使用了弱参数，需要在用户下次登录时升级。
// 例如算法参数在版本升级中加强过，老用户的哈希应被重新计算。
func NeedsRehash(encoded string) bool {
	_, _, _, mem, iter, par, err := decodeHash(encoded)
	if err != nil {
		return true
	}
	return mem < argonMemory || iter < argonIterations || par < argonParallelism
}

// decodeHash 解析 PHC 格式哈希。
func decodeHash(encoded string) (salt, key []byte, version int, mem, iter uint32, par uint8, err error) {
	parts := strings.Split(encoded, "$")
	// 格式：["", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash]
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return nil, nil, 0, 0, 0, 0, fmt.Errorf("哈希格式非法")
	}

	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return nil, nil, 0, 0, 0, 0, fmt.Errorf("版本字段非法")
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &iter, &par); err != nil {
		return nil, nil, 0, 0, 0, 0, fmt.Errorf("参数字段非法")
	}
	if version != argon2.Version {
		return nil, nil, 0, 0, 0, 0, fmt.Errorf("不支持的版本 %d", version)
	}

	salt, err = base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return nil, nil, 0, 0, 0, 0, fmt.Errorf("盐值解码失败")
	}
	key, err = base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return nil, nil, 0, 0, 0, 0, fmt.Errorf("哈希解码失败")
	}
	return salt, key, version, mem, iter, par, nil
}

// ---------- 凭据生成 ----------

// NewUUID 生成节点 UUID。
func NewUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// RFC 4122 v4：设置版本与变体位
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// NewAgentSecret 生成 Agent 密钥。
// 32 字节 = 256 位熵，base64url 编码后 43 字符。
func NewAgentSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewSessionToken 生成会话 token（明文）。
// 调用方负责哈希后存库，明文只在登录响应中出现一次。
func NewSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
