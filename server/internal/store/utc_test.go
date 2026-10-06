package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/m202471895/probeone/server/internal/auth"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/store"
)

// Test时间查询_本地时区不静默返回空集
//
// 背景：时间列按 UTC 写入，而 SQLite 按字面量比较。
// 修复前传本地时区会匹配 0 行且不报错——
// 拿到空切片只能理解成"这段时间没数据"，实际是时区错了。
// 这种静默失败比报错更难排查，所以用测试锁死行为。
func Test时间查询_本地时区不静默返回空集(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	hash, err := auth.HashPassword("test-secret-123456")
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	nodeID, err := db.Nodes.Create(ctx, store.CreateNodeInput{
		UID: "utc-node", Name: "时区测试节点", SecretHash: hash,
	})
	if err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	// 造一条 2 小时前的指标
	base := time.Now().UTC().Add(-2 * time.Hour)
	_, err = db.Metrics.Insert(ctx, &model.NodeMetric{NodeID: nodeID, CollectedAt: base, CPUUsage: 42, MemUsage: 51})
	if err != nil {
		t.Fatalf("插入指标失败: %v", err)
	}

	// 用本地时区构造同一个瞬间
	loc := time.FixedZone("UTC+8", 8*3600)
	fromLocal := base.In(loc).Add(-30 * time.Minute)
	toLocal := base.In(loc).Add(30 * time.Minute)

	rows, err := db.Metrics.Range(ctx, nodeID, fromLocal, toLocal, 100)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(rows) == 0 {
		t.Errorf("用本地时区(UTC+8)查询同一瞬间返回 0 行——时区未归一，"+
			"调用方会误判成\"这段时间没数据\"（期望至少 1 行）")
	}
}

// Test时间查询_区间两端都归一
//
// 只归一 from 漏掉 to 同样会匹配不到——这类半吊子修复最隐蔽。
func Test时间查询_区间两端都归一(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	hash, _ := auth.HashPassword("test-secret-123456")
	nodeID, err := db.Nodes.Create(ctx, store.CreateNodeInput{
		UID: "utc-node2", Name: "时区测试节点2", SecretHash: hash,
	})
	if err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}

	now := time.Now().UTC()
	if _, err := db.Metrics.Insert(ctx, &model.NodeMetric{NodeID: nodeID, CollectedAt: now, CPUUsage: 42, MemUsage: 51}); err != nil {
		t.Fatalf("插入指标失败: %v", err)
	}

	loc := time.FixedZone("UTC+8", 8*3600)
	// from 在 UTC、to 在本地时区：混用时最容易漏归一
	from := now.Add(-time.Hour)
	to := now.Add(time.Hour).In(loc)

	rows, err := db.Metrics.Range(ctx, nodeID, from, to, 100)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(rows) == 0 {
		t.Errorf("from/to 时区不一致时返回 0 行——区间两端都必须归一")
	}
}
