package auth

import (
	"strings"
	"testing"
)

func TestHashPassword_基本性质(t *testing.T) {
	h, err := HashPassword("Admin123456")
	if err != nil {
		t.Fatalf("哈希失败: %v", err)
	}
	// PHC 格式：$argon2id$v=19$m=65536,t=3,p=4$salt$hash
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("哈希格式不符: %s", h)
	}
	if h == "Admin123456" {
		t.Fatal("密码被明文存储")
	}
}

func TestHashPassword_盐值随机(t *testing.T) {
	a, _ := HashPassword("Admin123456")
	b, _ := HashPassword("Admin123456")
	if a == b {
		t.Error("相同密码两次哈希应不同（盐值必须随机）")
	}
	// 两者的哈希段应相同（同样输入+同样参数），盐段不同
	pa, pb := splitPHC(t, a), splitPHC(t, b)
	if pa.salt == pb.salt {
		t.Error("盐值重复")
	}
}

func TestHashPassword_长度不足拒绝(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Error("短于 10 位的密码应被拒绝")
	}
	if _, err := HashPassword(strings.Repeat("a", 9)); err == nil {
		t.Error("9 位密码应被拒绝")
	}
	if _, err := HashPassword(strings.Repeat("a", 10)); err != nil {
		t.Errorf("10 位密码应接受: %v", err)
	}
}

func TestVerifyPassword(t *testing.T) {
	h, _ := HashPassword("Admin123456")

	if !VerifyPassword("Admin123456", h) {
		t.Error("正确密码应验证通过")
	}
	if VerifyPassword("admin123456", h) {
		t.Error("密码应大小写敏感")
	}
	if VerifyPassword("Admin123457", h) {
		t.Error("错误密码不应通过")
	}
	if VerifyPassword("", h) {
		t.Error("空密码不应通过")
	}
}

func TestVerifyPassword_非法哈希不panic(t *testing.T) {
	bad := []string{
		"", "x", "$argon2id$", "$argon2id$v=19$m=1,t=1,p=1$onlyfour",
		"$argon2id$v=19$m=abc,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2i$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",  // 算法不是 argon2id
		"$argon2id$v=99$m=65536,t=3,p=4$c2FsdA$aGFzaA", // 版本不支持
		"$argon2id$v=19$m=65536,t=3,p=4$!!!$aGFzaA",    // 盐值 base64 非法
	}
	for _, h := range bad {
		if VerifyPassword("whatever", h) {
			t.Errorf("非法哈希 %q 不应验证通过", h)
		}
	}
}

func TestNeedsRehash(t *testing.T) {
	// 当前参数生成的哈希不需要 rehash
	h, _ := HashPassword("Admin123456")
	if NeedsRehash(h) {
		t.Error("用当前参数生成的哈希不应需要 rehash")
	}
	// 弱参数生成的旧哈希需要 rehash
	weak := "$argon2id$v=19$m=8192,t=1,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNo"
	if !NeedsRehash(weak) {
		t.Error("弱参数哈希应标记为需要 rehash")
	}
	// 无法解析的也应返回 true（保守：重新哈希）
	if !NeedsRehash("garbage") {
		t.Error("无法解析的哈希应返回 true")
	}
}

func TestNewUUID(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		u, err := NewUUID()
		if err != nil {
			t.Fatal(err)
		}
		if seen[u] {
			t.Fatalf("UUID 重复: %s", u)
		}
		seen[u] = true

		// RFC 4122 v4：第 13 位是 4，第 17 位是 8/9/a/b
		if len(u) != 36 {
			t.Fatalf("UUID 长度 = %d，期望 36", len(u))
		}
		if u[14] != '4' {
			t.Errorf("版本位应为 4，实际 = %c", u[14])
		}
		c := u[19]
		if c != '8' && c != '9' && c != 'a' && c != 'b' {
			t.Errorf("变体位应为 8/9/a/b，实际 = %c", c)
		}
	}
}

func TestNewAgentSecret(t *testing.T) {
	seen := make(map[string]bool, 500)
	for i := 0; i < 500; i++ {
		s, err := NewAgentSecret()
		if err != nil {
			t.Fatal(err)
		}
		if seen[s] {
			t.Fatal("Agent 密钥重复")
		}
		seen[s] = true
		// 32 字节 base64url = 43 字符
		if len(s) != 43 {
			t.Fatalf("密钥长度 = %d，期望 43", len(s))
		}
		// base64url 字符集：不应出现 + / =
		if strings.ContainsAny(s, "+/=") {
			t.Errorf("密钥含非 base64url 字符: %s", s)
		}
	}
}

func TestNewSessionToken(t *testing.T) {
	a, _ := NewSessionToken()
	b, _ := NewSessionToken()
	if a == b {
		t.Error("两次生成的会话 token 不应相同")
	}
	if len(a) != 43 {
		t.Errorf("会话 token 长度 = %d，期望 43", len(a))
	}
}

// phc 是 PHC 格式哈希的解析结果。
type phc struct {
	salt string
	hash string
}

func splitPHC(t *testing.T, encoded string) phc {
	t.Helper()
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 {
		t.Fatalf("PHC 格式解析失败: %s", encoded)
	}
	return phc{salt: parts[4], hash: parts[5]}
}
