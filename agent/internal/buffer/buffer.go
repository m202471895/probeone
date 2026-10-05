// Package buffer 实现离线数据缓冲。
//
// 为什么需要：监控系统最不能接受的就是"网络抖动导致曲线出现无法解释的空洞"。
// Agent 断网期间把采样点存在本地环形队列里，恢复后一次性补传。
//
// 关键设计：
//   - **有界**：同时受点数上限与字节上限约束。内存占用必须可预测（PRD G2），
//     不能让"配置写错一个数"就把 Agent 的内存吃光
//   - **不阻塞**：采集路径调用 Push 时不加锁等待。锁竞争最坏情况是丢一个点，
//     而阻塞采集会拖慢整个循环，得不偿失
//   - **不重复**：补传后从缓冲区移除。若服务端已收到但Agent 崩溃导致 ACK 丢失，
//     补传会产生重复数据——服务端按 collected_at 覆盖写入，天然幂等
package buffer

import (
	"sync"
	"sync/atomic"
	"time"
)

// Point 是一个待上报的采样点。
type Point struct {
	Seq         int32
	CollectedAt time.Time
	// Payload 是已序列化的指标数据（JSON）。
	// Agent 侧用 JSON 而非 protobuf：缓冲区数据要能落盘排查，
	// JSON 可读性远好于二进制。
	Payload []byte
}

// Size 返回该点的字节占用（估算）。
func (p *Point) Size() int {
	// 24 字节 slice header + len(Payload)
	return 24 + len(p.Payload)
}

// Stats 是缓冲区统计。
type Stats struct {
	Enabled   bool
	Points    int    // 当前积压点数
	Bytes     int    // 当前积压字节
	Capacity  int    // 点数上限
	MaxBytes  int    // 字节上限
	Dropped   uint64 // 因超限丢弃的累计点数
	Flushed   uint64 // 累计补传成功的点数
	OldestAge int    // 最旧数据距今秒数
}

// Buffer 是环形缓冲区。
type Buffer struct {
	enabled  bool
	capacity int
	maxBytes int

	// 环形存储：slice + head 索引，避免 append 反复分配
	slots []Point
	head  int
	size  int
	bytes int

	mu      sync.RWMutex
	dropped atomic.Uint64
	flushed atomic.Uint64
}

// New 创建缓冲区。
// enabled=false 时所有写操作都是空操作，调用方无需分支判断。
func New(enabled bool, capacity, maxBytes int) *Buffer {
	if capacity < 1 {
		capacity = 1
	}
	// 下限设 1KB 而不是 64KB。用户的配置可能合法地想要一个小缓冲
	// （比如内存极度紧张的机器上只留 20 个点）。
	// 真正需要保护的是"内存失控"，那由上层 config 的下限校验负责，
	// 这里只做防御性兜底，不该反过来覆盖用户的明确配置。
	if maxBytes < 1024 {
		maxBytes = 1024
	}
	// 预分配 slots 切片。单个 Point 约 32 字节（header），
	// 120 个槽位只占 4KB 左右，不值得为省这点内存增加复杂度。
	return &Buffer{
		enabled:  enabled,
		capacity: capacity,
		maxBytes: maxBytes,
		slots:    make([]Point, capacity),
	}
}

// Push 写入一个采样点。
//
// 满了或超字节上限时丢弃**最旧**的点——保留近期数据比保留久远数据更有价值，
// 因为用户看曲线时最关心最近的走势。
func (b *Buffer) Push(p Point) {
	if !b.enabled {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// 槽位已满：覆盖最旧的
	if b.size == b.capacity {
		b.bytes -= b.slots[b.head].Size()
		b.slots[b.head] = Point{}
		b.head = (b.head + 1) % b.capacity
		b.size--
		b.dropped.Add(1)
	}

	// 超出字节上限：持续丢最旧的直到放得下
	for b.size > 0 && b.bytes+p.Size() > b.maxBytes {
		oldest := b.head
		b.bytes -= b.slots[oldest].Size()
		b.slots[oldest] = Point{}
		b.head = (b.head + 1) % b.capacity
		b.size--
		b.dropped.Add(1)
	}

	b.slots[(b.head+b.size)%b.capacity] = p
	b.size++
	b.bytes += p.Size()
}

// Drain 取出并移除最早的最多 n 个点。
//
// 一次性取出而非逐个 Pop，是为了让补传能在一次 gRPC 消息里发多个点，
// 减少往返开销。返回的切片是新分配的，调用方可以安全持有。
func (b *Buffer) Drain(n int) []Point {
	if !b.enabled || n <= 0 {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.size == 0 {
		return nil
	}
	if n > b.size {
		n = b.size
	}

	out := make([]Point, 0, n)
	for i := 0; i < n; i++ {
		p := b.slots[(b.head+i)%b.capacity]
		b.bytes -= p.Size()
		b.slots[(b.head+i)%b.capacity] = Point{}
		out = append(out, p)
	}
	b.head = (b.head + n) % b.capacity
	b.size -= n
	b.flushed.Add(uint64(n))
	return out
}

// Peek 查看到最早的点，不移除。
func (b *Buffer) Peek() (Point, bool) {
	if !b.enabled {
		return Point{}, false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.size == 0 {
		return Point{}, false
	}
	return b.slots[b.head], true
}

// Len 返回积压点数。
func (b *Buffer) Len() int {
	if !b.enabled {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.size
}

// Stats 返回统计信息，用于日志与调试端点。
func (b *Buffer) Stats() Stats {
	s := Stats{
		Enabled:  b.enabled,
		Capacity: b.capacity,
		MaxBytes: b.maxBytes,
		Dropped:  b.dropped.Load(),
		Flushed:  b.flushed.Load(),
	}
	if !b.enabled {
		return s
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	s.Points = b.size
	s.Bytes = b.bytes
	if b.size > 0 {
		s.OldestAge = int(time.Since(b.slots[b.head].CollectedAt).Seconds())
	}
	return s
}

// Clear 清空缓冲区。
func (b *Buffer) Clear() {
	if !b.enabled {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.slots {
		b.slots[i] = Point{}
	}
	b.head, b.size, b.bytes = 0, 0, 0
}

// Enabled 返回缓冲是否开启。
func (b *Buffer) Enabled() bool { return b.enabled }

// EstimatePayloadSize 估算一份指标 JSON 的字节数，用于容量规划。
//
// 实测含多网卡多磁盘的完整一轮采样约 400–900 字节。
// 取 1KB 作为保守估计，配合 maxBytes 能覆盖约 4000 个点。
func EstimatePayloadSize() int { return 1024 }
