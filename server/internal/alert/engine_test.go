package alert

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/m202471895/probeone/server/internal/config"
	"github.com/m202471895/probeone/server/internal/migrate"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/notify"
	"github.com/m202471895/probeone/server/internal/store"
	"github.com/m202471895/probeone/server/internal/store/sqlite"
)

// testEnv 是测试环境：真实数据库 + 真实的 webhook 通道（httptest 假服务器）。
type testEnv struct {
	db     *store.DB
	engine *Engine
	// sent 记录实际发出的通知数
	sent *atomic.Int32
	// channelID 是测试通道的 ID，规则默认挂到它上面
	channelID int64
}

func setup(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	handle, err := sqlite.Open(&config.DatabaseConfig{
		Driver: "sqlite", Path: dir + "/test.db",
	})
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })

	if err := migrate.New(handle, migrate.Builtin(), ".").Up(context.Background()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	db := store.NewWithDialect(handle, "sqlite")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// 假的 webhook 服务器，统计通知数
	var sent atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	registry := notify.NewRegistry(srv.Client())
	// 把 srv.URL 配进所有用到的通道
	channelID, err := db.Channels.Create(context.Background(), &model.AlertChannel{
		Name: "测试通道", Type: model.ChannelWebhook,
		Config: map[string]any{"url": srv.URL}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("创建通道失败: %v", err)
	}

	eng := New(Config{
		DedupWindow:    30 * time.Minute,
		StormThreshold: 3,
		StormSilence:   time.Hour,
	}, db, log, registry)

	return &testEnv{
		db: db, engine: eng, sent: &sent, channelID: channelID,
	}
}

// createRule 创建一个指向测试通道的规则。
func (e *testEnv) createRule(t *testing.T, name, op string, threshold float64,
	forTimes, forMinutes int) int64 {
	t.Helper()
	id, err := e.db.Alerts.CreateRule(context.Background(), &model.AlertRule{
		Name:       name,
		TargetType: model.TargetNode,
		Metric:     "cpu_usage",
		Condition: model.RuleCondition{
			Op: op, Value: threshold,
			ForTimes: forTimes, ForMinutes: forMinutes,
		},
		Severity:       model.SeverityWarning,
		ChannelIDs:     []int64{e.channelID},
		DedupWindowSec: 1800,
		Enabled:        true,
	})
	if err != nil {
		t.Fatalf("创建规则失败: %v", err)
	}
	return id
}

func Test阈值规则_单次命中即触发(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	ruleID := env.createRule(t, "CPU 高", ">", 80, 0, 0)

	s := Sample{
		TargetType: model.TargetNode, TargetID: 1, TargetName: "hk-01",
		Metric: "cpu_usage", Value: 85,
	}
	env.engine.Evaluate(ctx, s)

	if env.sent.Load() != 1 {
		t.Errorf("应发 1 条通知，实际 = %d", env.sent.Load())
	}
	// 事件应落库
	_, total, _ := env.db.Alerts.ListEvents(ctx, store.AlertListFilter{})
	if total != 1 {
		t.Errorf("应创建 1 个事件，实际 = %d", total)
	}
	_ = ruleID
}

func Test阈值规则_未达阈值不触发(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	env.createRule(t, "CPU 高", ">", 80, 0, 0)

	env.engine.Evaluate(ctx, Sample{
		TargetType: model.TargetNode, TargetID: 1, TargetName: "hk-01",
		Metric: "cpu_usage", Value: 75, // 未达80
	})

	if env.sent.Load() != 0 {
		t.Errorf("未达阈值不应通知，实际发了 %d 条", env.sent.Load())
	}
}

func Test阈值规则_连续次数达标才触发(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	env.createRule(t, "连续 3 次超 80", ">", 80, 3, 0)

	s := Sample{TargetType: model.TargetNode, TargetID: 1,
		TargetName: "hk-01", Metric: "cpu_usage", Value: 85}

	// 前两次不触发
	env.engine.Evaluate(ctx, s)
	env.engine.Evaluate(ctx, s)
	if env.sent.Load() != 0 {
		t.Fatalf("前 2 次不应触发，实际 = %d", env.sent.Load())
	}
	// 第 3 次触发
	env.engine.Evaluate(ctx, s)
	if env.sent.Load() != 1 {
		t.Errorf("第 3 次应触发，实际 = %d", env.sent.Load())
	}
	// 第 4 次不重复（已触发过）
	env.engine.Evaluate(ctx, s)
	if env.sent.Load() != 1 {
		t.Errorf("已触发后不应重复通知，实际 = %d", env.sent.Load())
	}
}

func Test阈值规则_连续中断则重置计数(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	env.createRule(t, "连续 3 次", ">", 80, 3, 0)

	hit := Sample{TargetType: model.TargetNode, TargetID: 1,
		TargetName: "hk-01", Metric: "cpu_usage", Value: 85}
	miss := Sample{TargetType: model.TargetNode, TargetID: 1,
		TargetName: "hk-01", Metric: "cpu_usage", Value: 50}

	env.engine.Evaluate(ctx, hit)
	env.engine.Evaluate(ctx, hit)
	env.engine.Evaluate(ctx, miss) // 中断
	env.engine.Evaluate(ctx, hit)
	env.engine.Evaluate(ctx, hit)

	// 中断后计数重置，第 5 次是重新的第 2 次，不该触发
	if env.sent.Load() != 0 {
		t.Errorf("连续中断后计数应重置，实际发了 %d 条", env.sent.Load())
	}
}

func Test去重_同窗口内不重复通知(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	// 去重窗口设 0（走默认 1800s）
	env.createRule(t, "去重测试", ">", 80, 0, 0)

	// 第一次触发
	env.engine.Evaluate(ctx, Sample{TargetType: model.TargetNode, TargetID: 1,
		TargetName: "hk-01", Metric: "cpu_usage", Value: 85})
	// 恢复：发一条恢复通知，并把 firing 事件标记为 resolved
	env.engine.Evaluate(ctx, Sample{TargetType: model.TargetNode, TargetID: 1,
		TargetName: "hk-01", Metric: "cpu_usage", Value: 50})
	if env.sent.Load() != 2 {
		t.Errorf("恢复应发第 2 条（恢复通知），实际 = %d", env.sent.Load())
	}

	// 再次触发：这是"恢复后的新异常"，应新建事件并通知。
	// 去重的语义是"同一次异常的持续期间只通知一次"，
	// 而不是"历史上最近 N 分钟内只通知一次"——后者会让
	// 一个每天抖一次的监控永远不再通知。
	env.engine.Evaluate(ctx, Sample{TargetType: model.TargetNode, TargetID: 1,
		TargetName: "hk-01", Metric: "cpu_usage", Value: 85})
	if env.sent.Load() != 3 {
		t.Errorf("恢复后再次触发应新建告警，实际发出 = %d 条", env.sent.Load())
	}

	// 事件数是 2 而不是 3：第一次的 firing 事件在恢复时被改成 resolved，
	// 并没有新建一条"恢复事件"——恢复只是改状态 + 发通知。
	// 第二次触发才是真正的新建。
	_, total, _ := env.db.Alerts.ListEvents(ctx, store.AlertListFilter{})
	if total != 2 {
		t.Errorf("应创建 2 个事件（第一次被resolved + 第二次新建），实际 = %d", total)
	}
	// 其中一个 resolved、一个 firing
	evs, _, _ := env.db.Alerts.ListEvents(ctx, store.AlertListFilter{})
	var resolved, firing int
	for _, e := range evs {
		switch e.Status {
		case model.EventResolved:
			resolved++
		case model.EventFiring:
			firing++
		}
	}
	if resolved != 1 || firing != 1 {
		t.Errorf("应各有 1 个 resolved 与 firing，实际 resolved=%d firing=%d", resolved, firing)
	}
}

func Test去重_持续期间只通知一次(t *testing.T) {
	// 去重的真正语义：条件一直满足时，无论来多少个样本都只通知一次。
	// 这是"同一次异常"的定义。
	env := setup(t)
	ctx := context.Background()
	env.createRule(t, "持续告警", ">", 80, 0, 0)

	hit := Sample{TargetType: model.TargetNode, TargetID: 1,
		TargetName: "hk-01", Metric: "cpu_usage", Value: 85}

	// 连续 10 个样本全部超阈值
	for i := 0; i < 10; i++ {
		env.engine.Evaluate(ctx, hit)
	}
	if env.sent.Load() != 1 {
		t.Errorf("持续超阈值 10 次应只发 1 条通知，实际 = %d", env.sent.Load())
	}
	// 事件数：只有 1 个（后续都是更新时间）
	_, total, _ := env.db.Alerts.ListEvents(ctx, store.AlertListFilter{})
	if total != 1 {
		t.Errorf("持续告警应只有 1 个事件，实际 = %d", total)
	}
}

func Test恢复_发恢复通知(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	env.createRule(t, "CPU 高", ">", 80, 0, 0)

	// 触发
	env.engine.Evaluate(ctx, Sample{TargetType: model.TargetNode, TargetID: 1,
		TargetName: "hk-01", Metric: "cpu_usage", Value: 85})
	if env.sent.Load() != 1 {
		t.Fatalf("应先发 1 条告警，实际 = %d", env.sent.Load())
	}

	// 恢复
	env.engine.Evaluate(ctx, Sample{TargetType: model.TargetNode, TargetID: 1,
		TargetName: "hk-01", Metric: "cpu_usage", Value: 50})
	if env.sent.Load() != 2 {
		t.Errorf("恢复应发第 2 条通知，实际 = %d", env.sent.Load())
	}

	// 事件应标记为已解决
	evs, _, _ := env.db.Alerts.ListEvents(ctx, store.AlertListFilter{
		Status: model.EventResolved,
	})
	if len(evs) != 1 {
		t.Errorf("应有 1 个已解决事件，实际 = %d", len(evs))
	}
}

func Test恢复_未触发过则不发恢复通知(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	env.createRule(t, "CPU 高", ">", 80, 3, 0) // 需要连续 3 次

	// 只命中 1 次然后恢复
	env.engine.Evaluate(ctx, Sample{TargetType: model.TargetNode, TargetID: 1,
		TargetName: "hk-01", Metric: "cpu_usage", Value: 85})
	env.engine.Evaluate(ctx, Sample{TargetType: model.TargetNode, TargetID: 1,
		TargetName: "hk-01", Metric: "cpu_usage", Value: 50})

	if env.sent.Load() != 0 {
		t.Errorf("从未触发过就不该发任何通知（含恢复），实际 = %d", env.sent.Load())
	}
}

func Test防风暴_超阈值后静默(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	env.createRule(t, "抖动规则", ">", 80, 0, 0)

	hit := Sample{TargetType: model.TargetNode, TargetID: 1,
		TargetName: "noisy-01", Metric: "cpu_usage", Value: 85}
	low := Sample{TargetType: model.TargetNode, TargetID: 1,
		TargetName: "noisy-01", Metric: "cpu_usage", Value: 50}

	// 反复触发-恢复 5 轮
	for i := 0; i < 5; i++ {
		env.engine.Evaluate(ctx, hit)
		env.engine.Evaluate(ctx, low)
	}

	// 统计实际发出了多少条**告警**（不含恢复通知）。
	// 恢复通知不在防风暴管辖范围内——用户需要知道问题结束了。
	alerts, _, _ := env.db.Alerts.ListEvents(ctx, store.AlertListFilter{
		Severity: model.SeverityWarning,
	})
	notified := 0
	for _, ev := range alerts {
		if ev.Notified {
			notified++
		}
	}
	if notified > 3 {
		t.Errorf("超过防风暴阈值(3)后应静默，实际通知了 %d 条", notified)
	}
	if notified < 1 {
		t.Error("静默前应至少通知过 1 条")
	}
}

func Test防风暴_不同目标独立计数(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	env.createRule(t, "CPU 高", ">", 80, 0, 0)

	// 节点 A 触发 3 次（进入静默）
	for i := 0; i < 4; i++ {
		env.engine.Evaluate(ctx, Sample{TargetType: model.TargetNode, TargetID: 1,
			TargetName: "node-A", Metric: "cpu_usage", Value: 85})
		env.engine.Evaluate(ctx, Sample{TargetType: model.TargetNode, TargetID: 1,
			TargetName: "node-A", Metric: "cpu_usage", Value: 50})
	}
	sentA := env.sent.Load()

	// 节点 B 应能正常通知（不受 A 的静默影响）
	env.engine.Evaluate(ctx, Sample{TargetType: model.TargetNode, TargetID: 2,
		TargetName: "node-B", Metric: "cpu_usage", Value: 85})

	if env.sent.Load() <= sentA {
		t.Errorf("节点 B 的告警不应被节点 A 的静默影响：之前 %d，之后 %d", sentA, env.sent.Load())
	}
}

func Test规则匹配_目标与指标过滤(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	// TargetID 限定为节点 1
	rid := int64(1)
	_, err := env.db.Alerts.CreateRule(ctx, &model.AlertRule{
		Name: "指定节点", TargetType: model.TargetNode, TargetID: &rid,
		Metric: "cpu_usage", Condition: model.RuleCondition{Op: ">", Value: 80},
		Severity: model.SeverityWarning, ChannelIDs: []int64{env.channelID},
		DedupWindowSec: 1800, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 节点 2 的样本不应触发
	env.engine.Evaluate(ctx, Sample{TargetType: model.TargetNode, TargetID: 2,
		TargetName: "hk-02", Metric: "cpu_usage", Value: 95})
	if env.sent.Load() != 0 {
		t.Errorf("规则限定节点 1，节点 2 不该触发，实际 = %d", env.sent.Load())
	}

	// 节点 1 但指标不匹配，不应触发
	env.engine.Evaluate(ctx, Sample{TargetType: model.TargetNode, TargetID: 1,
		TargetName: "hk-01", Metric: "mem_usage", Value: 95})
	if env.sent.Load() != 0 {
		t.Errorf("指标不匹配不该触发，实际 = %d", env.sent.Load())
	}

	// 节点 1 + 匹配指标，应触发
	env.engine.Evaluate(ctx, Sample{TargetType: model.TargetNode, TargetID: 1,
		TargetName: "hk-01", Metric: "cpu_usage", Value: 95})
	if env.sent.Load() != 1 {
		t.Errorf("完全匹配应触发，实际 = %d", env.sent.Load())
	}
}

func Test规则匹配_未指定目标则匹配全部(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	// TargetID 为 nil 表示匹配全部节点
	_, err := env.db.Alerts.CreateRule(ctx, &model.AlertRule{
		Name: "全局规则", TargetType: model.TargetNode, TargetID: nil,
		Metric: "cpu_usage", Condition: model.RuleCondition{Op: ">", Value: 80},
		Severity: model.SeverityWarning, ChannelIDs: []int64{env.channelID},
		DedupWindowSec: 1800, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []int64{1, 2, 3} {
		env.engine.Evaluate(ctx, Sample{TargetType: model.TargetNode, TargetID: id,
			TargetName: "node", Metric: "cpu_usage", Value: 85})
	}
	if env.sent.Load() != 3 {
		t.Errorf("未指定目标时应匹配全部节点，应发 3 条，实际 = %d", env.sent.Load())
	}
}

func TestConditionMet(t *testing.T) {
	cases := []struct {
		value float64
		cond  model.RuleCondition
		want  bool
	}{
		{85, model.RuleCondition{Op: ">", Value: 80}, true},
		{75, model.RuleCondition{Op: ">", Value: 80}, false},
		{80, model.RuleCondition{Op: ">=", Value: 80}, true},
		{80, model.RuleCondition{Op: ">", Value: 80}, false},
		{75, model.RuleCondition{Op: "<", Value: 80}, true},
		{80, model.RuleCondition{Op: "<=", Value: 80}, true},
		{80, model.RuleCondition{Op: "==", Value: 80}, true},
		{81, model.RuleCondition{Op: "==", Value: 80}, false},
		{81, model.RuleCondition{Op: "!=", Value: 80}, true},
		{85, model.RuleCondition{Op: "无效运算符", Value: 80}, false}, // 未知运算符应判为不满足
	}
	for _, c := range cases {
		// 直接测条件判定逻辑：构造一个只含该规则的引擎
		e := &Engine{state: map[string]*ruleState{}}
		st := e.bumpHit("k", c.cond, c.value)
		_ = st
		got := conditionMetForTest(c.value, c.cond)
		if got != c.want {
			t.Errorf("conditionMet(%.0f, %s %.0f) = %v，期望 %v",
				c.value, c.cond.Op, c.cond.Value, got, c.want)
		}
	}
}

// conditionMetForTest 暴露条件判定用于测试。
func conditionMetForTest(v float64, c model.RuleCondition) bool {
	e := &Engine{}
	return e.conditionMet(v, c)
}

func Test无通道时事件仍落库(t *testing.T) {
	// 没配通知通道时，事件必须照常入库——
	// 否则用户回头配好通道后会发现历史告警全丢了
	dir := t.TempDir()
	handle, err := sqlite.Open(&config.DatabaseConfig{Driver: "sqlite", Path: dir + "/t.db"})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if err := migrate.New(handle, migrate.Builtin(), ".").Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	db := store.NewWithDialect(handle, "sqlite")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// 规则不配任何通道
	_, err = db.Alerts.CreateRule(context.Background(), &model.AlertRule{
		Name: "无通道规则", TargetType: model.TargetNode,
		Metric: "cpu_usage", Condition: model.RuleCondition{Op: ">", Value: 80},
		Severity: model.SeverityWarning, ChannelIDs: []int64{},
		DedupWindowSec: 1800, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	eng := New(Config{DedupWindow: time.Minute, StormThreshold: 100, StormSilence: time.Hour},
		db, log, notify.NewRegistry(http.DefaultClient))

	eng.Evaluate(context.Background(), Sample{
		TargetType: model.TargetNode, TargetID: 1, TargetName: "n1",
		Metric: "cpu_usage", Value: 85,
	})

	_, total, _ := db.Alerts.ListEvents(context.Background(), store.AlertListFilter{})
	if total != 1 {
		t.Errorf("无通知通道时事件仍应落库，实际 = %d", total)
	}
}

func Test并发评估安全(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	env.createRule(t, "并发规则", ">", 80, 0, 0)

	done := make(chan struct{})
	for g := 0; g < 10; g++ {
		go func(g int) {
			for i := 0; i < 20; i++ {
				env.engine.Evaluate(ctx, Sample{
					TargetType: model.TargetNode, TargetID: int64(g),
					TargetName: "node", Metric: "cpu_usage", Value: 85,
				})
			}
			done <- struct{}{}
		}(g)
	}
	for g := 0; g < 10; g++ {
		<-done
	}
	// -race 会抓数据竞争
}
