package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/m202471895/probeone/server/internal/apperr"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/store/sqlbase"
)

// ---------- 告警 ----------

type alertRepo struct {
	db *sql.DB
	d  sqlbase.Dialect
}

func (r *alertRepo) CreateRule(ctx context.Context, m *model.AlertRule) (int64, error) {
	q := `INSERT INTO alert_rules (name, target_type, target_id, metric, condition,
		severity, channel_ids, dedup_window_sec, enabled, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`
	res, err := r.db.ExecContext(ctx, r.d.Rebind(q),
		m.Name, string(m.TargetType), m.TargetID, m.Metric,
		sqlbase.MarshalJSON(m.Condition), string(m.Severity),
		sqlbase.MarshalJSON(m.ChannelIDs), m.DedupWindowSec, m.Enabled, sqlbase.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *alertRepo) GetRule(ctx context.Context, id int64) (*model.AlertRule, error) {
	q := `SELECT id, name, target_type, target_id, COALESCE(metric,''), condition,
		severity, channel_ids, dedup_window_sec, enabled, created_at
		FROM alert_rules WHERE id = ?`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, apperr.NotFound(apperr.CodeRuleNotFound, "告警规则不存在")
	}
	return scanRule(rows)
}

func (r *alertRepo) scanRuleRows(rows *sql.Rows) ([]model.AlertRule, error) {
	var out []model.AlertRule
	for rows.Next() {
		m, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func scanRule(s scanner) (*model.AlertRule, error) {
	var (
		m                    model.AlertRule
		targetType, severity string
		targetID             sql.NullInt64
		condJSON, chJSON     sql.NullString
		createdAt            time.Time
	)
	err := s.Scan(&m.ID, &m.Name, &targetType, &targetID, &m.Metric, &condJSON,
		&severity, &chJSON, &m.DedupWindowSec, &m.Enabled, &createdAt)
	if err != nil {
		return nil, err
	}
	if targetID.Valid {
		v := targetID.Int64
		m.TargetID = &v
	}
	m.TargetType = model.AlertTargetType(targetType)
	m.Severity = model.Severity(severity)
	m.CreatedAt = createdAt
	sqlbase.ScanJSON(condJSON, &m.Condition)
	sqlbase.ScanJSON(chJSON, &m.ChannelIDs)
	return &m, nil
}

func (r *alertRepo) ListRules(ctx context.Context, enabledOnly bool) ([]model.AlertRule, error) {
	q := `SELECT id, name, target_type, target_id, COALESCE(metric,''), condition,
		severity, channel_ids, dedup_window_sec, enabled, created_at
		FROM alert_rules`
	if enabledOnly {
		q += ` WHERE enabled = 1`
	}
	q += ` ORDER BY id`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return r.scanRuleRows(rows)
}

func (r *alertRepo) UpdateRule(ctx context.Context, m *model.AlertRule) error {
	q := `UPDATE alert_rules SET name = ?, target_type = ?, target_id = ?, metric = ?,
		condition = ?, severity = ?, channel_ids = ?, dedup_window_sec = ?, enabled = ?
		WHERE id = ?`
	res, _ := r.db.ExecContext(ctx, r.d.Rebind(q),
		m.Name, string(m.TargetType), m.TargetID, m.Metric,
		sqlbase.MarshalJSON(m.Condition), string(m.Severity),
		sqlbase.MarshalJSON(m.ChannelIDs), m.DedupWindowSec, m.Enabled, m.ID)
	return checkAffected(res, "告警规则不存在")
}

func (r *alertRepo) DeleteRule(ctx context.Context, id int64) error {
	res, _ := r.db.ExecContext(ctx, r.d.Rebind(`DELETE FROM alert_rules WHERE id = ?`), id)
	return checkAffected(res, "告警规则不存在")
}

// FireEvent 创建或复用告警事件。
//
// 第二个返回值 created 的含义：
//   - true  = 新建了事件，需要发通知
//   - false = 去重窗口内已有 firing 事件，只更新了 last_fired_at，不通知
//
// 这是告警去重的**唯一实现点**（PRD 8.6），
// 必须在事务内完成"查 + 插"，否则并发下会漏出去重。
func (r *alertRepo) FireEvent(ctx context.Context, e *model.AlertEvent, dedupWindowSec int) (*model.AlertEvent, bool, error) {
	if dedupWindowSec <= 0 {
		dedupWindowSec = 1800
	}
	cutoff := sqlbase.Now().Add(-time.Duration(dedupWindowSec) * time.Second)

	var result *model.AlertEvent
	created := false

	// 事务内用 r.db 的方言转换占位符，SQL 写在下方
	err := InTx(ctx, r.db, func(h txer) error {
		// 1. 查去重窗口内同 (rule, target, severity) 的 firing 事件
		//
		// severity 必须参与去重键。早期版本只按 (rule, target) 去重，
		// 结果是同一条规则先触发 warning 再升级为 critical 时，
		// critical 事件会被 warning 的去重窗口吞掉——
		// 恰恰是最该通知的场景被静默了。
		// 同理 resolved 状态不参与此查询（见下），避免"已恢复"被当作 firing 复用。
		var existingID int64
		var firstFired time.Time
		lookup := `SELECT id, first_fired_at FROM alert_events
			WHERE target_type = ? AND COALESCE(target_id, -1) = COALESCE(?, -1)
			  AND COALESCE(rule_id, -1) = COALESCE(?, -1)
			  AND severity = ?
			  AND status = 'firing' AND last_fired_at > ?
			ORDER BY last_fired_at DESC LIMIT 1`
		err := h.QueryRowContext(ctx, r.d.Rebind(lookup),
			string(e.TargetType), e.TargetID, e.RuleID, string(e.Severity), cutoff).
			Scan(&existingID, &firstFired)

		switch {
		case err == sql.ErrNoRows:
			// 2. 无重复 → 新建
			q := `INSERT INTO alert_events (rule_id, target_type, target_id, target_name,
				severity, status, message, payload, notified,
				first_fired_at, last_fired_at)
				VALUES (?,?,?,?,?,?,?,?,0,?,?)`
			now := sqlbase.Now()
			res, err := h.ExecContext(ctx, r.d.Rebind(q),
				e.RuleID, string(e.TargetType), e.TargetID, e.TargetName,
				string(e.Severity), string(model.EventFiring), e.Message,
				sqlbase.MarshalJSON(e.Payload), now, now)
			if err != nil {
				return err
			}
			id, err := res.LastInsertId()
			if err != nil {
				return err
			}
			result = &model.AlertEvent{
				ID: id, RuleID: e.RuleID, TargetType: e.TargetType, TargetID: e.TargetID,
				TargetName: e.TargetName, Severity: e.Severity, Status: model.EventFiring,
				Message: e.Message, Payload: e.Payload,
				FirstFiredAt: now, LastFiredAt: now,
			}
			created = true
			return nil

		case err != nil:
			return err

		default:
			// 3. 有重复 → 只更新时间，不新建、不通知
			if _, err := h.ExecContext(ctx, r.d.Rebind(
				`UPDATE alert_events SET last_fired_at = ? WHERE id = ?`),
				sqlbase.Now(), existingID); err != nil {
				return err
			}
			result = &model.AlertEvent{
				ID: existingID, RuleID: e.RuleID, TargetType: e.TargetType, TargetID: e.TargetID,
				TargetName: e.TargetName, Severity: e.Severity, Status: model.EventFiring,
				Message: e.Message, FirstFiredAt: firstFired, LastFiredAt: sqlbase.Now(),
			}
			created = false
			return nil
		}
	})

	return result, created, err
}

// ResolveEvents 把该目标下未结束的告警置为已恢复。
//
// 注意 WHERE 条件同时包含 firing 与 acked：
// acked 表示"值班人已确认但问题还在"，它同样是未结束状态。
// 早期版本只匹配 firing，导致"已确认的告警"永远不会被标记恢复，
// 事件会一直挂在未解决列表里。
func (r *alertRepo) ResolveEvents(ctx context.Context, ruleID *int64, targetType model.AlertTargetType, targetID int64) (int64, error) {
	q := `UPDATE alert_events SET status = 'resolved', resolved_at = ?
	      WHERE status IN ('firing', 'acked') AND target_type = ?
	      AND COALESCE(target_id, -1) = COALESCE(?, -1)
	      AND COALESCE(rule_id, -1) = COALESCE(?, -1)`
	res, err := r.db.ExecContext(ctx, r.d.Rebind(q),
		sqlbase.Now(), string(targetType), targetID, ruleID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *alertRepo) GetEvent(ctx context.Context, id int64) (*model.AlertEvent, error) {
	q := `SELECT id, rule_id, target_type, target_id, target_name, severity, status,
		COALESCE(message,''), payload, notified, first_fired_at, last_fired_at,
		resolved_at, acked_at, acked_by
		FROM alert_events WHERE id = ?`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, apperr.NotFound("ALERT_NOT_FOUND", "告警事件不存在")
	}
	return scanEvent(rows)
}

func (r *alertRepo) ListEvents(ctx context.Context, f AlertListFilter) ([]model.AlertEvent, int, error) {
	// 条件片段与参数一起生成，Count 与 List 共用，
	// 避免两处筛选逻辑漂移导致总数对不上
	where, args := eventFilter(f)

	var total int
	countQ := `SELECT COUNT(*) FROM alert_events` + where
	if err := r.db.QueryRowContext(ctx, r.d.Rebind(countQ), args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit, offset := f.Limit, f.Offset
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	q := `SELECT id, rule_id, target_type, target_id, target_name, severity, status,
		COALESCE(message,''), payload, notified, first_fired_at, last_fired_at,
		resolved_at, acked_at, acked_by
		FROM alert_events` + where + ` ORDER BY last_fired_at DESC LIMIT ? OFFSET ?`

	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []model.AlertEvent
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *e)
	}
	return out, total, rows.Err()
}

// eventFilter 生成 WHERE 子句与参数。
// 单独抽出是为了让 Count 与 List 用完全相同的筛选条件。
func eventFilter(f AlertListFilter) (string, []any) {
	q := ` WHERE 1=1`
	var args []any
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, string(f.Status))
	}
	if f.Severity != "" {
		q += ` AND severity = ?`
		args = append(args, string(f.Severity))
	}
	if f.TargetType != "" {
		q += ` AND target_type = ?`
		args = append(args, string(f.TargetType))
	}
	if f.TargetID != nil {
		q += ` AND target_id = ?`
		args = append(args, *f.TargetID)
	}
	return q, args
}

func scanEvent(s scanner) (*model.AlertEvent, error) {
	var (
		e                     model.AlertEvent
		targetType, severity  string
		status                string
		ruleID, targetID      sql.NullInt64
		msg                   string
		payload               sql.NullString
		notified              bool
		firstFired, lastFired time.Time
		resolvedAt, ackedAt   sql.NullTime
		ackedBy               sql.NullInt64
	)
	err := s.Scan(&e.ID, &ruleID, &targetType, &targetID, &e.TargetName, &severity, &status,
		&msg, &payload, &notified, &firstFired, &lastFired,
		&resolvedAt, &ackedAt, &ackedBy)
	if err != nil {
		return nil, err
	}
	if ruleID.Valid {
		v := ruleID.Int64
		e.RuleID = &v
	}
	if targetID.Valid {
		v := targetID.Int64
		e.TargetID = &v
	}
	if ackedBy.Valid {
		v := ackedBy.Int64
		e.AckedBy = &v
	}
	e.TargetType = model.AlertTargetType(targetType)
	e.Severity = model.Severity(severity)
	e.Status = model.EventStatus(status)
	e.Message = msg
	e.Notified = notified
	e.FirstFiredAt, e.LastFiredAt = firstFired, lastFired
	e.ResolvedAt = sqlbase.TimePtr(resolvedAt)
	e.AckedAt = sqlbase.TimePtr(ackedAt)
	sqlbase.ScanJSON(payload, &e.Payload)
	return &e, nil
}

func (r *alertRepo) AckEvent(ctx context.Context, id, userID int64) error {
	q := `UPDATE alert_events SET status = 'acked', acked_at = ?, acked_by = ? WHERE id = ?`
	res, _ := r.db.ExecContext(ctx, r.d.Rebind(q), sqlbase.Now(), userID, id)
	return checkAffected(res, "告警事件不存在")
}

func (r *alertRepo) CountRecentForTarget(ctx context.Context, targetType model.AlertTargetType, targetID int64, since time.Time) (int, error) {
	// 时间归一到 UTC：SQLite 按字面量比较时间值，
	// 传本地时区会静默匹配 0 行（详见 sqlbase.UTC）。
	since = sqlbase.UTC(since)

	q := `SELECT COUNT(*) FROM alert_events
	      WHERE target_type = ? AND COALESCE(target_id, -1) = COALESCE(?, -1) AND first_fired_at > ?`
	var n int
	err := r.db.QueryRowContext(ctx, r.d.Rebind(q), string(targetType), targetID, since).Scan(&n)
	return n, err
}

func (r *alertRepo) RecentFiredForTarget(ctx context.Context, targetType model.AlertTargetType, targetID int64, since, until time.Time) ([]model.AlertEvent, error) {
	// 时间归一到 UTC：SQLite 按字面量比较时间值，
	// 传本地时区会静默匹配 0 行（详见 sqlbase.UTC）。
	since, until = sqlbase.UTCRange(since, until)

	q := `SELECT id, rule_id, target_type, target_id, target_name, severity, status,
		COALESCE(message,''), payload, notified, first_fired_at, last_fired_at,
		resolved_at, acked_at, acked_by
		FROM alert_events
		WHERE target_type = ? AND COALESCE(target_id, -1) = COALESCE(?, -1)
		  AND first_fired_at > ? AND first_fired_at <= ?
		ORDER BY first_fired_at`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), string(targetType), targetID, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.AlertEvent
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (r *alertRepo) MarkNotified(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, r.d.Rebind(`UPDATE alert_events SET notified = 1 WHERE id = ?`), id)
	return err
}

func (r *alertRepo) PurgeEvents(ctx context.Context, before time.Time) (int64, error) {
	// 时间归一到 UTC：SQLite 按字面量比较时间值，
	// 传本地时区会静默匹配 0 行（详见 sqlbase.UTC）。
	before = sqlbase.UTC(before)

	res, err := r.db.ExecContext(ctx, r.d.Rebind(
		`DELETE FROM alert_events WHERE status IN ('resolved','acked') AND resolved_at < ?`), before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------- 通知通道 ----------

type channelRepo struct {
	db *sql.DB
	d  sqlbase.Dialect
}

func (r *channelRepo) Create(ctx context.Context, c *model.AlertChannel) (int64, error) {
	q := `INSERT INTO alert_channels (name, type, config, enabled, created_at) VALUES (?,?,?,?,?)`
	res, err := r.db.ExecContext(ctx, r.d.Rebind(q),
		c.Name, string(c.Type), sqlbase.MarshalJSON(c.Config), c.Enabled, sqlbase.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *channelRepo) GetByID(ctx context.Context, id int64) (*model.AlertChannel, error) {
	q := `SELECT id, name, type, config, enabled, created_at FROM alert_channels WHERE id = ?`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, apperr.NotFound(apperr.CodeChannelNotFound, "通知通道不存在")
	}
	return scanChannel(rows)
}

func (r *channelRepo) List(ctx context.Context) ([]model.AlertChannel, error) {
	q := `SELECT id, name, type, config, enabled, created_at FROM alert_channels ORDER BY id`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.AlertChannel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func scanChannel(s scanner) (*model.AlertChannel, error) {
	var (
		c         model.AlertChannel
		ctype     string
		cfg       sql.NullString
		createdAt time.Time
	)
	if err := s.Scan(&c.ID, &c.Name, &ctype, &cfg, &c.Enabled, &createdAt); err != nil {
		return nil, err
	}
	c.Type = model.ChannelType(ctype)
	c.CreatedAt = createdAt
	sqlbase.ScanJSON(cfg, &c.Config)
	return &c, nil
}

func (r *channelRepo) Update(ctx context.Context, c *model.AlertChannel) error {
	q := `UPDATE alert_channels SET name = ?, type = ?, config = ?, enabled = ? WHERE id = ?`
	res, _ := r.db.ExecContext(ctx, r.d.Rebind(q),
		c.Name, string(c.Type), sqlbase.MarshalJSON(c.Config), c.Enabled, c.ID)
	return checkAffected(res, "通知通道不存在")
}

func (r *channelRepo) Delete(ctx context.Context, id int64) error {
	res, _ := r.db.ExecContext(ctx, r.d.Rebind(`DELETE FROM alert_channels WHERE id = ?`), id)
	return checkAffected(res, "通知通道不存在")
}

// ---------- 可见性策略 ----------

type visibilityRepo struct {
	db *sql.DB
	d  sqlbase.Dialect
}

func (r *visibilityRepo) Policies(ctx context.Context, scope model.VisibilityScope) ([]model.VisibilityPolicy, error) {
	q := `SELECT id, scope, field, visible, mask_mode, COALESCE(mask_rule,'')
	      FROM visibility_policies WHERE scope = ? ORDER BY field`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), string(scope))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPolicies(rows)
}

func (r *visibilityRepo) All(ctx context.Context) ([]model.VisibilityPolicy, error) {
	q := `SELECT id, scope, field, visible, mask_mode, COALESCE(mask_rule,'')
	      FROM visibility_policies ORDER BY scope, field`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPolicies(rows)
}

func scanPolicies(rows *sql.Rows) ([]model.VisibilityPolicy, error) {
	var out []model.VisibilityPolicy
	for rows.Next() {
		var (
			p               model.VisibilityPolicy
			scope, maskMode string
		)
		if err := rows.Scan(&p.ID, &scope, &p.Field, &p.Visible, &maskMode, &p.MaskRule); err != nil {
			return nil, err
		}
		p.Scope = model.VisibilityScope(scope)
		p.MaskMode = model.MaskMode(maskMode)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *visibilityRepo) Update(ctx context.Context, scope model.VisibilityScope, field string, visible bool, mode model.MaskMode, rule string) error {
	q := `UPDATE visibility_policies SET visible = ?, mask_mode = ?, mask_rule = ?
	      WHERE scope = ? AND field = ?`
	res, err := r.db.ExecContext(ctx, r.d.Rebind(q),
		visible, string(mode), nullIfEmpty(rule), string(scope), field)
	if err != nil {
		return err
	}
	return checkAffected(res, "可见性策略不存在")
}

// ---------- 审计 ----------

type auditRepo struct {
	db *sql.DB
	d  sqlbase.Dialect
}

func (r *auditRepo) Write(ctx context.Context, l *model.AuditLog) error {
	q := `INSERT INTO audit_logs (user_id, username, action, target_type, target_id,
		detail, ip, user_agent, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`
	_, err := r.db.ExecContext(ctx, r.d.Rebind(q),
		l.UserID, nullIfEmpty(l.Username), l.Action,
		nullIfEmpty(l.TargetType), nullIfEmpty(l.TargetID),
		nullIfEmpty(sqlbase.MarshalJSON(l.Detail)),
		nullIfEmpty(l.IP), nullIfEmpty(l.UserAgent), sqlbase.Now())
	return err
}

func (r *auditRepo) List(ctx context.Context, userID *int64, action, targetType, targetID string, from, to time.Time, limit, offset int) ([]model.AuditLog, int, error) {
	// 时间归一到 UTC：SQLite 按字面量比较时间值，
	// 传本地时区会静默匹配 0 行（详见 sqlbase.UTC）。
	from, to = sqlbase.UTCRange(from, to)

	where, args := auditFilter(userID, action, targetType, targetID, from, to)
	q := `SELECT id, user_id, COALESCE(username,''), action, COALESCE(target_type,''),
		COALESCE(target_id,''), detail, COALESCE(ip,''), COALESCE(user_agent,''), created_at
		FROM audit_logs` + where

	var total int
	if err := r.db.QueryRowContext(ctx, r.d.Rebind(`SELECT COUNT(*) FROM audit_logs`+where), args...).
		Scan(&total); err != nil {
		return nil, 0, err
	}

	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	q += ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []model.AuditLog
	for rows.Next() {
		var (
			l                  model.AuditLog
			userID             sql.NullInt64
			tType, tID, detail sql.NullString
			ip, ua             sql.NullString
			createdAt          time.Time
		)
		if err := rows.Scan(&l.ID, &userID, &l.Username, &l.Action, &tType, &tID,
			&detail, &ip, &ua, &createdAt); err != nil {
			return nil, 0, err
		}
		if userID.Valid {
			v := userID.Int64
			l.UserID = &v
		}
		l.TargetType, l.TargetID = tType.String, tID.String
		l.IP, l.UserAgent = ip.String, ua.String
		l.CreatedAt = createdAt
		sqlbase.ScanJSON(detail, &l.Detail)
		out = append(out, l)
	}
	return out, total, rows.Err()
}

// auditFilter 生成 WHERE 子句与参数，供 Count 与 List 共用。
func auditFilter(userID *int64, action, targetType, targetID string, from, to time.Time) (string, []any) {
	q := ` WHERE 1=1`
	var args []any
	if userID != nil {
		q += ` AND user_id = ?`
		args = append(args, *userID)
	}
	if action != "" {
		q += ` AND action = ?`
		args = append(args, action)
	}
	if targetType != "" {
		q += ` AND target_type = ?`
		args = append(args, targetType)
	}
	if targetID != "" {
		q += ` AND target_id = ?`
		args = append(args, targetID)
	}
	if !from.IsZero() {
		q += ` AND created_at >= ?`
		args = append(args, from)
	}
	if !to.IsZero() {
		q += ` AND created_at <= ?`
		args = append(args, to)
	}
	return q, args
}

func (r *auditRepo) Purge(ctx context.Context, before time.Time) (int64, error) {
	// 时间归一到 UTC：SQLite 按字面量比较时间值，
	// 传本地时区会静默匹配 0 行（详见 sqlbase.UTC）。
	before = sqlbase.UTC(before)

	res, err := r.db.ExecContext(ctx, r.d.Rebind(`DELETE FROM audit_logs WHERE created_at < ?`), before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------- 设置 ----------

type settingsRepo struct {
	db *sql.DB
	d  sqlbase.Dialect
}

func (r *settingsRepo) Get(ctx context.Context, key string) (any, bool, error) {
	var raw sql.NullString
	err := r.db.QueryRowContext(ctx, r.d.Rebind(`SELECT value FROM settings WHERE key = ?`), key).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var v any
	if !sqlbase.ScanJSON(raw, &v) {
		return nil, false, nil
	}
	return v, true, nil
}

func (r *settingsRepo) Set(ctx context.Context, key string, value any) error {
	q := `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
	      ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`
	_, err := r.db.ExecContext(ctx, r.d.Rebind(q), key, sqlbase.MarshalJSON(value), sqlbase.Now())
	return err
}

func (r *settingsRepo) All(ctx context.Context) (map[string]any, error) {
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(`SELECT key, value FROM settings`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]any)
	for rows.Next() {
		var k string
		var raw sql.NullString
		if err := rows.Scan(&k, &raw); err != nil {
			return nil, err
		}
		var v any
		if sqlbase.ScanJSON(raw, &v) {
			out[k] = v
		}
	}
	return out, rows.Err()
}
