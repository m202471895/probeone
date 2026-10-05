package buffer

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// mkPoint 构造一个测试用采样点。
func mkPoint(seq int32, size int) Point {
	return Point{
		Seq:         seq,
		CollectedAt: time.Now().Add(time.Duration(-int(seq)) * time.Second),
		Payload:     make([]byte, size),
	}
}

func TestBuffer_关闭时不占内存(t *testing.T) {
	b := New(false, 100, 1024*1024)
	// 关闭状态下所有操作都应是空操作，不 panic、不占内存
	for i := 0; i < 1000; i++ {
		b.Push(mkPoint(int32(i), 1024))
	}
	if b.Len() != 0 {
		t.Errorf("关闭状态下 Len 应为 0，实际 = %d", b.Len())
	}
	if got := b.Drain(10); got != nil {
		t.Errorf("关闭状态下 Drain 应返回 nil")
	}
	if b.Enabled() {
		t.Error("Enabled 应为 false")
	}
}

func TestBuffer_基本进出(t *testing.T) {
	b := New(true, 10, 1024*1024)

	for i := 1; i <= 3; i++ {
		b.Push(mkPoint(int32(i), 100))
	}
	if b.Len() != 3 {
		t.Fatalf("积压 = %d，期望 3", b.Len())
	}

	// 取出最早的
	got := b.Drain(2)
	if len(got) != 2 {
		t.Fatalf("取出 %d 个，期望 2", len(got))
	}
	// 顺序必须保持：先取到的应是 seq=1
	if got[0].Seq != 1 || got[1].Seq != 2 {
		t.Errorf("顺序错误: seq=%d, %d，期望 1, 2", got[0].Seq, got[1].Seq)
	}
	if b.Len() != 1 {
		t.Errorf("剩余 = %d，期望 1", b.Len())
	}
	// 再取应是 seq=3
	got = b.Drain(1)
	if len(got) != 1 || got[0].Seq != 3 {
		t.Errorf("剩余点 seq = %v，期望 3", got)
	}
	if b.Len() != 0 {
		t.Error("应已取空")
	}
}

func TestBuffer_满时丢最旧(t *testing.T) {
	b := New(true, 3, 1024*1024)
	for i := 1; i <= 5; i++ {
		b.Push(mkPoint(int32(i), 100))
	}
	if b.Len() != 3 {
		t.Errorf("积压 = %d，期望 3（受容量限制）", b.Len())
	}
	// 保留的是最近的 3 个
	got := b.Drain(10)
	if len(got) != 3 {
		t.Fatalf("取出 %d 个，期望 3", len(got))
	}
	if got[0].Seq != 3 || got[1].Seq != 4 || got[2].Seq != 5 {
		t.Errorf("应保留最近的点，实际 seq = %d, %d, %d", got[0].Seq, got[1].Seq, got[2].Seq)
	}
	// 丢弃计数
	if s := b.Stats(); s.Dropped != 2 {
		t.Errorf("丢弃计数 = %d，期望 2", s.Dropped)
	}
}

func TestBuffer_字节上限(t *testing.T) {
	// 每点 1KB，上限 3KB → 最多存 3 个
	b := New(true, 1000, 3*1024)
	for i := 1; i <= 10; i++ {
		b.Push(mkPoint(int32(i), 1024))
	}
	s := b.Stats()
	if s.Bytes > s.MaxBytes {
		t.Errorf("积压字节 = %d，超过上限 %d", s.Bytes, s.MaxBytes)
	}
	if s.Points != 2 {
		// Point.Size() = 24 + 1024 = 1048，3 个会超 3072
		t.Logf("实际存了 %d 个点（每点 %d 字节）", s.Points, 1048)
	}
	if s.Dropped == 0 {
		t.Error("应有丢弃计数")
	}
}

func TestBuffer_单点超限保留(t *testing.T) {
	// 单个点超过 maxBytes 时仍必须保留，否则采集循环会陷入
	// "写进去→立刻被自己挤掉"的死循环，数据永远发不出去。
	// 这是有意的例外：宁可短暂超限，也不能丢最新的数据。
	b := New(true, 10, 64*1024)
	huge := mkPoint(1, 100*1024) // 100KB > 64KB 上限
	b.Push(huge)
	if b.Len() != 1 {
		t.Errorf("超大点应被保留，积压 = %d", b.Len())
	}
	// 超限后写入正常大小的点，最旧的（那个超大的）应被挤掉
	b.Push(mkPoint(2, 100))
	got := b.Drain(10)
	if len(got) != 1 {
		t.Fatalf("积压 = %d，期望 1（超大的点应被挤掉）", len(got))
	}
	if got[0].Seq != 2 {
		t.Errorf("保留的应是最新点 seq=2，实际 = %d", got[0].Seq)
	}
}

func TestBuffer_并发安全(t *testing.T) {
	// Agent 的采集循环与补传循环是两个 goroutine，必须并发安全。
	// 早期用 channel 实现时这里崩过：采集与补传同时进行导致 channel 关闭竞争。
	b := New(true, 1000, 1024*1024)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 采集侧
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			b.Push(mkPoint(int32(i), 100))
		}
	}()

	// 补传侧
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			b.Drain(3)
		}
	}()

	// 观测侧
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = b.Stats()
			_, _ = b.Peek()
		}
	}()

	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()

	// 跑通即可；有竞态的话 -race 会报出来
}

func TestBuffer_Peek不移除(t *testing.T) {
	b := New(true, 10, 1024*1024)
	b.Push(mkPoint(7, 100))
	p1, ok := b.Peek()
	if !ok {
		t.Fatal("Peek 应返回数据")
	}
	if p1.Seq != 7 {
		t.Errorf("seq = %d，期望 7", p1.Seq)
	}
	if b.Len() != 1 {
		t.Error("Peek 不应移除元素")
	}
	_, _ = b.Peek()
	if b.Len() != 1 {
		t.Error("重复 Peek 后元素仍应存在")
	}
}

func TestBuffer_Clear(t *testing.T) {
	b := New(true, 10, 1024*1024)
	for i := 0; i < 5; i++ {
		b.Push(mkPoint(int32(i), 100))
	}
	b.Clear()
	if b.Len() != 0 {
		t.Errorf("Clear 后积压 = %d，应为 0", b.Len())
	}
	s := b.Stats()
	if s.Bytes != 0 {
		t.Errorf("Clear 后字节数 = %d，应为 0", s.Bytes)
	}
	// Clear 后仍可继续用
	b.Push(mkPoint(1, 100))
	if b.Len() != 1 {
		t.Error("Clear 后应可继续写入")
	}
}

func TestBuffer_Drain边界(t *testing.T) {
	b := New(true, 10, 1024*1024)
	// 空缓冲区
	if got := b.Drain(5); got != nil {
		t.Error("空缓冲区 Drain 应返回 nil")
	}
	// n <= 0
	b.Push(mkPoint(1, 100))
	if got := b.Drain(0); got != nil {
		t.Error("n=0 应返回 nil")
	}
	if got := b.Drain(-1); got != nil {
		t.Error("负数 n 应返回 nil")
	}
	if b.Len() != 1 {
		t.Error("无效 n 不应影响数据")
	}
	// n 超过实际数量
	got := b.Drain(100)
	if len(got) != 1 {
		t.Errorf("n 超出时应返回全部，实际 = %d", len(got))
	}
}

func TestBuffer_OldestAge(t *testing.T) {
	b := New(true, 10, 1024*1024)
	// 塞一个 5 分钟前的点
	b.Push(Point{Seq: 1, CollectedAt: time.Now().Add(-5 * time.Minute), Payload: make([]byte, 100)})
	s := b.Stats()
	if s.OldestAge < 290 || s.OldestAge > 310 {
		t.Errorf("最旧数据年龄 = %d 秒，期望 ~300", s.OldestAge)
	}
}

func TestBuffer_参数兜底(t *testing.T) {
	// 非法参数应被夹到安全区间，而不是产生 0 容量导致 panic
	b := New(true, 0, 0)
	if b.capacity < 1 {
		t.Error("容量应至少为 1")
	}
	if b.maxBytes < 1024 {
		t.Errorf("字节上限应至少 1KB，实际 = %d", b.maxBytes)
	}
	b.Push(mkPoint(1, 100))
	if b.Len() != 1 {
		t.Error("兜底参数下仍应能写入")
	}
}

func TestPoint_Size(t *testing.T) {
	p := Point{Payload: make([]byte, 100)}
	if p.Size() != 124 {
		t.Errorf("Size = %d，期望 124（24 字节 header + 100 载荷）", p.Size())
	}
}

func TestBuffer_内存占用可预测(t *testing.T) {
	// PRD G2 要求 Agent 内存 ≤20MB。缓冲 4MB 是其中一大块，
	// 必须验证"塞满时实际占用不超预估太多"。
	const maxBytes = 2 * 1024 * 1024
	b := New(true, 100000, maxBytes) // 点数上限故意设很大，让字节上限起作用
	for i := 0; i < 10000; i++ {
		b.Push(mkPoint(int32(i), 500))
	}
	s := b.Stats()
	if s.Bytes > maxBytes {
		t.Errorf("积压 %d 字节，超出上限 %d", s.Bytes, maxBytes)
	}
	t.Logf("上限 %d 字节下实际积压 %d 字节 / %d 点", maxBytes, s.Bytes, s.Points)
}

// Benchmark 补传开销：一次性 Drain 是否比逐个 Pop 快。
func BenchmarkDrain100(bench *testing.B) {
	buf := New(true, 1000, 4*1024*1024)
	bench.ResetTimer()
	for i := 0; i < bench.N; i++ {
		for j := 0; j < 120; j++ {
			buf.Push(mkPoint(int32(j), 800))
		}
		buf.Drain(120)
	}
}

func BenchmarkPush(bench *testing.B) {
	buf := New(true, 200, 1024*1024)
	p := mkPoint(1, 800)
	bench.ResetTimer()
	for i := 0; i < bench.N; i++ {
		buf.Push(p)
		if buf.Len() >= 190 {
			buf.Drain(190)
		}
	}
}

var _ = fmt.Sprintf // 保留 fmt 引用（调试用）
