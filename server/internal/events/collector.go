package events

import (
	"context"
	"errors"
	"hash/fnv"
	"log"
	"sort"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kubeyard/internal/cluster"
	"kubeyard/internal/model"
)

const (
	collectInterval = 30 * time.Second // 轮询采集周期
	retention       = 7 * 24 * time.Hour
	cleanInterval   = time.Hour        // 清理周期
	batchSize       = 200              // 单批 upsert 行数（防超 MySQL 占位符上限 / max_allowed_packet）
	deadlockRetries = 5                // 死锁(1213)重试次数：瞬时锁冲突，官方建议直接重试事务
	cleanBatch      = 5000             // 过期清理单批删除行数
	wmOverlap       = 10 * time.Minute // 水位回看窗口：容忍实例间时钟偏差与边界上的 count 递增
	stateCap        = 100000           // 指纹表上限，超过则重置（下轮重写近水位窗口一次）
)

// 已写库事件指纹（cluster → event_uid → 指纹）：绝大多数事件两轮采集间没有变化，
// 跳过未变化的行可把每轮写库量从几十万降到增量级。仅在采集 goroutine 内访问，无需加锁。
var written = map[string]map[string]uint64{}

// 采集水位（cluster → 已入库事件的最新 last_time）：重启/新实例从库内播种水位，
// 只写水位之后的事件，避免启动时全量重写几十万行与其他实例/清理任务对撞死锁。
var watermarks = map[string]time.Time{}

// Start 启动事件采集：每 30s 轮询各集群事件按 (cluster, uid) upsert 入库；每小时清理超过 7 天的事件
func Start(mgr *cluster.Manager, db *gorm.DB) {
	go func() {
		collect(mgr, db)
		tick := time.NewTicker(collectInterval)
		clean := time.NewTicker(cleanInterval)
		defer tick.Stop()
		defer clean.Stop()
		for {
			select {
			case <-tick.C:
				collect(mgr, db)
			case <-clean.C:
				cleanup(db)
			}
		}
	}()
}

func collect(mgr *cluster.Manager, db *gorm.DB) {
	for _, name := range mgr.Names() {
		e, err := mgr.Get(name)
		if err != nil {
			continue
		}
		rows := []model.ClusterEvent{}
		// 单集群限 30s：某集群 API Server 挂起时不至于卡住整个采集循环
		// （循环内还串着其他集群的采集与每小时清理）
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		opts := metav1.ListOptions{Limit: 500}
		for {
			list, err := e.Client.CoreV1().Events("").List(cctx, opts)
			if err != nil {
				break
			}
			for i := range list.Items {
				if row, ok := toRow(name, &list.Items[i]); ok {
					rows = append(rows, row)
				}
			}
			if list.Continue == "" {
				break
			}
			opts.Continue = list.Continue
		}
		cancel()
		if len(rows) == 0 {
			continue
		}
		// 固定按 event_uid 排序后再分批：并发写方（本地/生产两实例共用库）加锁顺序一致，避免交叉等待死锁
		sort.Slice(rows, func(i, j int) bool { return rows[i].EventUID < rows[j].EventUID })
		state := written[name]
		if state == nil {
			state = map[string]uint64{}
			written[name] = state
			watermarks[name] = seedWatermark(db, name)
		} else if len(state) > stateCap {
			// 指纹表膨胀（长期运行）：重置，仅近水位窗口的行会重写一遍
			state = map[string]uint64{}
			written[name] = state
		}
		// 只写水位之后且新增/发生变化的行：水位之前的事件其他实例/上一轮已入库
		cut := watermarks[name].Add(-wmOverlap)
		pending := make([]model.ClusterEvent, 0, 256)
		var maxLT time.Time
		for i := range rows {
			if rows[i].LastTime.After(maxLT) {
				maxLT = rows[i].LastTime
			}
			if !rows[i].LastTime.After(cut) {
				continue
			}
			if state[rows[i].EventUID] != rowSig(&rows[i]) {
				pending = append(pending, rows[i])
			}
		}
		if maxLT.After(watermarks[name]) {
			watermarks[name] = maxLT
		}
		if len(pending) == 0 {
			continue
		}
		for i := 0; i < len(pending); i += batchSize {
			end := i + batchSize
			if end > len(pending) {
				end = len(pending)
			}
			batch := pending[i:end]
			if err := upsertEvents(db, batch); err != nil {
				// 失败的批次不记指纹，下一轮采集自然重试
				log.Printf("[WARN] 事件入库失败（集群 %s，本批 %d-%d 条，共 %d）: %v", name, i+1, end, len(pending), err)
				continue
			}
			for j := range batch {
				state[batch[j].EventUID] = rowSig(&batch[j])
			}
		}
	}
}

// seedWatermark 从库内播种水位：取该集群已入库事件的最新 last_time（无记录返回零值=全量首采）。
// 查询错误（连接抖动/超时）同样返回零值 → 该轮全量首采兜底（upsert 幂等不重复），
// 但打一条 WARN 留痕，避免「为什么这轮采集特别重」查不到原因
func seedWatermark(db *gorm.DB, cluster string) time.Time {
	var latest model.ClusterEvent
	if err := db.Where("cluster = ?", cluster).Order("last_time DESC").First(&latest).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("[WARN] 水位播种失败（集群 %s，本轮退化为全量首采）: %v", cluster, err)
		}
		return time.Time{}
	}
	return latest.LastTime
}

// upsertEvents 批量 upsert，死锁时退避重试。
// 使用静默 logger：重试期间的 Error 1213 不再经 GORM 逐次刷长日志（那正是刷屏来源），
// 全部重试耗尽才由调用方打一条紧凑 WARN。
func upsertEvents(db *gorm.DB, rows []model.ClusterEvent) error {
	var err error
	for attempt := 0; attempt < deadlockRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(50*attempt*attempt) * time.Millisecond) // 50/200/450/800ms 退避
			for j := range rows {
				rows[j].ID = 0 // 上次失败可能回填了自增主键，重置以免显式插旧 ID
			}
		}
		err = db.Session(&gorm.Session{Logger: logger.Discard}).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "cluster"}, {Name: "event_uid"}},
			DoUpdates: clause.AssignmentColumns([]string{"type", "reason", "message", "count", "last_time", "updated_at"}),
		}).Create(rows).Error
		if err == nil || !isDeadlock(err) {
			return err
		}
	}
	return err
}

// isDeadlock MySQL 1213：事务持有锁互相等待被回滚，属瞬时冲突
func isDeadlock(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1213
}

// rowSig 事件指纹：覆盖 upsert 会更新的全部字段，指纹不变则本轮无需写库
func rowSig(r *model.ClusterEvent) uint64 {
	h := fnv.New64a()
	h.Write([]byte(r.Type))
	h.Write([]byte{0})
	h.Write([]byte(r.Reason))
	h.Write([]byte{0})
	h.Write([]byte(r.Message))
	h.Write([]byte{0})
	h.Write([]byte(strconv.FormatInt(r.Count, 10)))
	h.Write([]byte{0})
	h.Write([]byte(r.LastTime.Format(time.RFC3339Nano)))
	return h.Sum64()
}

func toRow(clusterName string, ev *corev1.Event) (model.ClusterEvent, bool) {
	if ev.UID == "" {
		return model.ClusterEvent{}, false
	}
	lt := ev.LastTimestamp.Time
	if lt.IsZero() {
		lt = ev.EventTime.Time
	}
	if lt.IsZero() {
		lt = ev.FirstTimestamp.Time
	}
	if lt.IsZero() {
		lt = time.Now()
	}
	ft := ev.FirstTimestamp.Time
	if ft.IsZero() {
		ft = lt
	}
	return model.ClusterEvent{
		Cluster:         clusterName,
		EventUID:        string(ev.UID),
		Namespace:       ev.Namespace,
		Type:            ev.Type,
		Reason:          ev.Reason,
		ObjectType:      ev.InvolvedObject.Kind,
		ObjectName:      ev.InvolvedObject.Name,
		Message:         ev.Message,
		Count:           int64(ev.Count),
		SourceComponent: ev.Source.Component,
		FirstTime:       ft,
		LastTime:        lt,
	}, true
}

func cleanup(db *gorm.DB) {
	// 分批删除：一次删几十万行会长时间持锁，与采集 upsert 互相等待引发死锁；死锁同样静默重试
	cutoff := time.Now().Add(-retention)
	for {
		res := db.Session(&gorm.Session{Logger: logger.Discard}).
			Where("last_time < ?", cutoff).Limit(cleanBatch).Delete(&model.ClusterEvent{})
		if res.Error != nil {
			if isDeadlock(res.Error) {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			log.Printf("[WARN] 过期事件清理失败: %v", res.Error)
			return
		}
		if res.RowsAffected == 0 {
			return
		}
	}
}
