// Package reconnect 实现断线重连的指数退避。
//
// 退避曲线：1s → 2s → 4s → 8s → 16s → 30s（封顶），带 ±20% 抖动。
//
// 为什么要抖动：所有 Agent 会在同一时刻断网（服务端重启、网络抖动），
// 无抖动的指数退避会让它们在同一毫秒一起重连，把刚恢复的服务端再次打垮。
// 抖动把它们打散，避免惊群。
//
// 日志策略：服务端不可达时不刷日志。
// Agent 默认每 10 秒上报一次，无脑重连会每 10 秒刷一条 ERROR，
// 日志文件一天涨几十 MB，真正需要排查时反而找不到线索。
package reconnect

import (
	"context"
	"math"
	"math/rand"
	"sync"
	"time"
)

// Backoff 是指数退避计算器。
type Backoff struct {
	mu sync.Mutex

	base    time.Duration
	max     time.Duration
	attempt int

	// rng 抖动随机源。用接口而非 *rand.Rand 是为了测试能注入确定性序列，
	// 验证退避时长分布正确。
	rng randSource

	// resetAfter 超过这个时间没有失败就重置退避。
	// 默认与 max 相同：稳定连接维持 30 秒就算"已恢复"。
	resetAfter time.Duration

	// onGiveUp 在达到最大重试次数时调用。0 表示无限重试。
	// 探针场景应当无限重试——Agent 的职责就是一直盯着。
	maxAttempts int
}

// Option 是构造选项。
type Option func(*Backoff)

// randSource 是抖动随机源的最小接口。
type randSource interface {
	Float64() float64
}

// WithRand 注入随机源（测试用）。
func WithRand(r randSource) Option {
	return func(b *Backoff) { b.rng = r }
}

// WithMaxAttempts 设置最大重试次数，0 为无限。
func WithMaxAttempts(n int) Option {
	return func(b *Backoff) { b.maxAttempts = n }
}

// WithResetAfter 设置退避重置的静默时长。
func WithResetAfter(d time.Duration) Option {
	return func(b *Backoff) { b.resetAfter = d }
}

// New 创建退避计算器。
func New(base, max time.Duration, opts ...Option) *Backoff {
	if base <= 0 {
		base = time.Second
	}
	if max < base {
		max = base
	}
	b := &Backoff{
		base:       base,
		max:        max,
		rng:        rand.New(rand.NewSource(time.Now().UnixNano())),
		resetAfter: max,
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Next 返回下一次等待时长，并递增尝试计数。
func (b *Backoff) Next() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.attempt++

	// 计算基础时长：base * 2^(attempt-1)，用浮点避免位移溢出
	exp := math.Pow(2, float64(b.attempt-1))
	d := time.Duration(float64(b.base) * exp)
	if d > b.max || d <= 0 {
		d = b.max
	}

	// ±20% 抖动，打散惊群
	jitter := 1 + (b.rng.Float64()*0.4 - 0.2) // [0.8, 1.2]
	result := time.Duration(float64(d) * jitter)
	if result < 0 {
		result = b.base
	}
	return result
}

// Reset 重置退避。连接成功或稳定运行后调用。
func (b *Backoff) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.attempt = 0
}

// Attempt 返回当前连续失败次数。
func (b *Backoff) Attempt() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.attempt
}

// ShouldGiveUp 返回是否应放弃重试。
// maxAttempts 为 0 时永不放弃。
func (b *Backoff) ShouldGiveUp() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.maxAttempts <= 0 {
		return false
	}
	return b.attempt >= b.maxAttempts
}

// MaybeReset 若距上次失败已超过 resetAfter 则重置。
// 场景：连接成功但很快又断，应该从 1s 重新开始而不是从 30s。
func (b *Backoff) MaybeReset(sinceLastFailure time.Duration) {
	if sinceLastFailure >= b.resetAfter {
		b.Reset()
	}
}

// Loop 是重连循环。
//
// 它反复调用 attempt 函数（通常是建立 gRPC 连接），
// 失败则按退避等待后重试，成功则重置退避并返回 true。
//
// 这个函数会阻塞直到连接成功或 ctx 被取消。
func Loop(ctx context.Context, b *Backoff, log Logger, attempt func(context.Context) error) bool {
	for {
		// 尝试前先检查是否已达上限。
		// 顺序很关键：若先 attempt 再检查，实际调用次数会是 maxAttempts+1，
		// 也就是"允许的上限"之外还多试了一次。
		if b.ShouldGiveUp() {
			return false
		}

		err := attempt(ctx)
		if err == nil {
			b.Reset()
			return true
		}

		wait := b.Next()

		// 日志限流：前 3 次每次都记（便于排查启动问题），
		// 之后每 5 次才记一条，避免刷屏
		n := b.Attempt()
		if log != nil && (n <= 3 || n%5 == 0) {
			log.Warn("连接服务端失败，将重试",
				"attempt", n,
				"retry_in", wait.String(),
				"error", err.Error())
		}

		select {
		case <-ctx.Done():
			return false
		case <-time.After(wait):
		}
	}
}

// Logger 是重连循环需要的最小日志接口。
// 用接口而非 *slog.Logger 便于测试注入。
type Logger interface {
	Warn(msg string, args ...any)
}
