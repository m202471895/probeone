package sqlbase

import "time"

// UTC 归一。
//
// 为什么必须在仓储层统一做：
// 时间列一律按 UTC 写入，而 SQLite 驱动按字面量比较时间值。
// 调用方传本地时区（如 UTC+8）会匹配 0 行且**不报错**——
// 拿到空切片只能理解成"这段时间没数据"，实际是时区错了。
// 这种静默失败比报错更难排查，所以在边界处一次性填平。
//
// Postgres 的 timestamptz 自带时区，归一化也无害，
// 因此两种驱动共用同一份代码。
func UTC(t time.Time) time.Time { return t.UTC() }

// UTCRange 归一一个时间区间。两端都要处理——
// 只归一 from 而漏了 to 同样会匹配不到。
func UTCRange(from, to time.Time) (time.Time, time.Time) {
	return from.UTC(), to.UTC()
}
