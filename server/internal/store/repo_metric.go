package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/m202471895/probeone/server/internal/model"
	"github.com/m202471895/probeone/server/internal/store/sqlbase"
)

type metricRepo struct {
	db *sql.DB
	d  sqlbase.Dialect
}

func (r *metricRepo) Insert(ctx context.Context, m *model.NodeMetric) (int64, error) {
	q := `INSERT INTO node_metrics
		(node_id, collected_at, cpu_usage, cpu_cores, mem_total, mem_used, mem_available, mem_usage,
		 swap_total, swap_used, load1, load5, load15, uptime, tcp_conn_count,
		 disk_usage, disk_io, net_io, sensors, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	res, err := r.db.ExecContext(ctx, r.d.Rebind(q),
		m.NodeID, m.CollectedAt, m.CPUUsage, nullIfEmpty(sqlbase.MarshalJSON(m.CPUCores)),
		m.MemTotal, m.MemUsed, m.MemAvailable, m.MemUsage,
		m.SwapTotal, m.SwapUsed, m.Load1, m.Load5, m.Load15, m.Uptime, m.TCPConnCount,
		nullIfEmpty(sqlbase.MarshalJSON(m.Disks)), nullIfEmpty(sqlbase.MarshalJSON(m.Disks)),
		nullIfEmpty(sqlbase.MarshalJSON(m.NetIO)), nullIfEmpty(sqlbase.MarshalJSON(m.Sensors)),
		sqlbase.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *metricRepo) InsertBatch(ctx context.Context, ms []model.NodeMetric) error {
	if len(ms) == 0 {
		return nil
	}
	// 显式事务：批量导入中途失败必须整体回滚，
	// 半截数据会让曲线出现无法解释的断层。
	return InTx(ctx, r.db, func(h txer) error {
		stmt, err := h.PrepareContext(ctx, r.d.Rebind(`INSERT INTO node_metrics
			(node_id, collected_at, cpu_usage, cpu_cores, mem_total, mem_used, mem_available, mem_usage,
			 swap_total, swap_used, load1, load5, load15, uptime, tcp_conn_count,
			 disk_usage, disk_io, net_io, sensors, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`))
		if err != nil {
			return err
		}
		defer stmt.Close()

		for i := range ms {
			m := &ms[i]
			if _, err := stmt.ExecContext(ctx,
				m.NodeID, m.CollectedAt, m.CPUUsage, nullIfEmpty(sqlbase.MarshalJSON(m.CPUCores)),
				m.MemTotal, m.MemUsed, m.MemAvailable, m.MemUsage,
				m.SwapTotal, m.SwapUsed, m.Load1, m.Load5, m.Load15, m.Uptime, m.TCPConnCount,
				nullIfEmpty(sqlbase.MarshalJSON(m.Disks)), nullIfEmpty(sqlbase.MarshalJSON(m.Disks)),
				nullIfEmpty(sqlbase.MarshalJSON(m.NetIO)), nullIfEmpty(sqlbase.MarshalJSON(m.Sensors)),
				sqlbase.Now()); err != nil {
				return fmt.Errorf("批量插入指标失败（第 %d 条）: %w", i, err)
			}
		}
		return nil
	})
}

func (r *metricRepo) Range(ctx context.Context, nodeID int64, from, to time.Time, limit int) ([]model.NodeMetric, error) {
	if limit <= 0 || limit > 100000 {
		limit = 20000
	}
	q := `SELECT node_id, collected_at, COALESCE(cpu_usage,0), cpu_cores,
		COALESCE(mem_total,0), COALESCE(mem_used,0), COALESCE(mem_available,0), COALESCE(mem_usage,0),
		COALESCE(swap_total,0), COALESCE(swap_used,0),
		COALESCE(load1,0), COALESCE(load5,0), COALESCE(load15,0),
		COALESCE(uptime,0), COALESCE(tcp_conn_count,0), disk_usage, net_io, sensors
		FROM node_metrics
		WHERE node_id = ? AND collected_at >= ? AND collected_at <= ?
		ORDER BY collected_at LIMIT ?`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), nodeID, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.NodeMetric
	for rows.Next() {
		var (
			m                          model.NodeMetric
			diskJSON, netJSON, senJSON sql.NullString
			coresJSON                  sql.NullString
		)
		if err := rows.Scan(&m.NodeID, &m.CollectedAt, &m.CPUUsage, &coresJSON,
			&m.MemTotal, &m.MemUsed, &m.MemAvailable, &m.MemUsage,
			&m.SwapTotal, &m.SwapUsed, &m.Load1, &m.Load5, &m.Load15,
			&m.Uptime, &m.TCPConnCount, &diskJSON, &netJSON, &senJSON); err != nil {
			return nil, err
		}
		sqlbase.ScanJSON(coresJSON, &m.CPUCores)
		sqlbase.ScanJSON(diskJSON, &m.Disks)
		sqlbase.ScanJSON(netJSON, &m.NetIO)
		sqlbase.ScanJSON(senJSON, &m.Sensors)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *metricRepo) Latest(ctx context.Context, nodeIDs []int64) (map[int64]model.NodeMetric, error) {
	if len(nodeIDs) == 0 {
		return map[int64]model.NodeMetric{}, nil
	}
	in, args := sqlbase.InClause("node_id", nodeIDs)
	q := `SELECT node_id, collected_at, COALESCE(cpu_usage,0),
		COALESCE(mem_total,0), COALESCE(mem_used,0), COALESCE(mem_available,0), COALESCE(mem_usage,0),
		COALESCE(swap_total,0), COALESCE(swap_used,0),
		COALESCE(load1,0), COALESCE(load5,0), COALESCE(load15,0),
		COALESCE(uptime,0), COALESCE(tcp_conn_count,0), disk_usage, net_io, sensors
		FROM node_metrics WHERE ` + in + `
		AND collected_at = (SELECT MAX(collected_at) FROM node_metrics m2 WHERE m2.node_id = node_metrics.node_id)`

	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64]model.NodeMetric, len(nodeIDs))
	for rows.Next() {
		var (
			m                          model.NodeMetric
			diskJSON, netJSON, senJSON sql.NullString
		)
		if err := rows.Scan(&m.NodeID, &m.CollectedAt, &m.CPUUsage,
			&m.MemTotal, &m.MemUsed, &m.MemAvailable, &m.MemUsage,
			&m.SwapTotal, &m.SwapUsed, &m.Load1, &m.Load5, &m.Load15,
			&m.Uptime, &m.TCPConnCount, &diskJSON, &netJSON, &senJSON); err != nil {
			return nil, err
		}
		sqlbase.ScanJSON(diskJSON, &m.Disks)
		sqlbase.ScanJSON(netJSON, &m.NetIO)
		sqlbase.ScanJSON(senJSON, &m.Sensors)
		out[m.NodeID] = m
	}
	return out, rows.Err()
}

func (r *metricRepo) LatestAll(ctx context.Context) (map[int64]model.NodeMetric, error) {
	q := `SELECT node_id, collected_at, COALESCE(cpu_usage,0),
		COALESCE(mem_total,0), COALESCE(mem_used,0), COALESCE(mem_available,0), COALESCE(mem_usage,0),
		COALESCE(swap_total,0), COALESCE(swap_used,0),
		COALESCE(load1,0), COALESCE(load5,0), COALESCE(load15,0),
		COALESCE(uptime,0), COALESCE(tcp_conn_count,0), disk_usage, net_io, sensors
		FROM node_metrics
		WHERE collected_at = (SELECT MAX(collected_at) FROM node_metrics m2 WHERE m2.node_id = node_metrics.node_id)`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64]model.NodeMetric, 64)
	for rows.Next() {
		var (
			m                          model.NodeMetric
			diskJSON, netJSON, senJSON sql.NullString
		)
		if err := rows.Scan(&m.NodeID, &m.CollectedAt, &m.CPUUsage,
			&m.MemTotal, &m.MemUsed, &m.MemAvailable, &m.MemUsage,
			&m.SwapTotal, &m.SwapUsed, &m.Load1, &m.Load5, &m.Load15,
			&m.Uptime, &m.TCPConnCount, &diskJSON, &netJSON, &senJSON); err != nil {
			return nil, err
		}
		sqlbase.ScanJSON(diskJSON, &m.Disks)
		sqlbase.ScanJSON(netJSON, &m.NetIO)
		sqlbase.ScanJSON(senJSON, &m.Sensors)
		out[m.NodeID] = m
	}
	return out, rows.Err()
}

func (r *metricRepo) Rollup(ctx context.Context, nodeID int64, bucket string, from, to time.Time) ([]RollupPoint, error) {
	q := `SELECT bucket, bucket_at, sample_count,
		COALESCE(cpu_avg,0), COALESCE(cpu_max,0), COALESCE(mem_avg,0), COALESCE(mem_max,0),
		COALESCE(net_rx_avg,0), COALESCE(net_rx_max,0), COALESCE(net_tx_avg,0), COALESCE(net_tx_max,0),
		COALESCE(load_avg,0), COALESCE(disk_usage_max,0)
		FROM node_metrics_rollup
		WHERE node_id = ? AND bucket = ? AND bucket_at >= ? AND bucket_at <= ?
		ORDER BY bucket_at`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), nodeID, bucket, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RollupPoint
	for rows.Next() {
		var p RollupPoint
		if err := rows.Scan(&p.Bucket, &p.BucketAt, &p.SampleCount,
			&p.CPUAvg, &p.CPUMax, &p.MemAvg, &p.MemMax,
			&p.NetRxAvg, &p.NetRxMax, &p.NetTxAvg, &p.NetTxMax,
			&p.LoadAvg, &p.DiskUsageMax); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ComputeAndStoreRollup 把原始指标聚合进预聚合表。
//
// 幂等：依赖 UNIQUE(node_id,bucket,bucket_at) + ON CONFLICT DO UPDATE，
// 重复执行不产生重复行。这很重要——后台任务可能因重启重复跑。
//
// 网络吞吐特殊处理：net_io 存的是 JSON，聚合时取各网卡求和。
func (r *metricRepo) ComputeAndStoreRollup(ctx context.Context, bucket string, bucketStart, bucketEnd time.Time) error {
	// 校验聚合粒度。截断在下方按 bucket 分支处理。
	switch bucket {
	case "1m", "1h", "1d":
	default:
		return fmt.Errorf("不支持的聚合粒度：%s", bucket)
	}

	// 磁盘最大占用率需要解 JSON 数组取 max。
	// 两种数据库的 JSON 函数不同，这里用最朴素的做法：
	// 先聚合标量列，网络与磁盘部分在 Go 侧算。
	q := `SELECT node_id, collected_at, cpu_usage, mem_usage, load1, net_io, disk_usage
		FROM node_metrics
		WHERE collected_at >= ? AND collected_at < ?`
	rows, err := r.db.QueryContext(ctx, r.d.Rebind(q), bucketStart, bucketEnd)
	if err != nil {
		return err
	}
	defer rows.Close()

	// 按 (node, 截断后的时间) 聚合
	type acc struct {
		count   int
		cpuSum  float64
		cpuMax  float64
		memSum  float64
		memMax  float64
		loadSum float64
		rxSum   int64
		rxMax   int64
		txSum   int64
		txMax   int64
		diskMax float64
	}
	buckets := make(map[string]*acc)

	for rows.Next() {
		var (
			nodeID            int64
			collectedAt       time.Time
			cpu, mem, load    sql.NullFloat64
			netJSON, diskJSON sql.NullString
		)
		if err := rows.Scan(&nodeID, &collectedAt, &cpu, &mem, &load, &netJSON, &diskJSON); err != nil {
			return err
		}

		var keyTime time.Time
		switch bucket {
		case "1m":
			keyTime = collectedAt.Truncate(time.Minute)
		case "1h":
			keyTime = collectedAt.Truncate(time.Hour)
		case "1d":
			keyTime = time.Date(collectedAt.Year(), collectedAt.Month(), collectedAt.Day(),
				0, 0, 0, 0, collectedAt.Location())
		}
		key := fmt.Sprintf("%d|%d", nodeID, keyTime.Unix())

		a, ok := buckets[key]
		if !ok {
			a = &acc{}
			buckets[key] = a
		}
		a.count++

		if cpu.Valid {
			a.cpuSum += cpu.Float64
			if cpu.Float64 > a.cpuMax {
				a.cpuMax = cpu.Float64
			}
		}
		if mem.Valid {
			a.memSum += mem.Float64
			if mem.Float64 > a.memMax {
				a.memMax = mem.Float64
			}
		}
		if load.Valid {
			a.loadSum += load.Float64
		}

		// 网络求和后取平均；因此先累加所有样本的瞬时速率
		var nets []model.NetUsage
		if sqlbase.ScanJSON(netJSON, &nets) {
			var rx, tx int64
			for _, n := range nets {
				rx += n.RxBps
				tx += n.TxBps
			}
			a.rxSum += rx
			a.txSum += tx
			if rx > a.rxMax {
				a.rxMax = rx
			}
			if tx > a.txMax {
				a.txMax = tx
			}
		}

		// 磁盘取所有分区、所有样本的最大占用率
		var disks []model.DiskUsage
		if sqlbase.ScanJSON(diskJSON, &disks) {
			for _, d := range disks {
				if d.Usage > a.diskMax {
					a.diskMax = d.Usage
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(buckets) == 0 {
		return nil
	}

	// 写入
	ins := `INSERT INTO node_metrics_rollup
		(node_id, bucket, bucket_at, sample_count, cpu_avg, cpu_max, mem_avg, mem_max,
		 net_rx_avg, net_rx_max, net_tx_avg, net_tx_max, load_avg, disk_usage_max)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(node_id, bucket, bucket_at) DO UPDATE SET
			sample_count = excluded.sample_count,
			cpu_avg = excluded.cpu_avg, cpu_max = excluded.cpu_max,
			mem_avg = excluded.mem_avg, mem_max = excluded.mem_max,
			net_rx_avg = excluded.net_rx_avg, net_rx_max = excluded.net_rx_max,
			net_tx_avg = excluded.net_tx_avg, net_tx_max = excluded.net_tx_max,
			load_avg = excluded.load_avg, disk_usage_max = excluded.disk_usage_max`

	return InTx(ctx, r.db, func(h txer) error {
		stmt, err := h.PrepareContext(ctx, r.d.Rebind(ins))
		if err != nil {
			return err
		}
		defer stmt.Close()

		for key, a := range buckets {
			parts := strings.SplitN(key, "|", 2)
			var nodeID int64
			if _, err := fmt.Sscanf(parts[0], "%d", &nodeID); err != nil {
				return err
			}
			ts, err := strconvParseInt(parts[1])
			if err != nil {
				return err
			}
			n := float64(a.count)
			if _, err := stmt.ExecContext(ctx,
				nodeID, bucket, time.Unix(ts, 0).UTC(), a.count,
				a.cpuSum/n, a.cpuMax, a.memSum/n, a.memMax,
				a.rxSum/int64(n), a.rxMax, a.txSum/int64(n), a.txMax,
				a.loadSum/n, a.diskMax); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *metricRepo) PurgeRaw(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, r.d.Rebind(`DELETE FROM node_metrics WHERE collected_at < ?`), before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *metricRepo) PurgeRollup(ctx context.Context, bucket string, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, r.d.Rebind(
		`DELETE FROM node_metrics_rollup WHERE bucket = ? AND bucket_at < ?`), bucket, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------- JSON 编解码辅助 ----------

// MarshalJSONString 把值编码为 JSON 字符串。
func MarshalJSONString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
