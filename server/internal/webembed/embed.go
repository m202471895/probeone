// Package webembed 把前端产物内嵌进二进制。
//
// 为什么用 embed 而不是运行时读磁盘：
//  1. 单文件部署——拷贝一个二进制就能跑，不依赖外部目录
//  2. 不会出现"忘了同步前端导致后端是新版、前端是旧版"的错配
//
// 代价是改前端必须重新编译后端。开发时用 Vite dev server
// （见 vite.config.ts 的 proxy），不经过这里。
//
// embed 必须在**本包内**声明——go:embed 只能嵌入当前包目录树。
// 因此前端产物需要复制到本目录下（见 scripts/sync-web.sh），
// 而不是直接嵌入 ../web/dist。
package webembed

import (
	"embed"
	"io/fs"
)

// dist 需存在才能通过编译。
//
// 缺失时的报错必须明确：否则 go:embed 会报一个难懂的
// "pattern dist/*: no matching files found"，看不出是前端没构建。
//
// downloads 单独声明：Agent 二进制体积大（~15MB），
// 放外面是为了能让构建脚本往里放而不必重新打包前端。
//
//go:embed all:dist
var dist embed.FS

// FS 返回去掉 dist 前缀的产物文件系统。
//
// 去掉前缀后可以整体挂到 http.FileServer：内部路径变成
// index.html、assets/xxx.css，与 index.html 里的相对引用对得上。
func FS() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}

// Available 报告前端产物是否已内嵌。
// 用于启动时给出明确提示，而不是让人对着白屏猜。
func Available() bool {
	f, err := dist.Open("dist/index.html")
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
