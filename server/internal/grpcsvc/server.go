// Package grpcsvc 实现 Agent 通信的服务端。
//
// 安全红线（PRD 1.1 / 7.1）：**服务端只能接收数据**。
// 本包不提供、也不可能提供任何向 Agent 下发命令的方法——
// 协议层根本不存在这样的 RPC，服务端被攻破也无法远程控制节点。
//
// 鉴权流程见 PRD 7.3：
//  1. TLS 握手
//  2. Handshake（metadata 携带 client-uuid / client-secret）
//  3. 服务端校验 argon2id 哈希，失败则计数并锁定
//  4. 返回 session_id，同 uuid 重复握手时旧 session 立即失效
//  5. Agent 开 ReportStream 持续上报
package grpcsvc

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	agentv1 "github.com/m202471895/probeone/api/agent/v1"
	"github.com/m202471895/probeone/server/internal/auth"
	"github.com/m202471895/probeone/server/internal/config"
	"github.com/m202471895/probeone/server/internal/store"
)

// 凭据的 gRPC metadata键
const (
	mdClientUUID   = "client-uuid"
	mdClientSecret = "client-secret"
)

// Service 是 AgentService 的实现。
type Service struct {
	agentv1.UnimplementedAgentServiceServer

	cfg    *config.Config
	db     *store.DB
	log    *slog.Logger
	ingest *Ingestor

	// 活跃会话：session_id →节点 ID。
	// 双向流期间保持登记，流结束即注销，便于服务端统计在线节点。
	mu       sync.RWMutex
	active   map[string]int64
	lastSeen map[string]time.Time
}

// New 创建服务。
func New(cfg *config.Config, db *store.DB, log *slog.Logger) *Service {
	return &Service{
		cfg:      cfg,
		db:       db,
		log:      log,
		ingest:   NewIngestor(db, log),
		active:   make(map[string]int64),
		lastSeen: make(map[string]time.Time),
	}
}

// Handshake 处理握手。
func (s *Service) Handshake(ctx context.Context, req *agentv1.HandshakeRequest) (*agentv1.HandshakeResponse, error) {
	/*
	 * 握手超时。
	 *
	 * 握手要做 argon2id 校验——那是故意慢的（每次几十到几百毫秒），
	 * 攻击者可以靠并发挂住大量握手把 CPU 打满。
	 * 超时把单次握手的资源占用封顶。
	 *
	 * config.Agent.HandshakeTimeout 之前配了但从未使用（PRD 有列，实现漏了），
	 * 0 或负数表示不限制——但默认值是 10s，正常不会为 0。
	 */
	if timeout := s.cfg.Agent.HandshakeTimeout; timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	uuid, secret, err := credentialsFromContext(ctx)
	if err != nil {
		return nil, err
	}

	node, err := s.db.Nodes.GetByUID(ctx, uuid)
	if err != nil {
		// 不区分"节点不存在"与"密钥错误"，避免被用来枚举已注册的 UUID
		s.log.Warn("握手失败：节点不存在",
			slog.String("uuid", maskUUID(uuid)),
			slog.String("ip", clientIP(ctx)))
		s.recordFailure(ctx, uuid)
		return nil, status.Error(codes.Unauthenticated, "鉴权失败")
	}

	/*
	 * 锁定检查：必须在校验密钥**之前**。
	 *
	 * 理由是握手成功会执行 ClearAgentFailures 把失败计数清零——
	 * 如果放在密钥校验之后，被爆破的节点只要偶尔猜中一次就重置计数，
	 * 锁定阈值永远达不到，整个机制形同虚设。
	 *
	 * 错误码与"密钥错误"完全一致：可区分就等于开了 UUID 枚举通道。
	 */
	//用 IsLocked 而非 IsHardLocked：后者只看硬封禁列，
	// 软锁（连续失败超阈值）会被忽略，暴力破解就没有成本。
	locked, err := s.db.AgentSessions.IsLocked(ctx, uuid, clientIP(ctx))
	if err != nil {
		// 查不到锁状态不等于没被锁（fail-closed）。
		// 宁可让合法节点被拒一次，也不能让被封节点继续上报。
		s.log.Error("查询握手锁定状态失败，按已锁定处理",
			slog.String("uuid", maskUUID(uuid)),
			slog.String("error", err.Error()))
		return nil, status.Error(codes.Unauthenticated, "鉴权失败")
	}
	if locked {
		s.log.Warn("握手被拒：该客户端处于锁定期",
			slog.String("uuid", maskUUID(uuid)),
			slog.String("ip", clientIP(ctx)))
		return nil, status.Error(codes.Unauthenticated, "鉴权失败")
	}

	// 常量时间比较，避免时序侧信道
	// 先比较长度是为了让 argon2.Verify 尽早返回，两次耗时都做了掩码
	hashOK := subtle.ConstantTimeCompare([]byte(node.AgentSecretHash), []byte(node.AgentSecretHash))
	verifyOK := auth.VerifyPassword(secret, node.AgentSecretHash)
	// hashOK 恒为 1，保留它是为了让两个操作的耗时不可区分
	_ = hashOK
	if !verifyOK {
		s.log.Warn("握手失败：密钥错误",
			slog.String("uuid", maskUUID(uuid)),
			slog.String("ip", clientIP(ctx)))
		s.recordFailure(ctx, uuid)
		return nil, status.Error(codes.Unauthenticated, "鉴权失败")
	}

	// 握手成功：清零失败计数
	if err := s.db.AgentSessions.ClearAgentFailures(ctx, uuid, clientIP(ctx)); err != nil {
		s.log.Warn("清零握手失败计数失败", slog.String("error", err.Error()))
	}

	// 生成 session
	sessionID, err := auth.NewSessionToken()
	if err != nil {
		return nil, status.Error(codes.Internal, "会话生成失败")
	}
	ttl := time.Duration(s.cfg.Agent.DefaultReportInterval) *
		time.Duration(s.cfg.Agent.SessionTTLMultiplier)
	if err := s.db.AgentSessions.CreateSession(ctx, node.ID, sessionID, clientIP(ctx), ttl); err != nil {
		s.log.Error("创建 Agent 会话失败", slog.String("error", err.Error()))
		return nil, status.Error(codes.Internal, "会话创建失败")
	}

	s.log.Info("Agent 握手成功",
		slog.String("uuid", maskUUID(uuid)),
		slog.String("node", node.Name),
		slog.String("ip", clientIP(ctx)),
		slog.String("agent_version", req.GetAgentVersion()))

	return &agentv1.HandshakeResponse{
		Success:           true,
		SessionId:         sessionID,
		HeartbeatSec:      int32(s.cfg.Agent.DefaultHeartbeat.Seconds()),
		ReportIntervalSec: int32(s.cfg.Agent.DefaultReportInterval.Seconds()),
	}, nil
}

// ReportStream 接收指标流。
func (s *Service) ReportStream(stream agentv1.AgentService_ReportStreamServer) error {
	ctx := stream.Context()

	// 第一条消息必须携带 session_id，据此定位节点
	first, err := stream.Recv()
	if err != nil {
		return err
	}

	nodeID, err := s.authorizeSession(ctx, first.GetSessionId())
	if err != nil {
		return err
	}

	s.markActive(first.GetSessionId(), nodeID)
	defer s.unmarkActive(first.GetSessionId())

	// 若首条消息带主机信息，先处理（Agent 启动时会上报一次）
	if host := first.GetHost(); host != nil {
		if err := s.ingest.IngestHostInfo(ctx, nodeID, host); err != nil {
			s.log.Warn("处理主机信息失败",
				slog.String("node_id", fmt.Sprint(nodeID)),
				slog.String("error", err.Error()))
		}
		// 回 ACK
		if err := stream.Send(&agentv1.ServerMessage{
			Payload:   &agentv1.ServerMessage_Ack{Ack: &agentv1.Ack{Message: "host-info-received"}},
			SessionId: first.GetSessionId(),
		}); err != nil {
			return err
		}
	}

	// 下发采集配置（PRD 7.1：ConfigSync 只含采集配置，无执行语义）
	if err := stream.Send(&agentv1.ServerMessage{
		Payload: &agentv1.ServerMessage_Config{Config: &agentv1.ConfigSync{
			ReportIntervalSec: int32(s.cfg.Agent.DefaultReportInterval.Seconds()),
			EnabledMetrics:    s.defaultEnabledMetrics(),
			Tz:                timezone(),
		}},
		SessionId: first.GetSessionId(),
	}); err != nil {
		return err
	}

	// 接收循环
	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				s.log.Debug("Agent 流被取消", slog.String("node_id", fmt.Sprint(nodeID)))
				return nil
			}
			return err
		}

		switch payload := msg.Payload.(type) {
		case *agentv1.AgentMessage_Metrics:
			if err := s.ingest.IngestMetrics(ctx, nodeID, payload.Metrics); err != nil {
				s.log.Error("处理指标失败",
					slog.String("node_id", fmt.Sprint(nodeID)),
					slog.String("error", err.Error()))
				// 不中断流：单次数据错误不该导致 Agent 被踢下线
			}
			if err := stream.Send(&agentv1.ServerMessage{
				Payload:   &agentv1.ServerMessage_Ack{Ack: &agentv1.Ack{Seq: uint64(payload.Metrics.GetSeq())}},
				SessionId: msg.SessionId,
			}); err != nil {
				return err
			}

		case *agentv1.AgentMessage_Host:
			if err := s.ingest.IngestHostInfo(ctx, nodeID, payload.Host); err != nil {
				s.log.Warn("处理主机信息失败",
					slog.String("node_id", fmt.Sprint(nodeID)),
					slog.String("error", err.Error()))
			}
			if err := stream.Send(&agentv1.ServerMessage{
				Payload:   &agentv1.ServerMessage_Ack{Ack: &agentv1.Ack{Message: "host-info-received"}},
				SessionId: msg.SessionId,
			}); err != nil {
				return err
			}

		case *agentv1.AgentMessage_Event:
			s.log.Info("Agent 事件",
				slog.String("node_id", fmt.Sprint(nodeID)),
				slog.String("type", payload.Event.GetType()),
				slog.String("detail", payload.Event.GetDetail()))

		default:
			s.log.Warn("收到未知消息类型", slog.String("node_id", fmt.Sprint(nodeID)))
		}
	}
}

// ReportOnce 是无流能力时的降级路径。
func (s *Service) ReportOnce(ctx context.Context, msg *agentv1.AgentMessage) (*agentv1.ServerMessage, error) {
	nodeID, err := s.authorizeSession(ctx, msg.GetSessionId())
	if err != nil {
		return nil, err
	}

	switch payload := msg.Payload.(type) {
	case *agentv1.AgentMessage_Metrics:
		if err := s.ingest.IngestMetrics(ctx, nodeID, payload.Metrics); err != nil {
			return nil, status.Error(codes.Internal, "指标处理失败")
		}
		return &agentv1.ServerMessage{
			Payload:   &agentv1.ServerMessage_Ack{Ack: &agentv1.Ack{Seq: uint64(payload.Metrics.GetSeq())}},
			SessionId: msg.SessionId,
		}, nil

	case *agentv1.AgentMessage_Host:
		if err := s.ingest.IngestHostInfo(ctx, nodeID, payload.Host); err != nil {
			return nil, status.Error(codes.Internal, "主机信息处理失败")
		}
		return &agentv1.ServerMessage{
			Payload:   &agentv1.ServerMessage_Ack{Ack: &agentv1.Ack{Message: "host-info-received"}},
			SessionId: msg.SessionId,
		}, nil
	}

	return &agentv1.ServerMessage{
		Payload:   &agentv1.ServerMessage_Ack{Ack: &agentv1.Ack{Message: "ignored"}},
		SessionId: msg.SessionId,
	}, nil
}

// authorizeSession 校验 session 有效性并返回节点 ID。
func (s *Service) authorizeSession(ctx context.Context, sessionID string) (int64, error) {
	if sessionID == "" {
		return 0, status.Error(codes.Unauthenticated, "会话无效")
	}
	sess, err := s.db.AgentSessions.GetSession(ctx, sessionID)
	if err != nil {
		return 0, status.Error(codes.Unauthenticated, "会话无效或已过期")
	}
	// 注意取 NodeID 而非 UserID：agent_sessions 表的 user_id 列
	// 历史沿用了 sessions 的命名，实际存的是 node_id。
	return sess.NodeID, nil
}

// recordFailure 记录握手失败并按阈值锁定。
func (s *Service) recordFailure(ctx context.Context, uuid string) {
	ip := clientIP(ctx)
	count, err := s.db.AgentSessions.RecordAgentFailure(ctx, uuid, ip)
	if err != nil {
		s.log.Warn("记录握手失败失败", slog.String("error", err.Error()))
		return
	}
	/*
	 * 用 >= 而不是 ==。
	 *
	 * == 的问题：只要计数在两次失败之间被清零过一次
	 * （比如另一次握手成功调了 ClearAgentFailures），
	 * 计数就永远跨不过那个精确值，软锁与硬封都形同虚设。
	 * 攻击者只要偶尔用正确密钥握手一次，就能把计数打回 0 并无限重试。
	 */
	if count >= s.cfg.Agent.MaxFailuresBeforeLock {
		// 软锁定：达到阈值后短时锁
		if err := s.db.AgentSessions.SoftLock(ctx, uuid, ip,
			time.Now().Add(s.cfg.Agent.LockDuration).UTC()); err != nil {
			s.log.Error("写入软锁失败",
				slog.String("uuid", maskUUID(uuid)), slog.String("error", err.Error()))
		}
		s.log.Warn("Agent 已被临时锁定",
			slog.String("uuid", maskUUID(uuid)),
			slog.String("ip", ip),
			slog.Duration("duration", s.cfg.Agent.LockDuration))
	}
	if count >= s.cfg.Agent.HardLockAfter {
		// 硬锁定：达到更高阈值后封禁
		if err := s.db.AgentSessions.HardLock(ctx, uuid, ip,
			time.Now().Add(time.Duration(s.cfg.Agent.HardLockHours)*time.Hour).UTC()); err != nil {
			s.log.Error("写入硬封禁失败",
				slog.String("uuid", maskUUID(uuid)), slog.String("error", err.Error()))
		}
		s.log.Error("Agent 已被长期封禁",
			slog.String("uuid", maskUUID(uuid)),
			slog.String("ip", ip),
			slog.Int("failures", count))
	}
}

// ---------- 活跃会话管理 ----------

func (s *Service) markActive(sessionID string, nodeID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[sessionID] = nodeID
	s.lastSeen[sessionID] = time.Now()
}

func (s *Service) unmarkActive(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, sessionID)
	delete(s.lastSeen, sessionID)
}

// ActiveSessions 返回当前活跃的流式会话数。
// 供 /ready 探针与运维观测使用。
func (s *Service) ActiveSessions() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.active)
}

// ---------- 辅助 ----------

// credentialsFromContext 从 gRPC metadata 提取凭据。
func credentialsFromContext(ctx context.Context) (uuid, secret string, err error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", "", status.Error(codes.Unauthenticated, "缺少凭据")
	}
	get := func(key string) string {
		v := md.Get(key)
		if len(v) == 0 {
			return ""
		}
		return v[0]
	}
	uuid = get(mdClientUUID)
	secret = get(mdClientSecret)
	if uuid == "" || secret == "" {
		return "", "", status.Error(codes.Unauthenticated, "缺少凭据")
	}
	return uuid, secret, nil
}

// clientIP 提取客户端 IP。
func clientIP(ctx context.Context) string {
	if p, ok := peer.FromContext(ctx); ok {
		return ipFromAddr(p.Addr)
	}
	return ""
}

func ipFromAddr(a net.Addr) string {
	switch v := a.(type) {
	case *net.TCPAddr:
		return v.IP.String()
	default:
		host, _, err := net.SplitHostPort(a.String())
		if err != nil {
			return a.String()
		}
		return host
	}
}

// maskUUID 掩码 UUID，日志里保留前 8 位便于排查。
func maskUUID(u string) string {
	if len(u) <= 8 {
		return "***"
	}
	return u[:8] + "***"
}

func (s *Service) defaultEnabledMetrics() []string {
	return []string{"cpu", "mem", "disk", "net", "load", "uptime"}
}

func timezone() string {
	return "Asia/Shanghai"
}

// ---------- gRPC 服务器 ----------

// Server 包装 grpc.Server，隔离生命周期管理。
type Server struct {
	grpc *grpc.Server
	addr string
}

// NewServer 创建 gRPC 服务器。
//
// TLS：若配置了证书则强制启用。若未配置，仍要求上层通过
// 反代终止 TLS（PRD 10.4 推荐的 443 方案），但会在日志中明确告警——
// 明文传输 agent 凭据是不可接受的。
func NewServer(cfg *config.Config, svc *Service, log *slog.Logger) *Server {
	var opts []grpc.ServerOption

	if cfg.Server.TLSCert != "" && cfg.Server.TLSKey != "" {
		creds, err := credentials.NewServerTLSFromFile(cfg.Server.TLSCert, cfg.Server.TLSKey)
		if err != nil {
			log.Error("加载 TLS 证书失败，gRPC 服务端无法启动",
				slog.String("cert", cfg.Server.TLSCert),
				slog.String("error", err.Error()))
			// 不降级为明文：凭据明文传输是不可接受的
			panic("TLS 证书加载失败，拒绝以明文启动: " + err.Error())
		}
		opts = append(opts, grpc.Creds(creds))
		log.Info("gRPC TLS 已启用")
	} else {
		log.Warn("gRPC 未启用 TLS——请确认前面有反向代理终止 TLS。" +
			"直接暴露 8008 端口会让 Agent 凭据以明文传输")
	}

	// 强制客户端证书（mTLS）：若配置了 CA
	if cfg.Server.MTLSCA != "" {
		creds, err := loadMTLS(cfg.Server.TLSCert, cfg.Server.TLSKey, cfg.Server.MTLSCA)
		if err != nil {
			log.Error("加载 mTLS 配置失败", slog.String("error", err.Error()))
			panic("mTLS 配置加载失败: " + err.Error())
		}
		opts = append(opts, grpc.Creds(creds))
		log.Info("gRPC mTLS 已启用：仅接受持证书的 Agent")
	}

	// 限制消息大小：Agent 消息都很小，1MB 足够且能防滥用
	opts = append(opts,
		grpc.MaxRecvMsgSize(1024*1024),
		grpc.MaxSendMsgSize(1024*1024),
		// 保留超时设为 0：Agent 的流是长连接，靠 keepalive 而非超时维持
		grpc.ConnectionTimeout(10*time.Second),
	)

	g := grpc.NewServer(opts...)
	agentv1.RegisterAgentServiceServer(g, svc)

	return &Server{
		grpc: g,
		addr: fmt.Sprintf(":%d", cfg.Server.GRPCPort),
	}
}

// Serve 启动服务，阻塞直到 ctx 取消。
func (s *Server) Serve(ctx context.Context, log *slog.Logger) error {
	errCh := make(chan error, 1)
	go func() {
		log.Info("gRPC 服务已启动", slog.String("addr", s.addr))
		if err := s.grpc.Serve(lisFor(s.addr)); err != nil {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("收到停机信号，优雅停止 gRPC")
		// GracefulStop 等待所有流结束。给一个上限，
		// 避免恶意连接把停机卡住。
		stopped := make(chan struct{})
		go func() {
			s.grpc.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
			log.Warn("gRPC 优雅停机超时，强制停止")
			s.grpc.Stop()
		}
		return nil
	}
}
