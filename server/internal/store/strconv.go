package store

import "strconv"

// strconvParseInt 是strconv.ParseInt 的薄封装，
// 供 repo_metric.go 的聚合键解析使用。
func strconvParseInt(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }
