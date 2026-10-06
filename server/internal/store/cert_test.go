package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/m202471895/probeone/server/internal/model"
)

// TestUpsertCertificate_参数与占位符匹配
//
// 这条测试存在的原因：UpsertCertificate 曾经是 9 个占位符配 8 个参数
// （漏了 MonitorID），生产环境每轮证书检查都报
// missing argument with index 9，证书信息永远写不进库。
// 单元测试没覆盖到这个方法，所以溜过去了。
//
// 断言写的是"能写进去并读回来"，不是检查 SQL 文本——
// 文本检查抓不住这类错，只有真正执行一次能发现。
func TestUpsertCertificate_参数与占位符匹配(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	now0 := time.Now().UTC()

	// 证书有指向 monitors 的外键，必须先有监控。
	// 漏了这步会得到 FOREIGN KEY constraint failed——
	// 那个报错与占位符不匹配长得不一样，别混淆。
	if _, err := db.Monitors.Create(ctx, &model.Monitor{
		Name:          "证书测试监控",
		Type:          model.MonitorType("ssl"),
		Target:        "example.com:443",
		IntervalSec:   60,
		TimeoutSec:    10,
		Status:        model.MonitorStatus("pending"),
		LastCheckedAt: &now0,
	}); err != nil {
		t.Fatalf("创建监控失败: %v", err)
	}

	now := time.Now().UTC()
	notBefore := now.Add(-24 * time.Hour)
	notAfter := now.Add(365 * 24 * time.Hour)
	daysLeft := 365

	cert := &model.SSLCertificate{
		MonitorID:   1, // 漏了这个参数就是那个 bug
		Subject:     "CN=example.com",
		Issuer:      "CN=Example CA",
		Serial:      "0A1B2C3D",
		NotBefore:   &notBefore,
		NotAfter:    &notAfter,
		DaysLeft:    &daysLeft,
		Fingerprint: "SHA256:abcdef",
	}

	if err := db.Monitors.UpsertCertificate(ctx, cert); err != nil {
		t.Fatalf("写入证书失败（占位符与参数个数不匹配时正是这个错）: %v", err)
	}

	got, err := db.Monitors.GetCertificate(ctx, 1)
	if err != nil {
		t.Fatalf("读回证书失败: %v", err)
	}
	if got.Subject != cert.Subject {
		t.Errorf("Subject = %q，期望 %q", got.Subject, cert.Subject)
	}
	if got.DaysLeft == nil || *got.DaysLeft != 365 {
		t.Errorf("DaysLeft 未正确写入，期望 365")
	}

	// 二次写入应更新而非报唯一约束冲突（upsert 语义）
	updated := 30
	cert.DaysLeft = &updated
	if err := db.Monitors.UpsertCertificate(ctx, cert); err != nil {
		t.Fatalf("二次写入应走 upsert 而非报错: %v", err)
	}
	got2, _ := db.Monitors.GetCertificate(ctx, 1)
	if got2.DaysLeft == nil || *got2.DaysLeft != 30 {
		t.Errorf("upsert 未生效，DaysLeft 未更新为 30")
	}
}
