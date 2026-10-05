module github.com/m202471895/probeone/server

go 1.23

require (
	github.com/m202471895/probeone/api v0.0.0-00010101000000-000000000000
	golang.org/x/crypto v0.31.0
	google.golang.org/grpc v1.65.0
	modernc.org/sqlite v1.34.5
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/net v0.25.0 // indirect
	golang.org/x/sys v0.28.0 // indirect
	golang.org/x/text v0.21.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240528184218-531527333157 // indirect
	google.golang.org/protobuf v1.34.1 // indirect
	modernc.org/libc v1.55.3 // indirect
	modernc.org/mathutil v1.6.0 // indirect
	modernc.org/memory v1.8.0 // indirect
)

// api 模块在仓库根的 api/ 目录（PRD 第 5 章目录约定），
// 用 replace 指向本地路径，避免为子目录单独开一个 Go module
replace github.com/m202471895/probeone/api => ../api
