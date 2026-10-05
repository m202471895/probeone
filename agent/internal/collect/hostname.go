package collect

import (
	"context"
	"net"
	"strings"
	"time"
)

// resolveLocalFQDN 通过反向 DNS 解析本机 FQDN。
//
// 超时 2 秒：DNS 查询偶尔会卡住，不能因此拖慢启动。
// 解析失败返回空串——这是正常情况，多数 VPS 不配 PTR。
func resolveLocalFQDN(hostname string) string {
	if hostname == "" {
		return ""
	}
	// 已经带点的就认为是 FQDN，直接用
	if strings.Contains(hostname, ".") {
		return hostname
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	addrs, err := net.DefaultResolver.LookupAddr(ctx, hostname)
	if err != nil || len(addrs) == 0 {
		return ""
	}
	// 去掉 DNS 末尾的点
	return strings.TrimSuffix(addrs[0], ".")
}
