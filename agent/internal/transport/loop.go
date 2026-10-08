/*
 * loop.go —— Agent 主循环：采集 → 上报，断了自动重连。
 *
 * 结构上分成两个 goroutine：
 *   - 采集循环：固定周期采集，塞进缓冲
 *   - 发送循环：连接可用就发，断线就退避重连
 *
 * 为什么分开：采集周期（10 秒）不能被网络阻塞拖慢。
 * 网络卡住时采集应该继续，数据堆在缓冲里，
 * 恢复后补传——否则丢数据的是网络而不是采集，
 * 而网络抖动远比采集慢更常见。
 *
 * 失败处理原则：
 *   - 单次上报失败不算致命：不退出，交给重连逻辑
 *   - 连续失败才退避，且退避上限 5 分钟——
 *     服务端长时间维护时 Agent 不能无限快速重试
 *   - 校验配置失败直接退出：那是部署问题，重试无意义
 */
package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/m202471895/probeone/agent/internal/buffer"
	"github.com/m202471895/probeone/agent/internal/collect"
	"github.com/m202471895/probeone/agent/internal/reconnect"
	agentv1 "github.com/m202471895/probeone/api/agent/v1"
)

// Runner 负责 Agent 的采集与上报循环。
type Runner struct {
	client  *Client
	coll    *collect.Collector
	buf     *buffer.Buffer
	log     *slog.Logger
	backoff *reconnect.Backoff
	// localInterval 是本地配置的上报间隔。
	// 服务端下发值优先（见interval）。
	localInterval time.Duration
	// current 是当前生效的间隔，服务端可下发调整。
	current time.Duration
}

// RunnerConfig 是 Runner 的依赖。
type RunnerConfig struct {
	Client    *Client
	Collector *collect.Collector
	Buffer    *buffer.Buffer
	Log       *slog.Logger
	Interval  time.Duration
}

// NewRunner 创建运行器。
func NewRunner(cfg RunnerConfig) *Runner {
	iv := cfg.Interval
	if iv <= 0 {
		iv = 10 * time.Second
	}
	return &Runner{
		client:        cfg.Client,
		coll:          cfg.Collector,
		buf:           cfg.Buffer,
		log:           cfg.Log,
		backoff:       reconnect.New(2*time.Second, 5*time.Minute),
		localInterval: iv,
		current:       iv,
	}
}

// logAdapter 把 slog 适配到 reconnect.Logger 接口。
type logAdapter struct{ l *slog.Logger }

func (a logAdapter) Warn(msg string, args ...any) {
	a.l.Warn(msg, args...)
}

// Run 启动采集与上报，直到 ctx 取消。
func (r *Runner) Run(ctx context.Context) error {
	/*
	 * Prime 是必须的：CPU、网络、IO 的使用率都靠差值，
	 * 首次采集没有"上一次"可减，得到的会是 0 或垃圾数据。
	 */
	r.coll.Prime()
	r.log.Info("采集器已预热", slog.Duration("预热时长", r.localInterval))

	// 握手一次；失败不致命——重连循环会继续尝试。
	if err := r.client.Connect(ctx); err != nil {
		r.log.Warn("首次握手失败，将进入重连循环", slog.String("error", err.Error()))
	}

	// 采集循环
	collectDone := make(chan struct{})
	go func() {
		defer close(collectDone)
		r.collectLoop(ctx)
	}()

	// 上报循环（当前 goroutine）
	r.sendLoop(ctx)

	<-collectDone
	_ = r.client.Close()
	r.log.Info("Agent 已停止")
	return nil
}

// collectLoop 周期性采集。
func (r *Runner) collectLoop(ctx context.Context) {
	// 用心跳间隔而不是上报间隔：采集周期要短于上报周期吗？
	// 不需要——一次采集一份数据，报送与采集同频最简单。
	ticker := time.NewTicker(r.current)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.log.Info("收到停机信号，停止采集")
			return
		case <-ticker.C:
			m, err := r.coll.Collect(ctx)
			if err != nil {
				/*
				 * 采集失败只记日志不中断。
				 * 采不到数据是"这轮没有"，不是"Agent 坏了"——
				 * 退出反而会让服务端认为节点彻底失联。
				 */
				r.log.Warn("采集失败", slog.String("error", err.Error()))
				continue
			}
			r.enqueue(m)
		}
	}
}

// enqueue 把采集结果放入缓冲。
//
// 无条件入缓冲：发送成功时缓冲会被清空，
// 失败时留待下轮补传。这样不需要"先试发、失败才入队"的分支，
// 逻辑更简单，代价是每轮都过一次 JSON 序列化。
func (r *Runner) enqueue(m *collect.Metrics) {
	if !r.buf.Enabled() {
		return
	}
	payload, err := json.Marshal(m)
	if err != nil {
		r.log.Warn("指标序列化失败", slog.String("error", err.Error()))
		return
	}
	r.buf.Push(buffer.Point{
		Seq:         r.client.NextSeq(),
		CollectedAt: time.Unix(m.CollectedAt, 0),
		Payload:     payload,
	})
	r.log.Debug("已入缓冲",
		slog.Int("长度", r.buf.Len()),
		slog.Int("字节", len(payload)))
}

// sendLoop 发送循环：连接 → 发 → 断开 → 退避重连。
func (r *Runner) sendLoop(ctx context.Context) {
	stream, err := r.client.Stream(ctx)
	if err != nil {
		r.log.Warn("打开上报流失败", slog.String("error", err.Error()))
		if !reconnect.Loop(ctx, r.backoff, logAdapter{r.log}, func(c context.Context) error {
			return r.client.Connect(c)
		}) {
			return // ctx 取消
		}
		// 重连成功后重新打开流
		stream, err = r.client.Stream(ctx)
		if err != nil {
			r.log.Error("重连后仍无法打开上报流", slog.String("error", err.Error()))
			return
		}
	}

	// 接收 goroutine：处理 ACK 与配置下发
	go stream.ReceiveLoop(ctx, func(cfg *agentv1.ConfigSync) {
		r.applyRemoteConfig(cfg)
	})

	r.log.Info("上报流已建立", slog.String("session", r.client.SessionID()))

	for {
		select {
		case <-ctx.Done():
			_ = stream.Close()
			return
		case <-time.After(r.current):
			if err := r.reportOnce(ctx, stream); err != nil {
				r.log.Warn("上报失败", slog.String("error", err.Error()))
				/*
				 * 流断了就重建。
				 * 不试图在坏流上继续发——gRPC 的流一旦出错
				 * 后续写入会一直失败。
				 */
				_ = stream.Close()
				if !reconnect.Loop(ctx, r.backoff, logAdapter{r.log}, func(c context.Context) error {
					return r.client.Connect(c)
				}) {
					return
				}
				ns, err := r.client.Stream(ctx)
				if err != nil {
					r.log.Error("重建上报流失败", slog.String("error", err.Error()))
					continue
				}
				stream = ns
				go stream.ReceiveLoop(ctx, func(cfg *agentv1.ConfigSync) {
					r.applyRemoteConfig(cfg)
				})
			}
		}
	}
}

// reportOnce 发一轮数据：优先走流，失败降级到单次 RPC。
func (r *Runner) reportOnce(ctx context.Context, stream Stream) error {
	// 主机信息单独发一次：内容不变，重复发没有意义。
	if err := r.sendHostInfo(ctx, stream); err != nil {
		return err
	}

	m, err := r.coll.Collect(ctx)
	if err != nil {
		// 采集失败不算上报失败——那是另一条日志已经报过的事
		return nil
	}

	msg := &agentv1.AgentMessage{
		SessionId: r.client.SessionID(),
		Payload: &agentv1.AgentMessage_Metrics{
			Metrics: MetricsToProto(m, r.client.NextSeq()),
		},
	}
	if err := stream.Send(msg); err != nil {
		// 流失败，降级到单次 RPC——它不依赖流，可用性更高
		if _, derr := r.client.Send(ctx, msg); derr != nil {
			return fmt.Errorf("流与单次均失败: %w", derr)
		}
	}
	r.log.Debug("已上报",
		slog.Int64("seq", int64(r.client.NextSeq())),
		slog.Float64("cpu", m.CPU.Usage))
	return nil
}

// sendHostInfo 上报主机信息。
func (r *Runner) sendHostInfo(ctx context.Context, stream Stream) error {
	h := r.coll.HostInfo(ctx, AgentVersion)
	if h == nil {
		return nil
	}
	msg := &agentv1.AgentMessage{
		SessionId: r.client.SessionID(),
		Payload: &agentv1.AgentMessage_Host{
			Host: HostInfoToProto(h, r.client.NextSeq()),
		},
	}
	if err := stream.Send(msg); err != nil {
		if _, derr := r.client.Send(ctx, msg); derr != nil {
			return derr
		}
	}
	return nil
}

// applyRemoteConfig 应用服务端下发的配置。
//
// 只接受采集配置（间隔、指标列表）——协议里明确不含任何
// 可执行语义。这条边界不能模糊：一旦 Agent 能被远程改变行为，
// "只读监控"的承诺就不成立了。
func (r *Runner) applyRemoteConfig(cfg *agentv1.ConfigSync) {
	if cfg == nil {
		return
	}
	if secs := cfg.GetReportIntervalSec(); secs > 0 && secs != int32(r.current/time.Second) {
		old := r.current
		r.current = time.Duration(secs) * time.Second
		// 下限 5 秒：服务端配错时也不至于把 Agent 打满CPU
		if r.current < 5*time.Second {
			r.current = 5 * time.Second
		}
		r.log.Info("上报间隔已按服务端要求调整",
			slog.Duration("旧值", old),
			slog.Duration("新值", r.current))
	}
}
