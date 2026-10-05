// Command probeone-agent 是 ProbeOne Agent 入口。
//
// 安全边界（PRD 1.1 与 9.2）——以下是本程序**不做**的事，由 CI 断言强制：
//   - 不执行任何外部命令（agent/ 目录零引用 os/exec）
//   - 不监听任何端口（agent/ 目录零引用 net.Listen）
//   - 不 fork 子进程
//   - 不读取监控无关的文件（无 ~/.ssh、无浏览器数据、无其他进程环境变量）
//   - 不写入除自身配置目录与日志之外的文件
//
// Agent 全部能力就是：只读采集本机指标 → gRPC 外发 → 收 ACK。
// 服务端无法通过本 Agent 在被监控主机上执行任何操作。
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	var (
		configPath  = flag.String("config", "/etc/probeone-agent/config.yaml", "配置文件路径")
		showVersion = flag.Bool("version", false, "打印版本后退出")
		checkOnly   = flag.Bool("check", false, "仅校验配置后退出，不启动采集")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("probeone-agent %s\n", Version)
		return
	}

	if err := run(*configPath, *checkOnly); err != nil {
		// 日志系统可能未就绪，直接写 stderr
		fmt.Fprintf(os.Stderr, "Agent 启动失败: %v\n", err)
		os.Exit(1)
	}
}
