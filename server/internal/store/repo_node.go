package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/m202471895/probeone/server/internal/apperr"
	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/store/sqlbase"
)

type nodeRepo struct {
	db *sql.DB
	d  sqlbase.Dialect
}

const nodeColumns = `id, uid, name, group_id, agent_secret,
	COALESCE(hostname,''), COALESCE(os_type,''), COALESCE(os_version,''),
	COALESCE(arch,''), COALESCE(agent_version,''),
	COALESCE(cpu_model,''), COALESCE(cpu_cores,0), COALESCE(mem_total,0),
	disk_info, COALESCE(hardware_fp,''), hardware_changed_at,
	boot_time, COALESCE(public_ip,''), COALESCE(geo_country,''), COALESCE(geo_city,''),
	last_seen_at, last_report_at, COALESCE(remark,''), is_public, status,
	created_at, updated_at`

func (r *nodeRepo) Create(ctx context.Context, in CreateNodeInput) (int64, error) {
	q := `INSERT INTO nodes (uid, name, group_id, agent_secret, status, created_at, updated_at)
	      VALUES (?, ?, ?, ?, 'pending', ?, ?)`
	var gid any
	if in.GroupID != nil {
		gid = *in.GroupID
	}
	res, err := r.db.ExecContext(ctx, r.d.Rebind(q),
		in.UID, in.Name, gid, in.SecretHash, sqlbase.Now(), sqlbase.Now())
	if err != nil {
		if isUniqueViolation(err) {
			return 0, apperr.Conflict(apperr.CodeNodeExists, "节点 UID 已存在")
		}
		return 0, fmt.Errorf("创建节点失败: %w", err)
	}
	return res.LastInsertId()
}

func (r *nodeRepo) GetByID(ctx context.Context, id int64) (*model.Node, error) {
	return r.getOne(ctx, `id = ?`, id)
}

func (r *nodeRepo) GetByUID(ctx context.Context, uid string) (*model.Node, error) {
	return r.getOne(ctx, `uid = ?`, uid)
}

func (r *nodeRepo) getOne(ctx context.Context, where string, args ...any) (*model.Node, error) {
	q := `SELECT ` + nodeColumns + ` FROM nodes WHERE ` + where
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, apperr.NotFound(apperr.CodeNodeNotFound, "节点不存在")
	}
	n, err := scanNode(rows)
	if err != nil {
		return nil, err
	}
	return n, nil
}

// scanner 抽象 *sql.Row 与 *sql.Rows 的共同扫描能力。
type scanner interface {
	Scan(dest ...any) error
}

func scanNode(s scanner) (*model.Node, error) {
	var (
		n                           model.Node
		diskInfo                    sql.NullString
		groupID                     sql.NullInt64
		hardwareChangedAt, bootTime sql.NullTime
		lastSeenAt, lastReportAt    sql.NullTime
		status                      string
		createdAt, updatedAt        time.Time
	)
	err := s.Scan(
		&n.ID, &n.UID, &n.Name, &groupID, &n.AgentSecretHash,
		&n.Hostname, &n.OSType, &n.OSVersion, &n.Arch, &n.AgentVersion,
		&n.CPUModel, &n.CPUCores, &n.MemTotal,
		&diskInfo, &n.HardwareFP, &hardwareChangedAt,
		&bootTime, &n.PublicIP, &n.GeoCountry, &n.GeoCity,
		&lastSeenAt, &lastReportAt, &n.Remark, &n.IsPublic, &status,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	if groupID.Valid {
		v := groupID.Int64
		n.GroupID = &v
	}
	n.HardwareChangedAt = sqlbase.TimePtr(hardwareChangedAt)
	n.BootTime = sqlbase.TimePtr(bootTime)
	n.LastSeenAt = sqlbase.TimePtr(lastSeenAt)
	n.LastReportAt = sqlbase.TimePtr(lastReportAt)
	n.Status = model.NodeStatus(status)
	n.CreatedAt, n.UpdatedAt = createdAt, updatedAt
	sqlbase.ScanJSON(diskInfo, &n.DiskInfo)
	return &n, nil
}

func (r *nodeRepo) List(ctx context.Context, f NodeListFilter) ([]model.Node, int, error) {
	var (
		conds []string
		args  []any
	)
	if f.GroupID != nil {
		conds = append(conds, `group_id = ?`)
		args = append(args, *f.GroupID)
	}
	if f.Status != "" {
		conds = append(conds, `status = ?`)
		args = append(args, string(f.Status))
	}
	if f.Keyword != "" {
		// 模糊搜索名称、主机名、公网 IP
		kw := "%" + strings.ToLower(f.Keyword) + "%"
		conds = append(conds, `(lower(name) LIKE ? OR lower(COALESCE(hostname,'')) LIKE ? OR COALESCE(public_ip,'') LIKE ?)`)
		args = append(args, kw, kw, kw)
	}
	if f.PublicOnly {
		// 状态页专用：只返回已公开且**在线**的节点。
		//
		// 用 status = 'online' 白名单而非 status != 'offline' 黑名单。
		// 黑名单会把 pending（刚添加、Agent 还没连上）也算进来，
		// 于是"新建但未上线的公开节点"会短暂出现在状态页上——
		// 同样构成资产侧信道（PRD T16）。白名单语义更准：
		// 只有真正在跑且已公开的节点才对外可见。
		conds = append(conds, `is_public = 1`, `status = 'online'`)
	}

	where := ""
	if len(conds) > 0 {
		where = ` WHERE ` + strings.Join(conds, ` AND `)
	}

	// 总数
	var total int
	if err := r.db.QueryRowContext(ctx, r.d.Rebind(`SELECT COUNT(*) FROM nodes`+where), args...).
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
	q := `SELECT ` + nodeColumns + ` FROM nodes` + where +
		` ORDER BY status = 'offline', name LIMIT ? OFFSET ?`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]model.Node, 0, limit)
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *n)
	}
	return out, total, rows.Err()
}

func (r *nodeRepo) Update(ctx context.Context, n *model.Node) error {
	q := `UPDATE nodes SET
		name = ?, group_id = ?,
		hostname = ?, os_type = ?, os_version = ?, arch = ?, agent_version = ?,
		cpu_model = ?, cpu_cores = ?, mem_total = ?,
		disk_info = ?, hardware_fp = ?, hardware_changed_at = ?,
		boot_time = ?, public_ip = ?, geo_country = ?, geo_city = ?,
		remark = ?, is_public = ?, updated_at = ?
		WHERE id = ?`
	_, err := r.db.ExecContext(ctx, r.d.Rebind(q),
		n.Name, n.GroupID,
		nullIfEmpty(n.Hostname), nullIfEmpty(n.OSType), nullIfEmpty(n.OSVersion),
		nullIfEmpty(n.Arch), nullIfEmpty(n.AgentVersion),
		nullIfEmpty(n.CPUModel), n.CPUCores, n.MemTotal,
		nullIfEmpty(sqlbase.MarshalJSON(n.DiskInfo)), nullIfEmpty(n.HardwareFP),
		sqlbase.NullTimePtr(n.HardwareChangedAt),
		sqlbase.NullTimePtr(n.BootTime), nullIfEmpty(n.PublicIP),
		nullIfEmpty(n.GeoCountry), nullIfEmpty(n.GeoCity),
		nullIfEmpty(n.Remark), n.IsPublic, sqlbase.Now(),
		n.ID)
	return err
}

func (r *nodeRepo) Delete(ctx context.Context, id int64) error {
	res, _ := r.db.ExecContext(ctx, r.d.Rebind(`DELETE FROM nodes WHERE id = ?`), id)
	return checkAffected(res, "节点不存在")
}

func (r *nodeRepo) RotateSecret(ctx context.Context, id int64, newHash string) error {
	res, _ := r.db.ExecContext(ctx,
		r.d.Rebind(`UPDATE nodes SET agent_secret = ?, updated_at = ? WHERE id = ?`),
		newHash, sqlbase.Now(), id)
	return checkAffected(res, "节点不存在")
}

func (r *nodeRepo) Count(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes`).Scan(&n)
	return n, err
}

func (r *nodeRepo) TouchReport(ctx context.Context, id int64, at time.Time) error {
	q := `UPDATE nodes SET last_report_at = ?, last_seen_at = ?,
		status = CASE WHEN status = 'offline' THEN 'online' ELSE status END,
		updated_at = ? WHERE id = ?`
	_, err := r.db.ExecContext(ctx, r.d.Rebind(q), at, at, at, id)
	return err
}

func (r *nodeRepo) MarkOffline(ctx context.Context, before time.Time) (int64, error) {
	// 宽限时间由调用方算好后传入。这里只把超时未上报的**在线**节点改离线，
	// 已离线的重复处理没有意义。
	q := `UPDATE nodes SET status = 'offline', updated_at = ?
	      WHERE status = 'online' AND (last_report_at IS NULL OR last_report_at < ?)`
	res, err := r.db.ExecContext(ctx, r.d.Rebind(q), sqlbase.Now(), before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------- 分组 ----------

func (r *nodeRepo) ListGroups(ctx context.Context) ([]model.NodeGroup, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, sort FROM node_groups ORDER BY sort, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.NodeGroup
	for rows.Next() {
		var g model.NodeGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Sort); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (r *nodeRepo) CreateGroup(ctx context.Context, name string, sort int) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		r.d.Rebind(`INSERT INTO node_groups (name, sort, created_at) VALUES (?, ?, ?)`),
		strings.TrimSpace(name), sort, sqlbase.Now())
	if err != nil {
		if isUniqueViolation(err) {
			return 0, apperr.Conflict("GROUP_EXISTS", "分组名称已存在")
		}
		return 0, err
	}
	return res.LastInsertId()
}

func (r *nodeRepo) UpdateGroup(ctx context.Context, id int64, name string, sort int) error {
	res, _ := r.db.ExecContext(ctx,
		r.d.Rebind(`UPDATE node_groups SET name = ?, sort = ? WHERE id = ?`),
		strings.TrimSpace(name), sort, id)
	return checkAffected(res, "分组不存在")
}

func (r *nodeRepo) DeleteGroup(ctx context.Context, id int64) error {
	res, _ := r.db.ExecContext(ctx, r.d.Rebind(`DELETE FROM node_groups WHERE id = ?`), id)
	return checkAffected(res, "分组不存在")
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
