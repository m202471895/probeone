package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/m202471895/probeone/server/internal/apperr"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/store/sqlbase"
)

// ---------- 网站监控 ----------

type monitorRepo struct {
	db *sql.DB
	d  sqlbase.Dialect
}

const monitorColumns = `id, name, type, target, config, interval_sec, timeout_sec, group_id,
	status, is_public, sort, last_checked_at, uptime_30d, avg_latency_ms, created_at, updated_at`

func (r *monitorRepo) Create(ctx context.Context, m *model.Monitor) (int64, error) {
	q := `INSERT INTO monitors (name, type, target, config, interval_sec, timeout_sec, group_id,
		status, is_public, sort, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`
	var gid any
	if m.GroupID != nil {
		gid = *m.GroupID
	}
	res, err := r.db.ExecContext(ctx, r.d.Rebind(q),
		m.Name, string(m.Type), m.Target, sqlbase.MarshalJSON(m.Config),
		m.IntervalSec, m.TimeoutSec, gid, string(m.Status), m.IsPublic, m.Sort,
		sqlbase.Now(), sqlbase.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *monitorRepo) GetByID(ctx context.Context, id int64) (*model.Monitor, error) {
	q := `SELECT ` + monitorColumns + ` FROM monitors WHERE id = ?`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, apperr.NotFound(apperr.CodeMonitorNotFound, "监控不存在")
	}
	return scanMonitor(rows)
}

func scanMonitor(s scanner) (*model.Monitor, error) {
	var (
		m                    model.Monitor
		cfgJSON              sql.NullString
		groupID              sql.NullInt64
		status, mtype        string
		lastChecked          sql.NullTime
		uptime, avgLatency   sql.NullFloat64
		createdAt, updatedAt time.Time
	)
	err := s.Scan(&m.ID, &m.Name, &mtype, &m.Target, &cfgJSON, &m.IntervalSec, &m.TimeoutSec,
		&groupID, &status, &m.IsPublic, &m.Sort, &lastChecked, &uptime, &avgLatency,
		&createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	if groupID.Valid {
		v := groupID.Int64
		m.GroupID = &v
	}
	m.Type = model.MonitorType(mtype)
	m.Status = model.MonitorStatus(status)
	m.LastCheckedAt = sqlbase.TimePtr(lastChecked)
	if uptime.Valid {
		v := uptime.Float64
		m.Uptime30d = &v
	}
	if avgLatency.Valid {
		v := int(avgLatency.Float64)
		m.AvgLatencyMs = &v
	}
	m.CreatedAt, m.UpdatedAt = createdAt, updatedAt
	sqlbase.ScanJSON(cfgJSON, &m.Config)
	return &m, nil
}

func (r *monitorRepo) List(ctx context.Context, f MonitorListFilter) ([]model.Monitor, int, error) {
	var (
		conds []string
		args  []any
	)
	if f.GroupID != nil {
		conds = append(conds, `group_id = ?`)
		args = append(args, *f.GroupID)
	}
	if f.Type != "" {
		conds = append(conds, `type = ?`)
		args = append(args, string(f.Type))
	}
	if f.Status != "" {
		conds = append(conds, `status = ?`)
		args = append(args, string(f.Status))
	}
	if f.Keyword != "" {
		kw := "%" + strings.ToLower(f.Keyword) + "%"
		conds = append(conds, `(lower(name) LIKE ? OR lower(target) LIKE ?)`)
		args = append(args, kw, kw)
	}
	if f.PublicOnly {
		conds = append(conds, `is_public = 1`)
	}
	where := ""
	if len(conds) > 0 {
		where = ` WHERE ` + strings.Join(conds, ` AND `)
	}

	var total int
	if err := r.db.QueryRowContext(ctx, r.d.Rebind(`SELECT COUNT(*) FROM monitors`+where), args...).
		Scan(&total); err != nil {
		return nil, 0, err
	}

	limit, offset := f.Limit, f.Offset
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	q := `SELECT ` + monitorColumns + ` FROM monitors` + where + ` ORDER BY sort, id LIMIT ? OFFSET ?`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []model.Monitor
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *m)
	}
	return out, total, rows.Err()
}

func (r *monitorRepo) Update(ctx context.Context, m *model.Monitor) error {
	q := `UPDATE monitors SET name = ?, target = ?, config = ?, interval_sec = ?, timeout_sec = ?,
		group_id = ?, is_public = ?, sort = ?, updated_at = ? WHERE id = ?`
	_, err := r.db.ExecContext(ctx, r.d.Rebind(q),
		m.Name, m.Target, sqlbase.MarshalJSON(m.Config), m.IntervalSec, m.TimeoutSec,
		m.GroupID, m.IsPublic, m.Sort, sqlbase.Now(), m.ID)
	return err
}

func (r *monitorRepo) Delete(ctx context.Context, id int64) error {
	res, _ := r.db.ExecContext(ctx, r.d.Rebind(`DELETE FROM monitors WHERE id = ?`), id)
	return checkAffected(res, "监控不存在")
}

func (r *monitorRepo) UpdateStatus(ctx context.Context, id int64, status model.MonitorStatus, checkedAt time.Time, latencyMs *int) error {
	// 时间归一到 UTC：SQLite 按字面量比较时间值，
	// 传本地时区会静默匹配 0 行（详见 sqlbase.UTC）。
	checkedAt = sqlbase.UTC(checkedAt)

	q := `UPDATE monitors SET status = ?, last_checked_at = ?, avg_latency_ms = COALESCE(?, avg_latency_ms),
		updated_at = ? WHERE id = ?`
	_, err := r.db.ExecContext(ctx, r.d.Rebind(q), string(status), checkedAt, latencyMs, sqlbase.Now(), id)
	return err
}

func (r *monitorRepo) DueMonitors(ctx context.Context, now time.Time, limit int) ([]model.Monitor, error) {
	// 时间归一到 UTC：SQLite 按字面量比较时间值，
	// 传本地时区会静默匹配 0 行（详见 sqlbase.UTC）。
	now = sqlbase.UTC(now)

	if limit <= 0 {
		limit = 100
	}
	// 用秒级 Unix 时间比较，避免跨库的日期函数方言差异。
	// last_checked_at 为空的监控立即到期。
	q := `SELECT ` + monitorColumns + ` FROM monitors
		WHERE status != 'paused'
		  AND (last_checked_at IS NULL OR last_checked_at + interval_sec <= ?)
		ORDER BY COALESCE(last_checked_at, 0) LIMIT ?`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Monitor
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (r *monitorRepo) InsertResult(ctx context.Context, res *model.MonitorResult) (int64, error) {
	q := `INSERT INTO monitor_results (monitor_id, checked_at, ok, reason, status_code,
		latency_ms, dns_ms, tcp_ms, tls_ms, ttfb_ms, error_detail)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`
	r2, err := r.db.ExecContext(ctx, r.d.Rebind(q),
		res.MonitorID, res.CheckedAt, res.OK, string(res.Reason), res.StatusCode,
		res.LatencyMs, res.DNSMs, res.TCPMs, res.TLSMs, res.TTFBMs,
		nullIfEmpty(res.ErrorDetail))
	if err != nil {
		return 0, err
	}
	return r2.LastInsertId()
}

func (r *monitorRepo) Results(ctx context.Context, monitorID int64, from, to time.Time, limit int) ([]model.MonitorResult, error) {
	// 时间归一到 UTC：SQLite 按字面量比较时间值，
	// 传本地时区会静默匹配 0 行（详见 sqlbase.UTC）。
	from, to = sqlbase.UTCRange(from, to)

	if limit <= 0 || limit > 100000 {
		limit = 20000
	}
	q := `SELECT id, monitor_id, checked_at, ok, COALESCE(reason,''), status_code,
		latency_ms, dns_ms, tcp_ms, tls_ms, ttfb_ms, COALESCE(error_detail,'')
		FROM monitor_results
		WHERE monitor_id = ? AND checked_at >= ? AND checked_at <= ?
		ORDER BY checked_at LIMIT ?`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), monitorID, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.MonitorResult
	for rows.Next() {
		var (
			res                          model.MonitorResult
			reason                       string
			statusCode                   sql.NullInt64
			latency, dns, tcp, tls, ttfb sql.NullInt64
		)
		if err := rows.Scan(&res.ID, &res.MonitorID, &res.CheckedAt, &res.OK, &reason,
			&statusCode, &latency, &dns, &tcp, &tls, &ttfb, &res.ErrorDetail); err != nil {
			return nil, err
		}
		res.Reason = model.FailReason(reason)
		if statusCode.Valid {
			v := int(statusCode.Int64)
			res.StatusCode = &v
		}
		opt := func(n sql.NullInt64) *int {
			if !n.Valid {
				return nil
			}
			v := int(n.Int64)
			return &v
		}
		res.LatencyMs, res.DNSMs, res.TCPMs, res.TLSMs, res.TTFBMs =
			opt(latency), opt(dns), opt(tcp), opt(tls), opt(ttfb)
		out = append(out, res)
	}
	return out, rows.Err()
}

// Stats 计算可用率与延迟分位数。
//
// 分位数在 Go 侧算而不是交给SQL：不同数据库的 percentile 函数
// 差异大且版本要求高（SQLite 根本没有），放在 Go 里行为一致且好测。
func (r *monitorRepo) Stats(ctx context.Context, monitorID int64, from, to time.Time) (Stats, error) {
	// 时间归一到 UTC：SQLite 按字面量比较时间值，
	// 传本地时区会静默匹配 0 行（详见 sqlbase.UTC）。
	from, to = sqlbase.UTCRange(from, to)

	var st Stats
	q := `SELECT ok, latency_ms FROM monitor_results
		WHERE monitor_id = ? AND checked_at >= ? AND checked_at <= ? ORDER BY checked_at`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), monitorID, from, to)
	if err != nil {
		return st, err
	}
	defer rows.Close()

	var latencies []int
	for rows.Next() {
		var ok bool
		var lat sql.NullInt64
		if err := rows.Scan(&ok, &lat); err != nil {
			return st, err
		}
		st.Total++
		if ok {
			st.Up++
		}
		if lat.Valid && ok {
			latencies = append(latencies, int(lat.Int64))
		}
	}
	if err := rows.Err(); err != nil {
		return st, err
	}

	// 样本太少时不给百分比，避免误导（PRD 8.7）
	if st.Total < 10 {
		st.SampleInsufficient = true
	} else {
		st.UptimePercent = float64(st.Up) / float64(st.Total) * 100
	}

	sort.Ints(latencies)
	st.LatencyP50 = percentile(latencies, 0.50)
	st.LatencyP95 = percentile(latencies, 0.95)
	st.LatencyP99 = percentile(latencies, 0.99)
	return st, nil
}

// percentile 计算已排序切片的分位数（最近秩法）。
func percentile(sorted []int, p float64) int {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func (r *monitorRepo) AllPublic(ctx context.Context) ([]model.Monitor, error) {
	q := `SELECT ` + monitorColumns + ` FROM monitors WHERE is_public = 1 ORDER BY sort, id`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Monitor
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (r *monitorRepo) UpsertCertificate(ctx context.Context, c *model.SSLCertificate) error {
	q := `INSERT INTO ssl_certificates (monitor_id, subject, issuer, serial,
		not_before, not_after, days_left, fingerprint, last_checked_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(monitor_id) DO UPDATE SET
			subject = excluded.subject, issuer = excluded.issuer, serial = excluded.serial,
			not_before = excluded.not_before, not_after = excluded.not_after,
			days_left = excluded.days_left, fingerprint = excluded.fingerprint,
			last_checked_at = excluded.last_checked_at`
	_, err := r.db.ExecContext(ctx, r.d.Rebind(q),
		nullIfEmpty(c.Subject), nullIfEmpty(c.Issuer), nullIfEmpty(c.Serial),
		sqlbase.NullTimePtr(c.NotBefore), sqlbase.NullTimePtr(c.NotAfter),
		c.DaysLeft, nullIfEmpty(c.Fingerprint), sqlbase.Now())
	return err
}

func (r *monitorRepo) GetCertificate(ctx context.Context, monitorID int64) (*model.SSLCertificate, error) {
	q := `SELECT monitor_id, COALESCE(subject,''), COALESCE(issuer,''), COALESCE(serial,''),
		not_before, not_after, days_left, COALESCE(fingerprint,''), last_checked_at
		FROM ssl_certificates WHERE monitor_id = ?`
	var (
		c                   model.SSLCertificate
		notBefore, notAfter sql.NullTime
		daysLeft            sql.NullInt64
		lastChecked         sql.NullTime
	)
	err := r.db.QueryRowContext(ctx, r.d.Rebind(q), monitorID).
		Scan(&c.MonitorID, &c.Subject, &c.Issuer, &c.Serial, &notBefore, &notAfter,
			&daysLeft, &c.Fingerprint, &lastChecked)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, apperr.NotFound("CERT_NOT_FOUND", "证书信息不存在")
		}
		return nil, err
	}
	c.NotBefore = sqlbase.TimePtr(notBefore)
	c.NotAfter = sqlbase.TimePtr(notAfter)
	c.LastCheckedAt = sqlbase.TimePtr(lastChecked)
	if daysLeft.Valid {
		v := int(daysLeft.Int64)
		c.DaysLeft = &v
	}
	return &c, nil
}

func (r *monitorRepo) DueCertificates(ctx context.Context, now time.Time, limit int) ([]model.Monitor, error) {
	if limit <= 0 {
		limit = 100
	}
	// 证书检查频率远低于可用性检查：每天一次足够，
	// TLS 握手本身有开销，频繁检查没意义。
	q := `SELECT ` + `m.id, m.name, m.type, m.target, m.config, m.interval_sec, m.timeout_sec,
		m.group_id, m.status, m.is_public, m.sort, m.last_checked_at, m.uptime_30d, m.avg_latency_ms,
		m.created_at, m.updated_at
		FROM monitors m
		LEFT JOIN ssl_certificates c ON c.monitor_id = m.id
		WHERE m.type IN ('http','ssl')
		  AND (c.last_checked_at IS NULL OR c.last_checked_at < ?)
		LIMIT ?`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), now.Add(-24*time.Hour), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Monitor
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (r *monitorRepo) MarkCertificateNotified(ctx context.Context, monitorID int64, level string, at time.Time) error {
	// 通知等级存settings 表，避免在证书表加字段（该表只存 TLS 事实）
	key := fmt.Sprintf("cert_notified_%d", monitorID)
	return r.setSetting(ctx, key, map[string]any{"level": level, "at": at.UTC().Format(time.RFC3339)})
}

func (r *monitorRepo) setSetting(ctx context.Context, key string, value any) error {
	_, err := r.db.ExecContext(ctx, r.d.Rebind(
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`),
		key, sqlbase.MarshalJSON(value), sqlbase.Now())
	return err
}
