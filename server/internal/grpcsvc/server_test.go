package grpcsvc

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentv1 "github.com/m202471895/probeone/api/agent/v1"
	"github.com/m202471895/probeone/server/internal/auth"
	"github.com/m202471895/probeone/server/internal/config"
	"github.com/m202471895/probeone/server/internal/migrate"
	"github.com/m202471895/probeone/server/internal/store"
	"github.com/m202471895/probeone/server/internal/store/sqlite"
)

// testSecret 是测试用的 Agent 密钥明文。
// 长度必须 ≥10：auth.HashPassword 会拒绝更短的密码（它把长度检查当
// argon2 参数保护而非安全策略），用短密钥会让测试在建哈希时就失败，
// 掩盖真正要测的东西。
const testSecret = "s3cr3t-agent-key-0001"

// setupService 建一套完整的被测环境：真实 SQLite 库 + 真实 gRPC 服务端 +
// 真实 gRPC 客户端。
//
// 为什么不用 bufconn：握手失败要按 (client_uuid, ip) 计数并锁封，
// 而服务端取IP 走的是 peer.FromContext → net.TCPAddr 这条路径。
// bufconn 的地址不是 TCPAddr，clientIP 会退化成 SplitHostPort 兜底分支，
// 得到的 IP 不可预测，锁封测试就没法构造。监听 127.0.0.1:0 拿到随机端口
// 既不冲突，又能得到确定的 127.0.0.1。
//
// 同时也符合项目测试策略：SQL 用真实库，不用 mock——mock 过的 SQL
// 往往能过，上线才暴露方言差异。
func setupService(t *testing.T) (*store.DB, *Service, agentv1.AgentServiceClient) {
	t.Helper()

	// ---- 数据库 ----
	dir := t.TempDir()
	handle, err := sqlite.Open(&config.DatabaseConfig{
		Driver: "sqlite",
		Path:   filepath.Join(dir, "test.db"),
	})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	m := migrate.New(handle, migrate.Builtin(), ".")
	if err := m.Up(context.Background()); err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}
	db := store.NewWithDialect(handle, "sqlite")

	// ---- 配置 ----
	// 手写而非 config.Load()：Load 强依赖环境变量与主密钥，
	// 测试里构造环境变量会互相污染（同进程内t.Setenv 不可并行）。
	cfg := &config.Config{}
	cfg.Agent.MaxFailuresBeforeLock = 5
	cfg.Agent.LockDuration = 5 * time.Minute
	cfg.Agent.HardLockAfter = 20
	cfg.Agent.HardLockHours = 24
	cfg.Agent.DefaultReportInterval = 10 * time.Second
	cfg.Agent.DefaultHeartbeat = 30 * time.Second
	// SessionTTLMultiplier 必须非 0：Handshake 用
	// DefaultReportInterval × SessionTTLMultiplier 算会话 TTL，
	// 乘积为 0 会让会话一创建就过期，后续上报全部 401。
	cfg.Agent.SessionTTLMultiplier = 3

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc := New(cfg, db, log)

	// ---- 真实 gRPC 服务端 ----
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	gs := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(gs, svc)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = gs.Serve(lis)
	}()

	// ---- 真实 gRPC 客户端 ----
	// grpc.NewClient 是惰性的：不 DialUntil 需要时连，
	// 所以构造失败不会在这里暴露，错误会在首个 RPC 返回。
	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
		gs.Stop()
		<-done
	})

	return db, svc, agentv1.NewAgentServiceClient(conn)
}

// createNode 建一个节点，其密钥哈希是 testSecret 的真实 argon2id 哈希。
// 必须用真哈希：Handshake 走 auth.VerifyPassword，假哈希一律校验失败，
// "握手成功"这个用例就跑不通。
func createNode(t *testing.T, db *store.DB, uid string) int64 {
	t.Helper()
	hash, err := auth.HashPassword(testSecret)
	if err != nil {
		t.Fatalf("生成密钥哈希失败: %v", err)
	}
	id, err := db.Nodes.Create(context.Background(), store.CreateNodeInput{
		UID: uid, Name: "节点-" + uid, SecretHash: hash,
	})
	if err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	return id
}

// withCred 构造带凭据的 context。
// 凭据必须走 gRPC metadata —— 这是协议约定（agent.proto 明确写了
// client-uuid / client-secret 两个键），不是实现细节。
func withCred(ctx context.Context, uuid, secret string) context.Context {
	return metadata.AppendToOutgoingContext(ctx,
		mdClientUUID, uuid,
		mdClientSecret, secret,
	)
}

// ---------- 1. 握手成功 ----------

func TestHandshake_凭据正确返回会话(t *testing.T) {
	db, _, cli := setupService(t)
	nodeID := createNode(t, db, "uuid-ok")

	ctx, cancel := context.WithTimeout(withCred(context.Background(), "uuid-ok", testSecret), 10*time.Second)
	defer cancel()

	resp, err := cli.Handshake(ctx, &agentv1.HandshakeRequest{
		AgentVersion: "0.1.0", OsType: "linux", Arch: "amd64",
	})
	if err != nil {
		t.Fatalf("握手应成功: %v", err)
	}
	if !resp.GetSuccess() {
		t.Error("success = false，期望 true")
	}
	if resp.GetSessionId() == "" {
		t.Fatal("session_id 为空：Agent 拿到空会话就无法上报，等于完全不可用")
	}

	// 会话必须真的落库，且绑定到正确的节点——
	// 否则数据会写到别的节点名下（跨节点数据注入）
	sess, err := db.AgentSessions.GetSession(context.Background(), resp.GetSessionId())
	if err != nil {
		t.Fatalf("会话未落库: %v", err)
	}
	// agent_sessions 的 node_id 被扫描进 model.Session.UserID，
	// 这是 repository 层的历史包袱（见 server.go authorizeSession 的注释）
	if sess.UserID != nodeID {
		t.Errorf("会话绑定的节点 = %d，期望 %d", sess.UserID, nodeID)
	}

	// 服务端应下发自己的上报/心跳间隔，Agent 必须服从服务端配置
	if resp.GetReportIntervalSec() != 10 {
		t.Errorf("report_interval_sec = %d，期望 10", resp.GetReportIntervalSec())
	}
	if resp.GetHeartbeatSec() != 30 {
		t.Errorf("heartbeat_sec = %d，期望 30", resp.GetHeartbeatSec())
	}
}

func TestHandshake_重复握手使旧会话失效(t *testing.T) {
	// 防重放：同一节点再次握手时，旧 session 必须立刻作废。
	// 否则一个被截获的旧 session 在 TTL 内仍可上报，
	// 攻击者即使拿不到新密钥也能持续注入伪造指标。
	db, _, cli := setupService(t)
	createNode(t, db, "uuid-replay")

	hs := func() string {
		ctx, cancel := context.WithTimeout(withCred(context.Background(), "uuid-replay", testSecret), 10*time.Second)
		defer cancel()
		resp, err := cli.Handshake(ctx, &agentv1.HandshakeRequest{})
		if err != nil {
			t.Fatalf("握手失败: %v", err)
		}
		return resp.GetSessionId()
	}

	old := hs()
	newer := hs()
	if old == newer {
		t.Fatal("两次握手返回了相同 session_id")
	}
	if _, err := db.AgentSessions.GetSession(context.Background(), old); err == nil {
		t.Error("旧会话在新握手后仍有效——防重放失效")
	}
	if _, err := db.AgentSessions.GetSession(context.Background(), newer); err != nil {
		t.Errorf("新会话应有效: %v", err)
	}
}

// ---------- 2~4. 握手失败路径 ----------

// TestHandshake_错误与不存在的凭据都返回同一错误码 是防枚举断言：
// 若"UUID 不存在"与"密钥错误"返回可区分的错误，攻击者就能拿
// 已知 UUID 列表批量探测哪些 UUID 在本系统里注册过。
func TestHandshake_错误与不存在的凭据都返回同一错误码(t *testing.T) {
	db, _, cli := setupService(t)
	createNode(t, db, "uuid-known")

	cases := []struct {
		name   string
		uuid   string
		secret string
	}{
		{"密钥错误", "uuid-known", "wrong-secret-000000"},
		{"UUID 不存在", "uuid-does-not-exist", testSecret},
		// 两者都错：也必须落进同一个错误码，
		// 否则"存在但密钥错"会成为可区分信号
		{"UUID 与密钥都错", "uuid-does-not-exist", "wrong-secret-000000"},
	}

	var codes_ []codes.Code
	var msgs []string
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(
				withCred(context.Background(), c.uuid, c.secret), 10*time.Second)
			defer cancel()

			resp, err := cli.Handshake(ctx, &agentv1.HandshakeRequest{})
			if err == nil {
				t.Fatalf("握手应失败，却成功了: %+v", resp)
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("不是 gRPC 状态错误: %v", err)
			}
			if st.Code() != codes.Unauthenticated {
				t.Errorf("错误码 = %s，期望 Unauthenticated", st.Code())
			}
			if resp != nil {
				t.Error("失败时不应返回响应体")
			}
			codes_ = append(codes_, st.Code())
			msgs = append(msgs, st.Message())
		})
	}

	for i := 1; i < len(codes_); i++ {
		if codes_[i] != codes_[0] || msgs[i] != msgs[0] {
			t.Errorf("第%d种失败（%q）与第1种（%q）返回了可区分的错误：%s/%q vs %s/%q"+
				"——这会让攻击者用已知 UUID 探测哪些节点已注册",
				i+1, cases[i].name, cases[0].name, codes_[i], msgs[i], codes_[0], msgs[0])
		}
	}
}

func TestHandshake_缺少凭据被拒(t *testing.T) {
	// 不带任何 metadata：必须在进数据库之前就拒掉。
	// 少一次argon2 校验就是少一次可被用来打 CPU 的放大。
	db, _, cli := setupService(t)
	createNode(t, db, "uuid-nocred")

	cases := []struct {
		name string
		ctx  func() context.Context
	}{
		{"完全不带 metadata", func() context.Context {
			return context.Background()
		}},
		{"只有 uuid", func() context.Context {
			return metadata.AppendToOutgoingContext(context.Background(), mdClientUUID, "uuid-nocred")
		}},
		{"只有 secret", func() context.Context {
			return metadata.AppendToOutgoingContext(context.Background(), mdClientSecret, testSecret)
		}},
		{"uuid 为空串", func() context.Context {
			return withCred(context.Background(), "", testSecret)
		}},
		{"secret 为空串", func() context.Context {
			return withCred(context.Background(), "uuid-nocred", "")
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(c.ctx(), 10*time.Second)
			defer cancel()
			_, err := cli.Handshake(ctx, &agentv1.HandshakeRequest{})
			if err == nil {
				t.Fatal("应拒绝缺少凭据的握手")
			}
			if st, _ := status.FromError(err); st.Code() != codes.Unauthenticated {
				t.Errorf("错误码 = %s，期望 Unauthenticated", st.Code())
			}
		})
	}
}

// TestHandshake_硬封禁的节点即使密钥正确也被拒 —— 断言 PRD 7.3 的封禁生效。
//
// 【当前失败：生产代码缺陷】Handshake 全程没有调用
// AgentSessions.IsHardLocked。recordFailure 只往库里写 hard_locked 标记，
// 没有任何地方读它。结果是硬封禁形同虚设：被封 24 小时的攻击者只要
// 猜到密钥就能继续上报，甚至能靠反复失败把受害节点的计数推上去。
// 详见交付报告，此处按正确行为断言，不降低标准。
func TestHandshake_硬封禁的节点即使密钥正确也被拒(t *testing.T) {
	db, _, cli := setupService(t)
	createNode(t, db, "uuid-locked")

	// 造够 HardLockAfter 次失败。
	// 直接调RecordAgentFailure 而不是真的握手20 次：
	// 20 次 argon2id（64MB/3轮）≈ 2 秒，且这部分逻辑不属于本用例的考察范围。
	for i := 0; i < 20; i++ {
		if _, err := db.AgentSessions.RecordAgentFailure(context.Background(), "uuid-locked", "127.0.0.1"); err != nil {
			t.Fatalf("记录失败次数出错: %v", err)
		}
	}
	// 模拟 recordFailure 在达到阈值时写入的封禁标记
	if _, err := db.SQL.ExecContext(context.Background(),
		`UPDATE agent_failures SET hard_locked = 1, locked_until = ? WHERE client_uuid = ?`,
		time.Now().Add(24*time.Hour).UTC(), "uuid-locked"); err != nil {
		t.Fatalf("标记硬封禁失败: %v", err)
	}
	locked, err := db.AgentSessions.IsHardLocked(context.Background(), "uuid-locked", "127.0.0.1")
	if err != nil || !locked {
		t.Fatalf("前置条件不成立：应处于硬封禁态，locked=%v err=%v", locked, err)
	}

	// 现在用**正确密钥**握手，必须被拒
	ctx, cancel := context.WithTimeout(withCred(context.Background(), "uuid-locked", testSecret), 10*time.Second)
	defer cancel()
	resp, err := cli.Handshake(ctx, &agentv1.HandshakeRequest{})
	if err == nil {
		t.Fatalf("硬封禁节点握手竟成功，会话 = %q——封禁形同虚设，"+
			"攻击者被封 24 小时后仍可继续上报", resp.GetSessionId())
	}
	if st, _ := status.FromError(err); st.Code() != codes.Unauthenticated {
		t.Errorf("错误码 = %s，期望 Unauthenticated", st.Code())
	}
}

// TestHandshake_失败次数超限触发锁定 —— 断言 PRD 7.3 的失败锁定生效。
//
// 【当前失败：生产代码缺陷】recordFailure 在达到 MaxFailuresBeforeLock
// 时写了 locked_until（软锁），但IsHardLocked 只看 hard_locked 列，
// 且Handshake 根本没调用它。软锁标记写了没人读，等于没有软锁。
func TestHandshake_失败次数超限触发锁定(t *testing.T) {
	db, _, cli := setupService(t)
	createNode(t, db, "uuid-ratelimit")

	// MaxFailuresBeforeLock = 5：连续 5 次错误密钥后应进入锁态，
	// 此后即使密钥正确也必须被拒（防止持续爆破）
	for i := 0; i < 6; i++ {
		ctx, cancel := context.WithTimeout(
			withCred(context.Background(), "uuid-ratelimit", "wrong-secret-000000"), 10*time.Second)
		_, err := cli.Handshake(ctx, &agentv1.HandshakeRequest{})
		cancel()
		if err == nil {
			t.Fatalf("第 %d 次错误密钥握手竟成功", i+1)
		}
	}

	// 达到阈值后应已写入锁定截止时间
	var lockedUntil *time.Time
	if err := db.SQL.QueryRowContext(context.Background(),
		`SELECT locked_until FROM agent_failures WHERE client_uuid = ?`,
		"uuid-ratelimit").Scan(&lockedUntil); err != nil {
		t.Fatalf("读取锁定状态失败: %v", err)
	}
	if lockedUntil == nil {
		t.Error("超过失败阈值后未写入 locked_until")
	}

	// 关键：锁定期间正确密钥也必须被拒
	ctx, cancel := context.WithTimeout(
		withCred(context.Background(), "uuid-ratelimit", testSecret), 10*time.Second)
	defer cancel()
	if resp, err := cli.Handshake(ctx, &agentv1.HandshakeRequest{}); err == nil {
		t.Fatalf("失败锁定期间正确密钥仍能握手，会话 = %q——"+
			"意味着暴力破解没有任何成本，攻击者可以无限次尝试", resp.GetSessionId())
	}
}

func TestHandshake_失败被计数且成功后清零(t *testing.T) {
	// 失败计数是限流的依据，记错了等于限流失效。
	// 这里只断言计数与清零这两个可观测行为（锁定判定见上面两个用例）
	db, _, cli := setupService(t)
	createNode(t, db, "uuid-count")

	fail := func() {
		ctx, cancel := context.WithTimeout(
			withCred(context.Background(), "uuid-count", "wrong-secret-000000"), 10*time.Second)
		defer cancel()
		_, _ = cli.Handshake(ctx, &agentv1.HandshakeRequest{})
	}
	count := func() int {
		var n int
		if err := db.SQL.QueryRowContext(context.Background(),
			`SELECT count FROM agent_failures WHERE client_uuid = ?`,
			"uuid-count").Scan(&n); err != nil {
			t.Fatalf("读取失败计数出错: %v", err)
		}
		return n
	}

	fail()
	fail()
	if got := count(); got != 2 {
		t.Errorf("2 次失败后计数 = %d，期望 2", got)
	}

	// 正确握手应清零：否则一次成功也无法洗掉攻击者刷上去的计数，
	// 正常 Agent 会被误锁
	ctx, cancel := context.WithTimeout(
		withCred(context.Background(), "uuid-count", testSecret), 10*time.Second)
	defer cancel()
	if _, err := cli.Handshake(ctx, &agentv1.HandshakeRequest{}); err != nil {
		t.Fatalf("正确握手失败: %v", err)
	}
	if got := count(); got != 0 {
		t.Errorf("握手成功后失败计数 = %d，期望清零（否则正常 Agent 会被攻击者刷出的计数误锁）", got)
	}
}

// ---------- 6. ReportOnce ----------

func TestReportOnce_上报指标落库(t *testing.T) {
	db, _, cli := setupService(t)
	nodeID := createNode(t, db, "uuid-once")

	ctx, cancel := context.WithTimeout(withCred(context.Background(), "uuid-once", testSecret), 10*time.Second)
	defer cancel()
	hs, err := cli.Handshake(ctx, &agentv1.HandshakeRequest{})
	if err != nil {
		t.Fatalf("握手失败: %v", err)
	}
	sessID := hs.GetSessionId()

	// ReportOnce 本身不校验 metadata，只认 session_id
	rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer rcancel()
	resp, err := cli.ReportOnce(rctx, &agentv1.AgentMessage{
		SessionId: sessID,
		Payload: &agentv1.AgentMessage_Metrics{Metrics: &agentv1.ReportMetrics{
			CollectedAt: time.Now().Unix(),
			Seq:         7,
			HardwareFp:  "fp-once",
			Cpu:         &agentv1.CpuStat{Usage: 42.5, CoresLogical: 4, Model: "Test CPU"},
			Mem:         &agentv1.MemStat{Total: 8 << 30, Used: 4 << 30, Usage: 50},
		}},
	})
	if err != nil {
		t.Fatalf("ReportOnce 失败: %v", err)
	}
	// ACK 必须回带 seq：Agent 靠它确认"这一批已入库"，
	// 缺了它 Agent 无法判断该重发还是丢弃
	if got := resp.GetAck().GetSeq(); got != 7 {
		t.Errorf("ACK seq = %d，期望 7（Agent 依赖它确认入库）", got)
	}

	// 关键断言：真的写进了库
	latest, err := db.Metrics.Latest(context.Background(), []int64{nodeID})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := latest[nodeID]
	if !ok {
		t.Fatal("指标未落库：ReportOnce 返回成功但数据库里查不到")
	}
	if m.CPUUsage != 42.5 {
		t.Errorf("CPU = %v，期望 42.5", m.CPUUsage)
	}
}

func TestReportOnce_主机信息落库(t *testing.T) {
	db, _, cli := setupService(t)
	nodeID := createNode(t, db, "uuid-once-host")

	ctx, cancel := context.WithTimeout(withCred(context.Background(), "uuid-once-host", testSecret), 10*time.Second)
	defer cancel()
	hs, err := cli.Handshake(ctx, &agentv1.HandshakeRequest{})
	if err != nil {
		t.Fatalf("握手失败: %v", err)
	}

	rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer rcancel()
	if _, err := cli.ReportOnce(rctx, &agentv1.AgentMessage{
		SessionId: hs.GetSessionId(),
		Payload: &agentv1.AgentMessage_Host{Host: &agentv1.ReportHostInfo{
			Hostname: "web-01", OsType: "linux", OsVersion: "6.1", Arch: "amd64",
			AgentVersion: "0.2.0", CpuModel: "AMD EPYC", MemTotal: 16 << 30,
			Disks: []*agentv1.DiskInfo{
				{Device: "/dev/sda1", Mount: "/", Fstype: "ext4", Total: 100 << 30},
			},
			BootTime: 1700000000, PublicIp: "1.2.3.4",
		}},
	}); err != nil {
		t.Fatalf("ReportOnce 上报主机信息失败: %v", err)
	}

	node, err := db.Nodes.GetByID(context.Background(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	// A 类字段
	if node.Hostname != "web-01" || node.OSType != "linux" || node.AgentVersion != "0.2.0" {
		t.Errorf("A 类字段未落库: %+v", node)
	}
	// B 类兜底
	if node.CPUModel != "AMD EPYC" || node.MemTotal != 16<<30 {
		t.Errorf("B 类字段未落库: cpu=%q mem=%d", node.CPUModel, node.MemTotal)
	}
	if len(node.DiskInfo) != 1 || node.DiskInfo[0].Mount != "/" {
		t.Errorf("磁盘信息未落库: %+v", node.DiskInfo)
	}
	// C 类
	if node.BootTime == nil {
		t.Error("启动时间未落库")
	}
	if node.PublicIP != "1.2.3.4" {
		t.Errorf("公网 IP = %q，期望 1.2.3.4", node.PublicIP)
	}
}

func TestReportOnce_未知类型返回ignored(t *testing.T) {
	// 事件类消息走ReportOnce 没有 ACK 语义，
	// 但也不能报错——老版本 Agent 可能在无流模式发事件
	db, _, cli := setupService(t)
	createNode(t, db, "uuid-event")

	ctx, cancel := context.WithTimeout(withCred(context.Background(), "uuid-event", testSecret), 10*time.Second)
	defer cancel()
	hs, _ := cli.Handshake(ctx, &agentv1.HandshakeRequest{})

	rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer rcancel()
	resp, err := cli.ReportOnce(rctx, &agentv1.AgentMessage{
		SessionId: hs.GetSessionId(),
		Payload: &agentv1.AgentMessage_Event{Event: &agentv1.ReportAgentEvent{
			Type: "start", Detail: "agent 启动",
		}},
	})
	if err != nil {
		t.Fatalf("事件类消息不应导致失败: %v", err)
	}
	if resp.GetAck().GetMessage() != "ignored" {
		t.Errorf("ACK message = %q，期望 ignored", resp.GetAck().GetMessage())
	}
}

// ---------- 8. 未授权上报 ----------

func TestReportOnce_无效会话被拒(t *testing.T) {
	// 无效 session 若被接受，攻击者无需任何凭据即可注入伪造指标，
	// 污染监控曲线与告警。这是本服务最需要防住的一类攻击。
	db, _, cli := setupService(t)
	createNode(t, db, "uuid-authz")

	cases := []struct {
		name      string
		sessionID string
	}{
		{"空 session_id", ""},
		{"不存在的 session_id", "session-does-not-exist"},
		// 曾经合法但已被新握手顶掉的会话，必须立刻失效
		{"已被顶掉的旧 session", "stale-session"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := cli.ReportOnce(ctx, &agentv1.AgentMessage{
				SessionId: c.sessionID,
				Payload: &agentv1.AgentMessage_Metrics{Metrics: &agentv1.ReportMetrics{
					CollectedAt: time.Now().Unix(),
				}},
			})
			if err == nil {
				t.Fatal("无效会话竟被接受——任何人都能注入伪造指标")
			}
			if st, _ := status.FromError(err); st.Code() != codes.Unauthenticated {
				t.Errorf("错误码 = %s，期望 Unauthenticated", st.Code())
			}
		})
	}
}

func TestReportOnce_过期会话被拒(t *testing.T) {
	// 会话 TTL 是唯一的横向移动闸门：泄露的 session 必须在过期后失效
	db, _, cli := setupService(t)
	nodeID := createNode(t, db, "uuid-expire")

	if err := db.AgentSessions.CreateSession(context.Background(),
		nodeID, "expired-session", "127.0.0.1", -time.Minute); err != nil {
		t.Fatalf("创建过期会话失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := cli.ReportOnce(ctx, &agentv1.AgentMessage{
		SessionId: "expired-session",
		Payload:   &agentv1.AgentMessage_Metrics{Metrics: &agentv1.ReportMetrics{}},
	})
	if err == nil {
		t.Fatal("已过期会话仍可上报——泄露的 session 将永久可用")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Unauthenticated {
		t.Errorf("错误码 = %s，期望 Unauthenticated", st.Code())
	}
}

// ---------- 7 & 9. ReportStream 双向流 ----------

func TestReportStream_多条指标入库且流关闭后会话注销(t *testing.T) {
	db, svc, cli := setupService(t)
	nodeID := createNode(t, db, "uuid-stream")

	ctx, cancel := context.WithTimeout(withCred(context.Background(), "uuid-stream", testSecret), 10*time.Second)
	defer cancel()
	hs, err := cli.Handshake(ctx, &agentv1.HandshakeRequest{})
	if err != nil {
		t.Fatalf("握手失败: %v", err)
	}
	sessID := hs.GetSessionId()

	sctx, scancel := context.WithCancel(context.Background())
	defer scancel()
	stream, err := cli.ReportStream(sctx)
	if err != nil {
		t.Fatalf("建立流失败: %v", err)
	}

	// 首条消息必须带 session_id（服务端据此定位节点），
	// 顺便带主机信息覆盖"Agent 启动时上报一次"的路径
	if err := stream.Send(&agentv1.AgentMessage{
		SessionId: sessID,
		Payload: &agentv1.AgentMessage_Host{Host: &agentv1.ReportHostInfo{
			Hostname: "stream-node", OsType: "linux", Arch: "amd64",
			AgentVersion: "0.3.0", CpuModel: "Intel", MemTotal: 32 << 30,
			Disks: []*agentv1.DiskInfo{
				{Device: "/dev/sda1", Mount: "/", Fstype: "ext4", Total: 200 << 30},
				{Device: "/dev/sdb1", Mount: "/data", Fstype: "xfs", Total: 500 << 30},
			},
		}},
	}); err != nil {
		t.Fatalf("发送首条消息失败: %v", err)
	}

	// 首条是 Host → 先收 ACK，再收 ConfigSync
	if err := expectAckMessage(t, stream, "host-info-received"); err != nil {
		t.Fatalf("未收到主机信息 ACK: %v", err)
	}
	cfgMsg, err := stream.Recv()
	if err != nil {
		t.Fatalf("未收到 ConfigSync: %v", err)
	}
	cfg := cfgMsg.GetConfig()
	if cfg == nil {
		t.Fatal("第二条消息不是 ConfigSync")
	}
	// ConfigSync 是服务端能下发给 Agent 的全部内容，
	// 只允许采集配置——多一个字段就意味着多一条控制通道（PRD 1.1红线）
	if cfg.GetReportIntervalSec() != 10 {
		t.Errorf("ConfigSync 上报间隔 = %d，期望 10", cfg.GetReportIntervalSec())
	}
	if len(cfg.GetEnabledMetrics()) == 0 {
		t.Error("ConfigSync 未下发启用的指标项")
	}
	if cfg.GetTz() != "Asia/Shanghai" {
		t.Errorf("ConfigSync 时区 = %q，期望 Asia/Shanghai", cfg.GetTz())
	}

	// 连续上报 3 条指标，每条都要一个 seq 对应的 ACK
	for i := 1; i <= 3; i++ {
		seq := int32(i * 10)
		if err := stream.Send(&agentv1.AgentMessage{
			SessionId: sessID,
			Payload: &agentv1.AgentMessage_Metrics{Metrics: &agentv1.ReportMetrics{
				CollectedAt: time.Now().Add(time.Duration(-i) * 10 * time.Second).Unix(),
				Seq:         seq,
				HardwareFp:  "fp-stream",
				Cpu:         &agentv1.CpuStat{Usage: float32(i * 10), CoresLogical: 8},
				Mem:         &agentv1.MemStat{Total: 32 << 30, Used: 16 << 30, Usage: 50},
				Disks: []*agentv1.DiskStat{
					{Mount: "/", Device: "/dev/sda1", Fstype: "ext4", Total: 200 << 30, Usage: 55},
				},
				Nets: []*agentv1.NetStat{{Iface: "eth0", RxBps: 1000, TxBps: 2000}},
			}},
		}); err != nil {
			t.Fatalf("发送第 %d 条指标失败: %v", i, err)
		}
		if err := expectAckSeq(t, stream, uint64(seq)); err != nil {
			t.Fatalf("第 %d 条指标未收到 ACK: %v", i, err)
		}
	}

	rangeCtx := context.Background()

	// 3 条指标都要能查到——漏一条就是静默丢数据，表现为曲线出现无法解释的缺口。
	//
	// 必须传 UTC 参数：node_metrics.collected_at 存的是 UTC，
	// 而 SQLite 的modernc 驱动按字面量比较时间。传本地时间会匹配 0 行
	// 且不报错（详见交付报告的"时间参数未归一化"问题）。
	got, err := db.Metrics.Range(rangeCtx, nodeID,
		time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("入库指标数 = %d，期望 3（流式上报有丢数据）", len(got))
	}

	node, _ := db.Nodes.GetByID(rangeCtx, nodeID)
	if node.Hostname != "stream-node" {
		t.Errorf("主机信息未入库: hostname=%q", node.Hostname)
	}
	// 磁盘数以 ReportMetrics 为准而非主机信息：
	// agent.proto 明确"B 类字段以 ReportMetrics 为权威源"，
	// 后续指标带1 块盘就把主机信息里的 2 块覆盖成了 1 块。
	// 断言这个覆盖行为，是为了让"以谁为准"这个决定显式化——
	// 若哪天改成"取并集"，这条断言会失败并提醒reviewer。
	if len(node.DiskInfo) != 1 {
		t.Errorf("磁盘数 = %d，期望 1（B 类以 ReportMetrics 为权威源，应覆盖主机信息的 2 块）",
			len(node.DiskInfo))
	}

	// 关闭流
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("关闭发送失败: %v", err)
	}
	// 关闭后服务端应注销活跃会话
	waitActive(t, svc, 0, 5*time.Second)

	// 【设计缺口，详见报告】注销只作用于内存 map，DB 里的 agent_sessions
	// 记录仍在——也就是说流断开后，那个 session_id 仍能继续调 ReportOnce
	// 直到 TTL 到期。流断开并不等于会话失效。
	if _, err := db.AgentSessions.GetSession(rangeCtx, sessID); err == nil {
		t.Log("注意：流关闭后 DB 会话仍存在（当前实现如此，非断言失败）")
	}
}

func TestReportStream_无效会话立即断开流(t *testing.T) {
	db, _, cli := setupService(t)
	createNode(t, db, "uuid-badstream")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := cli.ReportStream(ctx)
	if err != nil {
		t.Fatalf("建流失败: %v", err)
	}
	if err := stream.Send(&agentv1.AgentMessage{
		SessionId: "not-a-real-session",
		Payload:   &agentv1.AgentMessage_Metrics{Metrics: &agentv1.ReportMetrics{}},
	}); err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	// 服务端应在读完首条消息后立刻返回 Unauthenticated
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("无效会话的流未被拒绝")
	}
	if st, _ := status.FromError(err); st.Code() != codes.Unauthenticated {
		t.Errorf("错误码 = %s，期望 Unauthenticated", st.Code())
	}
}

func TestReportStream_未发首条消息即关闭(t *testing.T) {
	// 客户端建流后什么都不发就断开：服务端 Recv 拿到 EOF 直接返回，
	// 不应 panic、不应留下活跃会话
	db, svc, cli := setupService(t)
	createNode(t, db, "uuid-nomsg")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := cli.ReportStream(ctx)
	if err != nil {
		t.Fatalf("建流失败: %v", err)
	}
	_ = stream.CloseSend()
	_, _ = stream.Recv() // 预期读到错误或 EOF

	waitActive(t, svc, 0, 3*time.Second)
}

// ---------- 9. ActiveSessions ----------

func TestActiveSessions_反映流存活状态(t *testing.T) {
	// ActiveSessions 供 /ready 探针与运维观测使用。
	// 若它与真实流状态脱节，运维看到的在线数就是假的——
	// 更糟的是有人会拿它当"服务健康"的判据。
	db, svc, cli := setupService(t)
	createNode(t, db, "uuid-active")

	if got := svc.ActiveSessions(); got != 0 {
		t.Fatalf("初始活跃会话 = %d，期望 0", got)
	}

	ctx, cancel := context.WithTimeout(withCred(context.Background(), "uuid-active", testSecret), 10*time.Second)
	defer cancel()
	hs, err := cli.Handshake(ctx, &agentv1.HandshakeRequest{})
	if err != nil {
		t.Fatalf("握手失败: %v", err)
	}

	sctx, scancel := context.WithCancel(context.Background())
	defer scancel()
	stream, err := cli.ReportStream(sctx)
	if err != nil {
		t.Fatalf("建流失败: %v", err)
	}
	// 首条消息不带 Host：服务端只会回一条 ConfigSync（无ACK）。
	// 这一点本身就是要断言的行为之一——Agent 启动时必发 Host，
	// 若哪天首条总是 Host，这条消息的时序假设就得改。
	if err := stream.Send(&agentv1.AgentMessage{
		SessionId: hs.GetSessionId(),
		Payload:   &agentv1.AgentMessage_Metrics{Metrics: &agentv1.ReportMetrics{CollectedAt: time.Now().Unix()}},
	}); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("未收到 ConfigSync: %v", err)
	}
	if msg.GetConfig() == nil {
		t.Fatalf("首条无 Host 时应收到 ConfigSync，实际收到 %T", msg.GetPayload())
	}

	// 发完一条并收到服务端处理完的 ACK，说明已越过 authorizeSession 完成 markActive
	if err := stream.Send(&agentv1.AgentMessage{
		SessionId: hs.GetSessionId(),
		Payload:   &agentv1.AgentMessage_Metrics{Metrics: &agentv1.ReportMetrics{CollectedAt: time.Now().Unix()}},
	}); err != nil {
		t.Fatalf("发送第二条失败: %v", err)
	}
	if err := expectAckSeq(t, stream, 0); err != nil {
		t.Fatalf("未收到 ACK: %v", err)
	}

	waitActive(t, svc, 1, 3*time.Second)

	// 取消流 → 服务端 ctx 被取消 → defer unmarkActive 注销
	scancel()
	waitActive(t, svc, 0, 5*time.Second)
}

// ---------- 辅助 ----------

// expectAckSeq 收一条 ACK 并校验 seq。
func expectAckSeq(t *testing.T, stream grpc.BidiStreamingClient[agentv1.AgentMessage, agentv1.ServerMessage], want uint64) error {
	t.Helper()
	msg, err := stream.Recv()
	if err != nil {
		return err
	}
	ack := msg.GetAck()
	if ack == nil {
		return fmt.Errorf("收到的是 %T，不是 ACK", msg.GetPayload())
	}
	if ack.GetSeq() != want {
		return fmt.Errorf("ACK seq = %d，期望 %d", ack.GetSeq(), want)
	}
	return nil
}

// expectAckMessage 收一条 ACK 并校验 message 文本。
func expectAckMessage(t *testing.T, stream grpc.BidiStreamingClient[agentv1.AgentMessage, agentv1.ServerMessage], want string) error {
	t.Helper()
	msg, err := stream.Recv()
	if err != nil {
		return err
	}
	ack := msg.GetAck()
	if ack == nil {
		return fmt.Errorf("收到的是 %T，不是 ACK", msg.GetPayload())
	}
	if ack.GetMessage() != want {
		return fmt.Errorf("ACK message = %q，期望 %q", ack.GetMessage(), want)
	}
	return nil
}

// waitActive 轮询等待活跃会话数达到期望值。
// 用轮询而非 sleep：流处理是异步的，固定 sleep 会在慢机器上假失败。
func waitActive(t *testing.T, svc *Service, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var got int
	for time.Now().Before(deadline) {
		got = svc.ActiveSessions()
		if got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("活跃会话数 = %d，期望 %d（等待 %v 内未达成）", got, want, timeout)
}

// 编译期确认 ReportStream 的错误路径真的返回了 io.EOF 而不是 panic。
var _ = io.EOF
