package util

import (
	"strings"
	"testing"
)

func TestMask(t *testing.T) {
	cases := []struct {
		in   string
		want string
		why  string
	}{
		{"", "", "空串不处理"},
		{"abc", "***", "短于 8 字符全部打码"},
		{"12345678", "***", "刚好 8 位也全部打码，避免从掩码长度推断密钥长度"},
		{"supersecretkey", "su***ey", "长值保留首尾各 2 位"},
		{"agent-secret-123456", "ag***56", "保留首尾"},
	}
	for _, c := range cases {
		if got := Mask(c.in); got != c.want {
			t.Errorf("Mask(%q) = %q，期望 %q（%s）", c.in, got, c.want, c.why)
		}
	}
}

func TestMask_不随长度线性膨胀(t *testing.T) {
	// 关键性质：掩码长度不能随原文长度线性增长，否则可反推密钥长度。
	// 实现上分两档（≤8 全打码，>8 保留首尾），每档内长度恒定。
	short := Mask("12345678")
	if len(short) != 3 {
		t.Errorf("短值应全部打码为 3 字符，实际 = %q", short)
	}
	for _, n := range []int{9, 20, 100, 500, 5000} {
		got := Mask(strings.Repeat("a", n))
		if len(got) != 7 {
			t.Errorf("长度 %d 的输入，掩码长度 = %d（%q），应恒为 7", n, len(got), got)
		}
	}
}

func TestRandomToken(t *testing.T) {
	a, err := RandomToken(32)
	if err != nil {
		t.Fatalf("RandomToken 失败: %v", err)
	}
	if len(a) < 40 {
		t.Errorf("32 字节 token 的 base64 长度应 >= 43，实际 = %d", len(a))
	}
	// 两次生成必须不同
	b, _ := RandomToken(32)
	if a == b {
		t.Error("两次生成的 token 不应相同")
	}
	// base64url 不应含 + / =
	c, _ := RandomToken(32)
	if strings.ContainsAny(c, "+/=") {
		t.Errorf("base64url 编码不应含 + / =，实际 = %s", c)
	}
}

func TestRandomHex(t *testing.T) {
	h, err := RandomHex(16)
	if err != nil {
		t.Fatalf("RandomHex 失败: %v", err)
	}
	if len(h) != 32 {
		t.Errorf("16 字节应编码为 32 个十六进制字符，实际 = %d", len(h))
	}
	for _, ch := range h {
		if !strings.ContainsRune("0123456789abcdef", ch) {
			t.Errorf("含非十六进制字符 %q", ch)
		}
	}
}

func TestRandomPassword(t *testing.T) {
	pw, err := RandomPassword(16)
	if err != nil {
		t.Fatalf("RandomPassword 失败: %v", err)
	}
	if len(pw) != 16 {
		t.Errorf("密码长度 = %d，期望 16", len(pw))
	}

	var hasLower, hasUpper, hasDigit bool
	for _, c := range pw {
		switch {
		case c >= 'a' && c <= 'z':
			hasLower = true
		case c >= 'A' && c <= 'Z':
			hasUpper = true
		case c >= '0' && c <= '9':
			hasDigit = true
		}
	}
	if !hasLower || !hasUpper || !hasDigit {
		t.Errorf("密码必须含大小写与数字，实际 = %s (小写=%v 大写=%v 数字=%v)",
			pw, hasLower, hasUpper, hasDigit)
	}

	// 不应含易混淆字符
	for _, bad := range "lO01I" {
		if strings.ContainsRune(pw, bad) {
			t.Errorf("密码含易混淆字符 %q：%s", bad, pw)
		}
	}
}

func TestRandomPassword_长度下限(t *testing.T) {
	// 请求长度过小时应抬到 12 位（符合最小密码策略）
	pw, err := RandomPassword(4)
	if err != nil {
		t.Fatalf("RandomPassword 失败: %v", err)
	}
	if len(pw) != 12 {
		t.Errorf("过短请求应抬到 12 位，实际 = %d", len(pw))
	}
}

func TestRandomPassword_随机性(t *testing.T) {
	// 100 次生成不应出现重复，否则说明随机源有问题
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		pw, err := RandomPassword(16)
		if err != nil {
			t.Fatalf("第 %d 次生成失败: %v", i, err)
		}
		if seen[pw] {
			t.Fatalf("第 %d 次生成的密码与之前重复：%s", i, pw)
		}
		seen[pw] = true
	}
}

func TestSHA256Hex(t *testing.T) {
	// 已知答案验证
	got := SHA256Hex("abc")
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want {
		t.Errorf("SHA256Hex(\"abc\") = %s，期望 %s", got, want)
	}
	// 稳定性
	if SHA256Hex("x") != SHA256Hex("x") {
		t.Error("SHA256Hex 应为确定性函数")
	}
	// 长度
	if len(SHA256Hex("任意长度输入")) != 64 {
		t.Error("SHA256 十六进制编码应为 64 字符")
	}
}

func TestClientIP(t *testing.T) {
	cidr := mustCIDR(t, "172.17.0.0/16")

	cases := []struct {
		name       string
		remoteAddr string
		xff        string
		trusted    []*netIPNet
		want       string
		why        string
	}{
		{
			name:       "无反代时忽略 XFF",
			remoteAddr: "203.0.113.9:1234",
			xff:        "1.2.3.4",
			trusted:    nil,
			want:       "203.0.113.9",
			why:        "未配置信任链时不得采信任何 X-Forwarded-For",
		},
		{
			name:       "无反代且无 XFF",
			remoteAddr: "203.0.113.9:1234",
			xff:        "",
			trusted:    nil,
			want:       "203.0.113.9",
			why:        "直连场景取连接来源",
		},
		{
			name:       "可信反代且有 XFF",
			remoteAddr: "172.17.0.5:1234",
			xff:        "203.0.113.9",
			trusted:    cidr,
			want:       "203.0.113.9",
			why:        "来源在信任网段内，应采信 XFF",
		},
		{
			name:       "可信反代但 XFF 为空",
			remoteAddr: "172.17.0.5:1234",
			xff:        "",
			trusted:    cidr,
			want:       "172.17.0.5",
			why:        "XFF 缺失时回落到代理地址",
		},
		{
			name:       "不可信来源携带伪造 XFF",
			remoteAddr: "198.51.100.7:1234",
			xff:        "1.1.1.1",
			trusted:    cidr,
			want:       "198.51.100.7",
			why:        "来源不在信任网段内，XFF 是伪造的，必须忽略",
		},
		{
			name:       "XFF 多跳取最左",
			remoteAddr: "172.17.0.5:1234",
			xff:        "203.0.113.9, 10.0.0.1, 172.17.0.5",
			trusted:    cidr,
			want:       "203.0.113.9",
			why:        "多跳链中最左侧为原始客户端",
		},
		{
			name:       "XFF 格式非法",
			remoteAddr: "172.17.0.5:1234",
			xff:        "not-an-ip",
			trusted:    cidr,
			want:       "172.17.0.5",
			why:        "XFF 无法解析时回落到代理地址",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ClientIP(c.remoteAddr, c.xff, c.trusted)
			if got != c.want {
				t.Errorf("ClientIP() = %s，期望 %s（%s）", got, c.want, c.why)
			}
		})
	}
}
