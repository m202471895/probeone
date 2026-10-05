package reconnect

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestBackoff_指数增长并封顶(t *testing.T) {
	// 固定随机源让抖动可预测
	b := New(time.Second, 30*time.Second, WithRand(newFixedRand(0.5)))

	var got []time.Duration
	for i := 0; i < 8; i++ {
		d := b.Next()
		got = append(got, d)
	}

	// 抖动为 0.5 时是 ×1.0（1 + (0.5*0.4 - 0.2) = 1.0），无抖动
	want := []time.Duration{
		1 * time.Second,  // 1s  * 2^0
		2 * time.Second,  // 1s  * 2^1
		4 * time.Second,  // 1s  * 2^2
		8 * time.Second,  // 1s  * 2^3
		16 * time.Second, // 1s  * 2^4
		30 * time.Second, // 16s * 2^5 = 32s > 30s → 封顶
		30 * time.Second, // 封顶
		30 * time.Second, // 封顶
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 次退避 = %v，期望 %v", i+1, got[i], want[i])
		}
	}
}

func TestBackoff_抖动范围(t *testing.T) {
	b := New(time.Second, 30*time.Second)

	// 跑 200 次，统计分布。
	// 注意：每次循环后 Reset，让退避始终停在第 1 步（1s），
	// 否则中途会指数增长到 30s 封顶，那时校验的是封顶值而非基准值。
	counts := map[time.Duration]int{}
	for i := 0; i < 200; i++ {
		d := b.Next()
		b.Reset()
		if d < 800*time.Millisecond || d > 1200*time.Millisecond {
			t.Fatalf("第 %d 次退避 = %v，超出正负 20%% 抖动范围", i, d)
		}
		counts[d]++
	}
	// 抖动应真正打散，而不是固定值（否则惊群没被解决）
	if len(counts) < 50 {
		t.Errorf("抖动产生的不同取值仅 %d 种，未有效打散", len(counts))
	}
}

func TestBackoff_重置(t *testing.T) {
	b := New(time.Second, 30*time.Second, WithRand(newFixedRand(0.5)))
	b.Next()
	b.Next()
	b.Next()
	if b.Attempt() != 3 {
		t.Errorf("连续失败次数 = %d，期望 3", b.Attempt())
	}

	b.Reset()
	if b.Attempt() != 0 {
		t.Errorf("重置后失败次数 = %d，应为 0", b.Attempt())
	}
	// 重置后退避应回到 1s
	if d := b.Next(); d != time.Second {
		t.Errorf("重置后退避 = %v，期望 1s", d)
	}
}

func TestBackoff_MaybeReset(t *testing.T) {
	b := New(time.Second, 5*time.Second, WithRand(newFixedRand(0.5)))
	b.Next()
	b.Next()

	// 静默时间不足 → 不重置
	b.MaybeReset(2 * time.Second)
	if b.Attempt() != 2 {
		t.Errorf("静默不足时不应重置，实际 = %d", b.Attempt())
	}
	// 静默超过 resetAfter(=max=5s) → 重置
	b.MaybeReset(6 * time.Second)
	if b.Attempt() != 0 {
		t.Errorf("静默足够时应重置，实际 = %d", b.Attempt())
	}
}

func TestBackoff_ShouldGiveUp(t *testing.T) {
	// 默认无限重试（探针场景必须如此）
	b := New(time.Second, 30*time.Second)
	for i := 0; i < 100; i++ {
		b.Next()
	}
	if b.ShouldGiveUp() {
		t.Error("默认配置下应永不放弃")
	}

	// 显式设置上限
	b2 := New(time.Second, 30*time.Second, WithMaxAttempts(3))
	for i := 0; i < 3; i++ {
		if b2.ShouldGiveUp() {
			t.Errorf("第 %d 次后不应放弃", i+1)
		}
		b2.Next()
	}
	if !b2.ShouldGiveUp() {
		t.Error("达到 maxAttempts 后应放弃")
	}
}

func TestBackoff_参数兜底(t *testing.T) {
	// 非法参数不应 panic
	b := New(0, 0)
	if b.base != time.Second {
		t.Errorf("base 兜底值 = %v，期望 1s", b.base)
	}
	if b.max != b.base {
		t.Errorf("max 应不小于 base，实际 = %v < %v", b.max, b.base)
	}
	// max < base 的情况
	b2 := New(10*time.Second, time.Second)
	if b2.max != 10*time.Second {
		t.Errorf("max < base 时应取 base，实际 = %v", b2.max)
	}
}

// mockLogger 收集日志，用于验证限流策略。
type mockLogger struct {
	mu    sync.Mutex
	count int
}

func (l *mockLogger) Warn(msg string, args ...any) {
	l.mu.Lock()
	l.count++
	l.mu.Unlock()
}

func TestLoop_成功后返回(t *testing.T) {
	b := New(time.Millisecond, 5*time.Millisecond)
	calls := 0
	ok := Loop(context.Background(), b, nil, func(ctx context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("暂时失败")
		}
		return nil
	})
	if !ok {
		t.Error("第三次成功应返回 true")
	}
	if calls != 3 {
		t.Errorf("调用 %d 次，期望 3", calls)
	}
	if b.Attempt() != 0 {
		t.Error("成功后应重置退避")
	}
}

func TestLoop_上下文取消时退出(t *testing.T) {
	b := New(10*time.Millisecond, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	ok := Loop(ctx, b, nil, func(ctx context.Context) error {
		return errors.New("一直失败")
	})
	if ok {
		t.Error("上下文取消后不应返回 true")
	}
	// 应及时退出而不是等满重试次数
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("取消后退出过慢：%v", elapsed)
	}
}

func TestLoop_达到上限后放弃(t *testing.T) {
	b := New(time.Millisecond, 5*time.Millisecond, WithMaxAttempts(2))
	calls := 0
	ok := Loop(context.Background(), b, nil, func(ctx context.Context) error {
		calls++
		return errors.New("失败")
	})
	if ok {
		t.Error("达到上限应返回 false")
	}
	if calls != 2 {
		t.Errorf("调用 %d 次，期望 2", calls)
	}
}

func TestLoop_日志限流(t *testing.T) {
	// 服务端不可达时不能刷屏：前 3 次每次记，之后每 5 次记一条
	log := &mockLogger{}
	b := New(time.Microsecond, time.Microsecond, WithMaxAttempts(21))
	Loop(context.Background(), b, log, func(ctx context.Context) error {
		return errors.New("失败")
	})

	// 21 次失败 → 前 3 次 + 第5,10,15,20 次 = 7 条
	if log.count != 7 {
		t.Errorf("日志条数 = %d，期望 7（前3次 + 每5次一条）", log.count)
	}
}

func TestLoop_参数兜底不panic(t *testing.T) {
	b := New(0, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	Loop(ctx, b, nil, func(ctx context.Context) error {
		return errors.New("x")
	})
}

// fixedRand 是确定性随机源，rand.Float64() 恒返回给定值。
type fixedRand struct {
	mu sync.Mutex
	v  float64
}

func newFixedRand(v float64) *fixedRand { return &fixedRand{v: v} }

func (r *fixedRand) Float64() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.v
}

func (r *fixedRand) Int63() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return int64(r.v * 1e9)
}

func (r *fixedRand) Seed(int64) {}
