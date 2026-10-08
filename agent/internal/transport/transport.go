// Package transport 实现 Agent 与服务端的 gRPC 连接。
//
// 职责边界：只管"怎么连、怎么发、怎么断"，
// 不负责采集什么（那是 collect 的事），也不负责何时采
// （那是 run 循环的事）。这样分层后重连逻辑可以独立测试。
//
// 安全约束：Agent 是只读的，本包不含任何执行外部命令的调用。
package transport

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentv1 "github.com/m202471895/probeone/api/agent/v1"
)

// AgentVersion 由构建时注入（build.sh 传 -X）。
var AgentVersion = "dev"

// Credentials 是节点身份凭据。
type Credentials struct {
	UUID   string
	Secret string
}

/*
 * Client 是与服务端的 gRPC 连接。
 *
 * 生命周期：New → Connect → （握手）→ 循环收发 → Close。
 * 断线后由调用方通过 reconnect.Loop 重连——
 * 连接逻辑与重连策略分开，前者测"能不能连上"，
 * 后者测"断了多久重试一次"。
 */
type Client struct {
	addr  string
	creds Credentials
	log   *slog.Logger

	conn   *grpc.ClientConn
	client agentv1.AgentServiceClient

	sessionID string
	// serverInterval 是服务端下发的上报间隔（秒）。
	// 为 0 表示服务端没指定，用本地配置。
	serverInterval int32

	// seq 是上报序号，服务端用它检测丢包。
	// 单调递增，不因重连重置——重连后继续从上次序号走，
	// 服务端能发现中间的缺口。
	seq int32

	mu     sync.Mutex
	closed bool
}

// New 创建客户端（不建立连接）。
func New(addr string, creds Credentials, log *slog.Logger) *Client {
	return &Client{addr: addr, creds: creds, log: log}
}

// Connect 建立连接并完成握手。
//
// 握手是必须的：服务端在 Handshake 里分配 session_id，
// 后续所有上报都要带它。凭据通过 metadata 传递
// 而不是消息体——这样即使抓包也不会看到密钥。
func (c *Client) Connect(ctx context.Context) error {
	/*
	 * 用 NewClient 而不是 DialContext：前者是惰性连接，
	 * 不会阻塞在这里。真正建连在下面显式调 Handshake 时发生——
	 * 这样"连不上"表现为握手失败，而不是一个看不出原因的 Dial 错误。
	 */
	conn, err := grpc.NewClient(c.addr,
		// 当前服务端未启用 TLS（启动日志会警告），
		// 因此用 insecure。部署时若在反代后终止 TLS，
		// 应改成 credentials.NewTLS —— Agent 侧配置已预留该字段。
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// Agent 每 10 秒上报一次，keepalive 设成 30 秒足够；
		// 设太长会掩盖真正的断线，设太短在弱网下会反复重连。
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			// 每 20 秒发一次 ping。Agent 每 10 秒上报一次，
			// keepalive 间隔必须大于上报间隔，否则服务端会被 ping 淹没。
			Time: 20 * time.Second,
			// 允许 10 秒的延迟抖动。弱网下太严格会误判断线。
			Timeout: 10 * time.Second,
			// 失败后不立刻重连：交给外层 reconnect.Loop 的退避策略，
			// 两层退避叠在一起会让恢复时间长得离谱。
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		return fmt.Errorf("建立连接失败: %w", err)
	}

	c.mu.Lock()
	c.conn = conn
	c.client = agentv1.NewAgentServiceClient(conn)
	c.mu.Unlock()

	return c.handshake(ctx)
}

// handshake 执行一次握手并保存 session_id。
func (c *Client) handshake(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ctx = c.withCredentials(ctx)

	resp, err := c.client.Handshake(ctx, &agentv1.HandshakeRequest{
		AgentVersion: AgentVersion,
		OsType:       runtime.GOOS,
		Arch:         runtime.GOARCH,
	})
	if err != nil {
		return fmt.Errorf("握手失败: %w", err)
	}

	c.mu.Lock()
	c.sessionID = resp.GetSessionId()
	c.mu.Unlock()

	if !resp.GetSuccess() {
		return fmt.Errorf("握手被拒绝: %s", resp.GetMessage())
	}
	c.mu.Lock()
	c.serverInterval = resp.GetReportIntervalSec()
	c.mu.Unlock()

	c.log.Info("握手成功",
		slog.String("server", c.addr),
		slog.String("session", resp.GetSessionId()),
		slog.Int("上报间隔", int(resp.GetReportIntervalSec())))
	return nil
}

// SessionID 返回当前会话 ID。
func (c *Client) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

// NextSeq 返回下一个上报序号。
func (c *Client) NextSeq() int32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	return c.seq
}

/*
 * withCredentials 把凭据放进 metadata。
 *
 * 不用消息体传：metadata 在 HTTP/2 层，标准日志与抓包工具
 * 通常不会记录它，而消息体内容可能被业务日志打出来。
 */
func (c *Client) withCredentials(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx,
		"client-uuid", c.creds.UUID,
		"client-secret", c.creds.Secret,
	)
}

// Send 上报一条消息（走 ReportOnce 单次路径）。
//
// 单次路径存在的意义：流不可用时（服务端版本旧、网络限制双向流）
// 仍能上报数据。代价是每条都要一个 RPC，效率低于流。
func (c *Client) Send(ctx context.Context, msg *agentv1.AgentMessage) (*agentv1.ServerMessage, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()

	if cli == nil {
		return nil, fmt.Errorf("连接未建立")
	}
	// 每条都带凭据：服务端按metadata 认证，不依赖连接时的身份。
	return cli.ReportOnce(c.withCredentials(ctx), msg)
}

/*
 * Stream 打开双向流并持续收发。
 *
 * 为什么要流：Agent 每 10 秒一条，单次 RPC 的开销（建连、握手）
 * 与采集成本不成比例；而流让服务端能主动下发 ConfigSync。
 *
 * send 会阻塞直到服务端读到——这是有意的背压：
 * 服务端处理不过来时，Agent 自然减速而不是堆积内存。
 */
func (c *Client) Stream(ctx context.Context) (Stream, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil {
		return Stream{}, fmt.Errorf("连接未建立")
	}

	s, err := cli.ReportStream(c.withCredentials(ctx))
	if err != nil {
		return Stream{}, fmt.Errorf("打开上报流失败: %w", err)
	}
	return Stream{stream: s, client: c}, nil
}

// Stream 是一条上报流。
type Stream struct {
	stream agentv1.AgentService_ReportStreamClient
	client *Client
}

// Send 通过流发送一条消息。
func (s Stream) Send(msg *agentv1.AgentMessage) error {
	return s.stream.Send(msg)
}

// Recv 接收服务端消息（ACK 或配置下发）。
func (s Stream) Recv() (*agentv1.ServerMessage, error) {
	return s.stream.Recv()
}

// Close 关闭底层流。
func (s Stream) Close() error { return s.stream.CloseSend() }

/*
 * ReceiveLoop 持续接收服务端消息，直到流断开。
 *
 * 单独放一个 goroutine 的理由：发送是周期性的、接收是被动的，
 * 混在一个循环里会出现"正在采集时阻塞等 ACK"的假死。
 * ACK 本身不需要逐条处理（只记日志），配置下发才需要动。
 */
func (s Stream) ReceiveLoop(ctx context.Context, onConfig func(*agentv1.ConfigSync)) {
	log := s.client.log
	for {
		msg, err := s.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return // 正常退出
			}
			if isStreamClosed(err) {
				log.Debug("上报流已关闭")
				return
			}
			log.Warn("接收服务端消息失败", slog.String("error", err.Error()))
			return
		}
		if ack := msg.GetAck(); ack != nil {
			log.Debug("收到 ACK", slog.Uint64("seq", ack.GetSeq()))
			continue
		}
		if cfg := msg.GetConfig(); cfg != nil && onConfig != nil {
			log.Info("收到服务端配置下发",
				slog.Int("interval_sec", int(cfg.GetReportIntervalSec())),
				slog.Any("metrics", cfg.GetEnabledMetrics()))
			onConfig(cfg)
		}
	}
}

// Close 关闭连接。
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.conn == nil {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}

// IsStreamClosed 判断错误是否为流的正常关闭。
//
// gRPC 把它映射成 codes.Canceled 或 Unavailable，
// 两者都可能是网络抖动而非真的关闭——重连逻辑会兜住。
func isStreamClosed(err error) bool {
	s, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch s.Code() {
	case 0, 1, 13, 14: // OK, Canceled, Unavailable, DeadlineExceeded
		return true
	}
	return false
}
