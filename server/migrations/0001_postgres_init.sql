-- ProbeOne 初始迁移
-- 命名：0001_init.sql
-- 约定：所有变更都通过新增迁移文件实现，不修改已发布的迁移。
--       PostgreSQL 与 SQLite 需各自维护一份，SQLite 版本做类型映射。

-- ============ 用户与鉴权 ============
CREATE TABLE IF NOT EXISTS users (
    id            BIGSERIAL PRIMARY KEY,
    username      VARCHAR(64)  NOT NULL,
    email         VARCHAR(255),
    password_hash VARCHAR(255) NOT NULL,
    role          VARCHAR(16)  NOT NULL DEFAULT 'viewer',
    status        VARCHAR(16)  NOT NULL DEFAULT 'active',
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users(lower(username));
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email ON users(lower(email)) WHERE email IS NOT NULL;

CREATE TABLE IF NOT EXISTS sessions (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash VARCHAR(128) NOT NULL,
    ip         VARCHAR(45),
    user_agent VARCHAR(255),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_token ON sessions(token_hash);
CREATE INDEX IF NOT EXISTS idx_sessions_expire ON sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);

-- ============ 节点分组 ============
CREATE TABLE IF NOT EXISTS node_groups (
    id         BIGSERIAL PRIMARY KEY,
    name       VARCHAR(64) NOT NULL,
    sort       INTEGER      NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_name ON node_groups(name);

-- ============ 节点 ============
CREATE TABLE IF NOT EXISTS nodes (
    id             BIGSERIAL PRIMARY KEY,
    uid            VARCHAR(64) UNIQUE NOT NULL,
    name           VARCHAR(128) NOT NULL,
    group_id       BIGINT REFERENCES node_groups(id) ON DELETE SET NULL,
    agent_secret   VARCHAR(255) NOT NULL,

    -- A 类·身份信息（不可变）
    hostname       VARCHAR(255),
    os_type        VARCHAR(32),
    os_version     VARCHAR(128),
    arch           VARCHAR(32),
    agent_version  VARCHAR(32),

    -- B 类·硬件规格（可随升配变化，每轮UPSERT + 变更检测）
    cpu_model           VARCHAR(255),
    cpu_cores           INTEGER,
    mem_total           BIGINT,
    disk_info           JSONB,
    hardware_fp         VARCHAR(32),
    hardware_changed_at TIMESTAMPTZ,

    -- C 类·运行时环境（每轮覆盖）
    boot_time      TIMESTAMPTZ,
    public_ip      VARCHAR(45),
    geo_country    VARCHAR(8),
    geo_city       VARCHAR(64),
    last_seen_at   TIMESTAMPTZ,
    last_report_at TIMESTAMPTZ,

    status     VARCHAR(16) NOT NULL DEFAULT 'pending',
    remark     VARCHAR(255),
    is_public  BOOLEAN     NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_nodes_group ON nodes(group_id);
CREATE INDEX IF NOT EXISTS idx_nodes_status ON nodes(status);
CREATE INDEX IF NOT EXISTS idx_nodes_created ON nodes(created_at);
CREATE INDEX IF NOT EXISTS idx_nodes_public ON nodes(is_public, status);

-- ============ 时序指标 ============
CREATE TABLE IF NOT EXISTS node_metrics (
    id             BIGSERIAL PRIMARY KEY,
    node_id        BIGINT      NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    collected_at   TIMESTAMPTZ NOT NULL,
    cpu_usage      REAL,
    cpu_cores      JSONB,
    mem_total      BIGINT,
    mem_used       BIGINT,
    mem_available  BIGINT,
    mem_usage      REAL,
    swap_total     BIGINT,
    swap_used      BIGINT,
    load1          REAL,
    load5          REAL,
    load15         REAL,
    uptime         BIGINT,
    tcp_conn_count INTEGER,
    disk_usage     JSONB,
    disk_io        JSONB,
    net_io         JSONB,
    sensors        JSONB,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_metrics_node_time ON node_metrics(node_id, collected_at DESC);
CREATE INDEX IF NOT EXISTS idx_metrics_time ON node_metrics(collected_at);

-- 预聚合表：避免大范围查询扫原始数据（PRD 8.5）
CREATE TABLE IF NOT EXISTS node_metrics_rollup (
    id            BIGSERIAL PRIMARY KEY,
    node_id       BIGINT      NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    bucket        VARCHAR(8)  NOT NULL,
    bucket_at     TIMESTAMPTZ NOT NULL,
    sample_count  INTEGER     NOT NULL DEFAULT 0,
    cpu_avg       REAL,
    cpu_max       REAL,
    mem_avg       REAL,
    mem_max       REAL,
    net_rx_avg    BIGINT,
    net_rx_max    BIGINT,
    net_tx_avg    BIGINT,
    net_tx_max    BIGINT,
    load_avg      REAL,
    disk_usage_max REAL,
    UNIQUE(node_id, bucket, bucket_at)
);
CREATE INDEX IF NOT EXISTS idx_rollup_node_time ON node_metrics_rollup(node_id, bucket, bucket_at DESC);

-- ============ 网站监控 ============
CREATE TABLE IF NOT EXISTS monitors (
    id              BIGSERIAL PRIMARY KEY,
    name            VARCHAR(128) NOT NULL,
    type            VARCHAR(16)  NOT NULL,
    target          VARCHAR(512) NOT NULL,
    config          JSONB        NOT NULL DEFAULT '{}',
    interval_sec    INTEGER      NOT NULL DEFAULT 60,
    timeout_sec     INTEGER      NOT NULL DEFAULT 10,
    group_id        BIGINT REFERENCES node_groups(id) ON DELETE SET NULL,
    status          VARCHAR(16)  NOT NULL DEFAULT 'pending',
    is_public       BOOLEAN      NOT NULL DEFAULT true,
    sort            INTEGER      NOT NULL DEFAULT 0,
    last_checked_at TIMESTAMPTZ,
    uptime_30d      REAL,
    avg_latency_ms  INTEGER,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_monitors_status ON monitors(status);
CREATE INDEX IF NOT EXISTS idx_monitors_group ON monitors(group_id);
CREATE INDEX IF NOT EXISTS idx_monitors_type ON monitors(type);

CREATE TABLE IF NOT EXISTS monitor_results (
    id           BIGSERIAL PRIMARY KEY,
    monitor_id   BIGINT      NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
    checked_at   TIMESTAMPTZ NOT NULL,
    ok           BOOLEAN     NOT NULL,
    reason       VARCHAR(32),
    status_code  INTEGER,
    latency_ms   INTEGER,
    dns_ms       INTEGER,
    tcp_ms       INTEGER,
    tls_ms       INTEGER,
    ttfb_ms      INTEGER,
    error_detail TEXT
);
CREATE INDEX IF NOT EXISTS idx_results_monitor_time ON monitor_results(monitor_id, checked_at DESC);
CREATE INDEX IF NOT EXISTS idx_results_time ON monitor_results(checked_at);

CREATE TABLE IF NOT EXISTS ssl_certificates (
    monitor_id       BIGINT PRIMARY KEY REFERENCES monitors(id) ON DELETE CASCADE,
    subject          VARCHAR(255),
    issuer           VARCHAR(255),
    serial           VARCHAR(64),
    not_before       TIMESTAMPTZ,
    not_after        TIMESTAMPTZ,
    days_left        INTEGER,
    fingerprint      VARCHAR(128),
    last_checked_at  TIMESTAMPTZ
);

-- ============ 告警 ============
CREATE TABLE IF NOT EXISTS alert_channels (
    id         BIGSERIAL PRIMARY KEY,
    name       VARCHAR(64) NOT NULL,
    type       VARCHAR(32) NOT NULL,
    config     JSONB       NOT NULL,
    enabled    BOOLEAN     NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS alert_rules (
    id                BIGSERIAL PRIMARY KEY,
    name              VARCHAR(128) NOT NULL,
    target_type       VARCHAR(16)  NOT NULL,
    target_id         BIGINT,
    metric            VARCHAR(64),
    condition         JSONB        NOT NULL,
    severity          VARCHAR(16)  NOT NULL DEFAULT 'warning',
    channel_ids       JSONB        NOT NULL,
    dedup_window_sec  INTEGER      NOT NULL DEFAULT 1800,
    enabled           BOOLEAN      NOT NULL DEFAULT true,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_rules_enabled ON alert_rules(enabled);
CREATE INDEX IF NOT EXISTS idx_rules_target ON alert_rules(target_type, target_id);

CREATE TABLE IF NOT EXISTS alert_events (
    id              BIGSERIAL PRIMARY KEY,
    rule_id         BIGINT REFERENCES alert_rules(id) ON DELETE SET NULL,
    target_type     VARCHAR(16)  NOT NULL,
    target_id       BIGINT,
    target_name     VARCHAR(128) NOT NULL,
    severity        VARCHAR(16)  NOT NULL,
    status          VARCHAR(16)  NOT NULL DEFAULT 'firing',
    message         TEXT,
    payload         JSONB,
    notified        BOOLEAN      NOT NULL DEFAULT false,
    first_fired_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_fired_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    resolved_at     TIMESTAMPTZ,
    acked_at        TIMESTAMPTZ,
    acked_by        BIGINT REFERENCES users(id)
);
CREATE INDEX IF NOT EXISTS idx_events_status_time ON alert_events(status, last_fired_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_target ON alert_events(target_type, target_id, status);
CREATE INDEX IF NOT EXISTS idx_events_dedup ON alert_events(rule_id, target_id, first_fired_at DESC);

-- 登录失败计数：暴力破解防护
CREATE TABLE IF NOT EXISTS login_attempts (
    id          BIGSERIAL PRIMARY KEY,
    identifier  VARCHAR(128) NOT NULL,
    ip          VARCHAR(45)  NOT NULL,
    success     BOOLEAN      NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_attempts_lookup ON login_attempts(identifier, ip, created_at DESC);

-- Agent 握手失败计数：按 uuid + IP 维度（PRD 7.3）
CREATE TABLE IF NOT EXISTS agent_failures (
    id         BIGSERIAL PRIMARY KEY,
    client_uuid VARCHAR(64) NOT NULL,
    ip          VARCHAR(45) NOT NULL,
    count       INTEGER     NOT NULL DEFAULT 0,
    hard_locked BOOLEAN     NOT NULL DEFAULT false,
    locked_until TIMESTAMPTZ,
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_failures_key ON agent_failures(client_uuid, ip);
CREATE INDEX IF NOT EXISTS idx_agent_failures_lock ON agent_failures(hard_locked, locked_until);

-- Agent 会话：session_id 短期有效，同 uuid 重复握手时旧 session 立即失效
CREATE TABLE IF NOT EXISTS agent_sessions (
    id           BIGSERIAL PRIMARY KEY,
    session_id   VARCHAR(64) UNIQUE NOT NULL,
    node_id      BIGINT      NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    ip           VARCHAR(45),
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ  NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_node ON agent_sessions(node_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expire ON agent_sessions(expires_at);

-- ============ 可见性策略 ============
-- 控制"哪些字段在免鉴权状态页/公开接口中可见"。
-- 任何对未认证请求者返回数据的接口，都必须经过此表的过滤（PRD 3.6）。
CREATE TABLE IF NOT EXISTS visibility_policies (
    id         BIGSERIAL PRIMARY KEY,
    scope      VARCHAR(16) NOT NULL,
    field      VARCHAR(64) NOT NULL,
    visible    BOOLEAN     NOT NULL DEFAULT false,
    mask_mode  VARCHAR(16) NOT NULL DEFAULT 'hide',
    mask_rule  VARCHAR(32),
    UNIQUE(scope, field)
);

-- 内置初始数据。硬件规格（CPU型号/核心数/内存/磁盘容量）按产品决策定为公开，
-- 商业 VPS 配置本就是公开信息；磁盘仅返回容量维度，device/label/uuid 由 DTO 剔除。
INSERT INTO visibility_policies (scope, field, visible, mask_mode, mask_rule) VALUES
  ('public_status', 'public_ip',      false, 'partial', 'country_only'),
  ('public_status', 'geo_city',       false, 'partial', 'country_only'),
  ('public_status', 'geo_country',    true,  'full',    NULL),
  ('public_status', 'hostname',       false, 'hide',    NULL),
  ('public_status', 'fqdn',           false, 'hide',    NULL),
  ('public_status', 'remark',         false, 'hide',    NULL),
  ('public_status', 'cpu_model',      true,  'full',    NULL),
  ('public_status', 'cores_logical',  true,  'full',    NULL),
  ('public_status', 'mem_total',      true,  'full',    NULL),
  ('public_status', 'disk_info',      true,  'full',    NULL),
  ('public_status', 'os_type',        true,  'full',    NULL),
  ('public_status', 'os_version',     true,  'full',    NULL),
  ('public_status', 'name',           true,  'full',    NULL),
  ('public_status', 'cpu_usage',      true,  'full',    NULL),
  ('public_status', 'mem_usage',      true,  'full',    NULL),
  ('public_status', 'uptime_30d',     true,  'full',    NULL),
  ('public_status', 'is_online',      true,  'full',    NULL),
  ('api_unauth',    'public_ip',      false, 'hide',    NULL),
  ('api_unauth',    'hostname',       false, 'hide',    NULL),
  ('api_unauth',    'geo_city',       false, 'hide',    NULL),
  ('api_unauth',    'cpu_model',      false, 'hide',    NULL),
  ('api_unauth',    'disk_info',      false, 'hide',    NULL),
  ('export',        'public_ip',      true,  'partial', 'first_two_octets')
ON CONFLICT (scope, field) DO NOTHING;

-- ============ 系统设置与审计 ============
CREATE TABLE IF NOT EXISTS settings (
    key        VARCHAR(64) PRIMARY KEY,
    value      JSONB       NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS audit_logs (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT REFERENCES users(id) ON DELETE SET NULL,
    username    VARCHAR(64),
    action      VARCHAR(64) NOT NULL,
    target_type VARCHAR(32),
    target_id   VARCHAR(64),
    detail      JSONB,
    ip          VARCHAR(45),
    user_agent  VARCHAR(255),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_audit_time ON audit_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_user ON audit_logs(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_logs(action, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_target ON audit_logs(target_type, target_id, created_at DESC);
