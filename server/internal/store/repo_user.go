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

// 用户与密码相关实现。
// 哈希在 apperr 之外的 internal/auth 包完成——存储层不关心密码学。

type userRepo struct {
	db *sql.DB
	d  sqlbase.Dialect
}

func (r *userRepo) Create(ctx context.Context, in CreateUserInput) (int64, error) {
	hash, err := HashPassword(in.Password)
	if err != nil {
		return 0, err
	}
	q := `INSERT INTO users (username, email, password_hash, role, status, created_at, updated_at)
	      VALUES (?, ?, ?, ?, ?, ?, ?)`
	var email any
	if in.Email != "" {
		email = in.Email
	}
	res, err := r.db.ExecContext(ctx, r.d.Rebind(q),
		strings.ToLower(in.Username), email, hash, string(in.Role), "active",
		sqlbase.Now(), sqlbase.Now())
	if err != nil {
		if isUniqueViolation(err) {
			return 0, apperr.Conflict(apperr.CodeUserExists, "用户名或邮箱已被占用")
		}
		return 0, fmt.Errorf("创建用户失败: %w", err)
	}
	return res.LastInsertId()
}

func (r *userRepo) GetByID(ctx context.Context, id int64) (*model.User, error) {
	return r.get(ctx, `id = ?`, id)
}

func (r *userRepo) GetByUsername(ctx context.Context, username string) (*model.User, error) {
	// 用户名统一小写存储，查询时也转小写，避免大小写绕过唯一性
	return r.get(ctx, `lower(username) = lower(?)`, username)
}

func (r *userRepo) get(ctx context.Context, where string, args ...any) (*model.User, error) {
	q := `SELECT id, username, COALESCE(email,''), password_hash, role, status, created_at, updated_at
	      FROM users WHERE ` + where
	var u model.User
	var role, status string
	var createdAt, updatedAt time.Time
	err := r.db.QueryRowContext(ctx, r.d.Rebind(q), args...).
		Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &role, &status, &createdAt, &updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, apperr.NotFound(apperr.CodeUserNotFound, "用户不存在")
		}
		return nil, err
	}
	u.Role = model.Role(role)
	u.Status = status
	u.CreatedAt, u.UpdatedAt = createdAt, updatedAt
	return &u, nil
}

func (r *userRepo) List(ctx context.Context, limit, offset int) ([]model.User, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	q := `SELECT id, username, COALESCE(email,''), password_hash, role, status, created_at, updated_at
	      FROM users ORDER BY id LIMIT ? OFFSET ?`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.User
	for rows.Next() {
		var (
			u                    model.User
			role, status         string
			createdAt, updatedAt time.Time
		)
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &role, &status, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		u.Role = model.Role(role)
		u.Status = status
		u.CreatedAt, u.UpdatedAt = createdAt, updatedAt
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r *userRepo) Update(ctx context.Context, u *model.User) error {
	q := `UPDATE users SET username = ?, email = ?, role = ?, status = ?, updated_at = ?
	      WHERE id = ?`
	res, err := r.db.ExecContext(ctx, r.d.Rebind(q),
		strings.ToLower(strings.TrimSpace(u.Username)), nullIfEmpty(u.Email),
		string(u.Role), u.Status, sqlbase.Now(), u.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return apperr.Conflict(apperr.CodeUserExists, "用户名或邮箱已被占用")
		}
		return fmt.Errorf("更新用户失败: %w", err)
	}
	return checkAffected(res, "用户不存在")
}

func (r *userRepo) UpdateRole(ctx context.Context, id int64, role model.Role) error {
	if !role.Valid() {
		return apperr.BadRequest("角色非法")
	}
	res, _ := r.db.ExecContext(ctx,
		r.d.Rebind(`UPDATE users SET role = ?, updated_at = ? WHERE id = ?`),
		string(role), sqlbase.Now(), id)
	return checkAffected(res, "用户不存在")
}

func (r *userRepo) UpdatePassword(ctx context.Context, id int64, newPassword string) error {
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	res, _ := r.db.ExecContext(ctx,
		r.d.Rebind(`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`),
		hash, sqlbase.Now(), id)
	return checkAffected(res, "用户不存在")
}

func (r *userRepo) Delete(ctx context.Context, id int64) error {
	res, _ := r.db.ExecContext(ctx, r.d.Rebind(`DELETE FROM users WHERE id = ?`), id)
	return checkAffected(res, "用户不存在")
}

func (r *userRepo) Count(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (r *userRepo) RecordLoginAttempt(ctx context.Context, identifier, ip string, success bool) error {
	q := `INSERT INTO login_attempts (identifier, ip, success, created_at) VALUES (?, ?, ?, ?)`
	_, err := r.db.ExecContext(ctx, r.d.Rebind(q), identifier, ip, success, sqlbase.Now())
	return err
}

func (r *userRepo) CountRecentFailures(ctx context.Context, identifier, ip string, since time.Time) (int, error) {
	q := `SELECT COUNT(*) FROM login_attempts
	      WHERE identifier = ? AND ip = ? AND success = 0 AND created_at > ?`
	var n int
	err := r.db.QueryRowContext(ctx, r.d.Rebind(q), identifier, ip, since).Scan(&n)
	return n, err
}

func (r *userRepo) PurgeLoginAttempts(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, r.d.Rebind(`DELETE FROM login_attempts WHERE created_at < ?`), before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------- 会话 ----------

type sessionRepo struct {
	db *sql.DB
	d  sqlbase.Dialect
}

func (r *sessionRepo) Create(ctx context.Context, userID int64, tokenHash, ip, ua string, expiresAt time.Time) (int64, error) {
	q := `INSERT INTO sessions (user_id, token_hash, ip, user_agent, expires_at, created_at)
	      VALUES (?, ?, ?, ?, ?, ?)`
	res, err := r.db.ExecContext(ctx, r.d.Rebind(q), userID, tokenHash, ip, ua, expiresAt, sqlbase.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *sessionRepo) GetByTokenHash(ctx context.Context, tokenHash string) (*model.Session, error) {
	q := `SELECT id, user_id, token_hash, COALESCE(ip,''), COALESCE(user_agent,''), expires_at, created_at
	      FROM sessions WHERE token_hash = ?`
	var s model.Session
	var ip, ua sql.NullString
	err := r.db.QueryRowContext(ctx, r.d.Rebind(q), tokenHash).
		Scan(&s.ID, &s.UserID, &s.TokenHash, &ip, &ua, &s.ExpiresAt, &s.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, apperr.NotFound(apperr.CodeSessionExpired, "会话不存在或已过期")
		}
		return nil, err
	}
	// 过期会话视为不存在，并顺手删掉
	if !s.ExpiresAt.After(sqlbase.Now()) {
		_ = r.DeleteByTokenHash(ctx, tokenHash)
		return nil, apperr.NotFound(apperr.CodeSessionExpired, "会话已过期")
	}
	s.IP = ip.String
	s.UserAgent = ua.String
	return &s, nil
}

func (r *sessionRepo) DeleteByTokenHash(ctx context.Context, tokenHash string) error {
	_, err := r.db.ExecContext(ctx, r.d.Rebind(`DELETE FROM sessions WHERE token_hash = ?`), tokenHash)
	return err
}

func (r *sessionRepo) DeleteAllForUser(ctx context.Context, userID int64) error {
	_, err := r.db.ExecContext(ctx, r.d.Rebind(`DELETE FROM sessions WHERE user_id = ?`), userID)
	return err
}

func (r *sessionRepo) PurgeExpired(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, r.d.Rebind(`DELETE FROM sessions WHERE expires_at < ?`), before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------- Agent 会话 ----------

type agentSessionRepo struct {
	db *sql.DB
	d  sqlbase.Dialect
}

func (r *agentSessionRepo) CreateSession(ctx context.Context, nodeID int64, sessionID, ip string, ttl time.Duration) error {
	// 关键安全动作：同一节点重复握手时，旧会话立即失效。
	// 否则拿到旧 session_id 的攻击者可在新会话建立后继续上报，
	// 形成并行的伪造通道（PRD 7.3 防重放）。
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, r.d.Rebind(`DELETE FROM agent_sessions WHERE node_id = ?`), nodeID); err != nil {
		return err
	}
	q := `INSERT INTO agent_sessions (session_id, node_id, ip, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`
	if _, err := tx.ExecContext(ctx, r.d.Rebind(q), sessionID, nodeID, ip, sqlbase.Now(), sqlbase.Now().Add(ttl)); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *agentSessionRepo) GetSession(ctx context.Context, sessionID string) (*model.Session, error) {
	q := `SELECT id, node_id, session_id, COALESCE(ip,''), expires_at, created_at
	      FROM agent_sessions WHERE session_id = ?`
	var s model.Session
	var ip sql.NullString
	err := r.db.QueryRowContext(ctx, r.d.Rebind(q), sessionID).
		Scan(&s.ID, &s.UserID, &s.TokenHash, &ip, &s.ExpiresAt, &s.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, apperr.Unauthenticated("会话无效，请重新握手")
		}
		return nil, err
	}
	if !s.ExpiresAt.After(sqlbase.Now()) {
		_ = r.DeleteBySessionID(ctx, sessionID)
		return nil, apperr.Unauthenticated("会话已过期，请重新握手")
	}
	s.IP = ip.String
	return &s, nil
}

func (r *agentSessionRepo) DeleteBySessionID(ctx context.Context, sessionID string) error {
	_, err := r.db.ExecContext(ctx, r.d.Rebind(`DELETE FROM agent_sessions WHERE session_id = ?`), sessionID)
	return err
}

func (r *agentSessionRepo) PurgeExpired(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, r.d.Rebind(`DELETE FROM agent_sessions WHERE expires_at < ?`), before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *agentSessionRepo) RecordAgentFailure(ctx context.Context, clientUUID, ip string) (int, error) {
	// upsert 计数：PostgreSQL 用 ON CONFLICT，SQLite 用 ON CONFLICT DO UPDATE
	// 两者语法此处兼容（SQLite 3.24+ 支持 ON CONFLICT）
	q := `INSERT INTO agent_failures (client_uuid, ip, count, hard_locked, updated_at)
	      VALUES (?, ?, 1, 0, ?)
	      ON CONFLICT(client_uuid, ip) DO UPDATE SET count = count + 1, updated_at = ?`
	now := sqlbase.Now()
	if _, err := r.db.ExecContext(ctx, r.d.Rebind(q), clientUUID, ip, now, now); err != nil {
		return 0, err
	}
	var n int
	err := r.db.QueryRowContext(ctx, r.d.Rebind(
		`SELECT count FROM agent_failures WHERE client_uuid = ? AND ip = ?`), clientUUID, ip).Scan(&n)
	return n, err
}

// SoftLock 写入软锁窗口。
//
// 单独暴露给调用方而不是让 RecordAgentFailure 内部决定：
// 阈值判定需要配置（属于服务层职责），写库属于仓储职责。
// 混在一起会让"改一次失败计数"和"改锁定策略"互相牵连。
func (r *agentSessionRepo) SoftLock(ctx context.Context, clientUUID, ip string, until time.Time) error {
	_, err := r.db.ExecContext(ctx, r.d.Rebind(
		`UPDATE agent_failures SET locked_until = ? WHERE client_uuid = ? AND ip = ?`),
		until.UTC(), clientUUID, ip)
	if err != nil {
		return fmt.Errorf("写入软锁失败: %w", err)
	}
	return nil
}

// HardLock 写入硬封禁标记与到期时间。
func (r *agentSessionRepo) HardLock(ctx context.Context, clientUUID, ip string, until time.Time) error {
	_, err := r.db.ExecContext(ctx, r.d.Rebind(
		`UPDATE agent_failures SET hard_locked = 1, locked_until = ? WHERE client_uuid = ? AND ip = ?`),
		until.UTC(), clientUUID, ip)
	if err != nil {
		return fmt.Errorf("写入硬封禁失败: %w", err)
	}
	return nil
}

// IsLocked 判断是否处于任意锁定状态（软锁或硬封禁）。
//
// 与 IsHardLocked 的区别：后者只看 hard_locked 列，
// 软锁（连续失败超阈值写的 locked_until）会被忽略。
// 握手鉴权必须用这个，否则软锁形同虚设。
//
// 返回 true 时若已过期，副作用是清零该行——
// 让封禁到期后自动恢复，不需额外的清理任务。
func (r *agentSessionRepo) IsLocked(ctx context.Context, clientUUID, ip string) (bool, error) {
	q := `SELECT count, locked_until, hard_locked FROM agent_failures
	      WHERE client_uuid = ? AND ip = ?`
	var count int
	var until sql.NullTime
	var hard bool
	err := r.db.QueryRowContext(ctx, r.d.Rebind(q), clientUUID, ip).
		Scan(&count, &until, &hard)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	now := sqlbase.Now()

	// 硬封禁优先
	if hard {
		if until.Valid && until.Time.After(now) {
			return true, nil
		}
		/*
		 * 硬封禁到期：整体清零。
		 * 这里清 count 是对的——封禁期内的失败都是明确的恶意尝试，
		 * 封禁结束就该从头开始，不该让受罚前的历史继续影响新周期。
		 */
		_, _ = r.db.ExecContext(ctx, r.d.Rebind(
			`UPDATE agent_failures SET hard_locked = 0, locked_until = NULL, count = 0
			 WHERE client_uuid = ? AND ip = ?`), clientUUID, ip)
		return false, nil
	}

	// 软锁：只看是否落在 locked_until 窗口内
	if until.Valid && until.Time.After(now) {
		return true, nil
	}

	/*
	 * 锁已过期：只清 locked_until，**绝不能动 count**。
	 *
	 * 之前这里顺带把 count 也清零了，等于每次握手都把失败计数归零——
	 * 计数永远停在 1，软锁与硬封的阈值都跨不过去，限流完全失效。
	 * 计数的清零只应该发生在握手成功（ClearAgentFailures）那一刻，
	 * 因为成功证明持有者是对的。
	 */
	if until.Valid {
		_, _ = r.db.ExecContext(ctx, r.d.Rebind(
			`UPDATE agent_failures SET locked_until = NULL
			 WHERE client_uuid = ? AND ip = ?`), clientUUID, ip)
	}
	_ = count
	return false, nil
}

func (r *agentSessionRepo) IsHardLocked(ctx context.Context, clientUUID, ip string) (bool, error) {
	q := `SELECT hard_locked FROM agent_failures WHERE client_uuid = ? AND ip = ?`
	var locked bool
	err := r.db.QueryRowContext(ctx, r.d.Rebind(q), clientUUID, ip).Scan(&locked)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	// 封禁有期限，到期自动解除
	var until sql.NullTime
	err = r.db.QueryRowContext(ctx, r.d.Rebind(
		`SELECT locked_until FROM agent_failures WHERE client_uuid = ? AND ip = ?`), clientUUID, ip).Scan(&until)
	if err != nil {
		return false, err
	}
	if !until.Valid || !until.Time.After(sqlbase.Now()) {
		_, _ = r.db.ExecContext(ctx, r.d.Rebind(
			`UPDATE agent_failures SET hard_locked = 0, locked_until = NULL, count = 0 WHERE client_uuid = ? AND ip = ?`),
			clientUUID, ip)
		return false, nil
	}
	return true, nil
}

func (r *agentSessionRepo) ClearAgentFailures(ctx context.Context, clientUUID, ip string) error {
	_, err := r.db.ExecContext(ctx, r.d.Rebind(
		`UPDATE agent_failures SET count = 0, hard_locked = 0, locked_until = NULL WHERE client_uuid = ? AND ip = ?`),
		clientUUID, ip)
	return err
}

// ---------- 内部辅助 ----------

// isUniqueViolation 判断是否唯一约束冲突。
// 两种驱动的错误字符串不同，这里用子串匹配兜底。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unique constraint") ||
		strings.Contains(s, "unique violation") ||
		strings.Contains(s, "duplicate key")
}

// checkAffected 检查更新/删除是否命中了行。
// 未命中通常意味着资源不存在，转成 404 让上层不必再查一次。
func checkAffected(res sql.Result, notFoundMsg string) error {
	if res == nil {
		return nil
	}
	n, err := res.RowsAffected()
	if err != nil {
		// 某些驱动在某些情况下拿不到影响行数，不视为错误
		return nil
	}
	if n == 0 {
		return apperr.NotFound("NOT_FOUND", notFoundMsg)
	}
	return nil
}
