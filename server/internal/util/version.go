// Package util 提供通用工具。
package util

// Version 是构建版本号。
// 发布时由 Makefile 通过 -ldflags "-X ...util.Version=vX.Y.Z" 注入。
var Version = "dev"

// Commit 是构建时的 git commit。
var Commit = "unknown"

// BuildTime 是构建时间。
var BuildTime = "unknown"
