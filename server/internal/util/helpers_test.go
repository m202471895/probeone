package util

import (
	"net"
	"testing"
)

// netIPNet 是 net.IPNet 的别名，让测试表的类型声明更易读。
type netIPNet = net.IPNet

// mustCIDR 解析 CIDR，失败时直接终止测试。
func mustCIDR(t *testing.T, s string) []*net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("解析 CIDR %q 失败: %v", s, err)
	}
	return []*net.IPNet{n}
}
