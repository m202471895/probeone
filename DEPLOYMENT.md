# 部署记录：43.250.175.188

## 环境

| 项 | 值 |
|---|---|
| 系统 | Ubuntu 24.04.5 LTS |
| 架构 | x86_64 |
| 资源 | 6 核 / 5.9GB 内存 / 55GB 可用 |
| 部署路径 | /opt/probeone |
| 面板地址 | http://43.250.175.188:8000 |
| gRPC | 8008（未启用 TLS） |
| 数据库 | SQLite /opt/probeone/data/probeone.db |
| 服务管理 | systemd（probeone.service，已 enable 开机自启）|

## 目录结构

    /opt/probeone/
    ├── bin/probeone        主程序（19MB）
    ├── bin/inituser        用户初始化工具
    ├── bin/probeone.bak    上一版本备份
    ├── data/probeone.db    SQLite 数据库
    ├── logs/probeone.log   启动时的日志
    └── .env                配置（权限 600，含 master key）

## 常用运维命令

    systemctl status probeone      查看状态
    systemctl restart probeone     重启
    journalctl -u probeone -f      跟踪日志
    journalctl -u probeone -n 100  最近 100 行

## 登录信息

    用户名：admin
    密码：Andnode2026Test
    角色：owner

⚠️ **部署完成后必须做的事**：
1. 立即修改 admin 密码
2. 立即轮换两个服务器的 root 密码（部署期间在对话中明文传递过）
3. 面板目前是 HTTP 无加密，建议加 Nginx 反代 + Let's Encrypt

## 已验证的功能

- [x] HTTP 服务启动，公网 90ms 响应
- [x] gRPC 服务启动（Agent 接入端口）
- [x] 7 个后台任务全部运行
- [x] 数据库迁移（版本 2）
- [x] owner 用户创建与登录
- [x] www.andnode.com 监控创建
- [x] 立即探测：200 状态码
- [x] 后台自动探测：60 条记录
- [x] SSL 证书信息入库（53 天到期）
- [x] systemd 开机自启 + 崩溃重启

## 未完成

- [ ] **Agent 未安装**（154.94.236.71 的 SSH 端口不可用，详见下）
- [ ] 前端界面未验证（未在浏览器打开过面板）
- [ ] 告警通知通道未配置
- [ ] 未配 HTTPS

## Agent 服务器问题

154.94.236.71 的 22 端口 TCP 可连接，但**服务端不发送 SSH banner**：
从本机和从 43.250.175.188 两个来源测试都是连上后 12 秒收不到任何数据。

这不是 SSH 服务的正常表现。可能原因：
- sshd 未运行或被防火墙拦截在应用层之前
- 端口被其他服务占用
- 云厂商安全组配置异常

需要在云控制台检查该实例的状态与安全组。
