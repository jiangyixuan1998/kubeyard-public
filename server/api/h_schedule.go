package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/robfig/cron/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kubeyard/internal/audit"
	"kubeyard/internal/middleware"
	"kubeyard/internal/model"
)

// ---------- 定时任务（定时重启 / 定时扩缩容）----------
//
// 触发方式：once（指定时间执行一次，完成后自动停用保留记录）/ cron（5 段标准表达式周期执行）。
// 「月底一周扩缩容」用两条 cron 表达：扩容 0 0 25 * * + 缩容 0 0 1 * *（月末天数不固定，两条各管一头）。
// 调度：每分钟对齐 tick，串行执行到期任务；执行前复核创建者权限（收回则跳过并记失败）。

const restartAnnotation = "kubectl.kubernetes.io/restartedAt"

// actionOfTask 任务类型对应的细粒度动作令牌（与手动重启/扩缩容接口同一鉴权口径）
func actionOfTask(t *model.ScheduleTask) string {
	if t.Type == "scale" {
		return "replicas"
	}
	return "restart"
}

// nextRunOf 预计下次执行时间（展示用）；停用 / 已过期的 once / 坏表达式返回 nil
func nextRunOf(t *model.ScheduleTask, from time.Time) *time.Time {
	if !t.Enabled {
		return nil
	}
	switch t.Trigger {
	case "once":
		if t.RunAt != nil && t.RunAt.After(from) {
			return t.RunAt
		}
	case "cron":
		if s, err := cron.ParseStandard(t.Cron); err == nil {
			n := s.Next(from)
			return &n
		}
	}
	return nil
}

// scheduleLoop 后台调度循环：对齐到下一分钟整，每分钟扫描到期任务
func (h *H) scheduleLoop() {
	now := time.Now()
	time.Sleep(time.Until(now.Truncate(time.Minute).Add(time.Minute)))
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		h.runDueTasks()
	}
}

// runDueTasks 扫描并执行到期任务（串行，简单可靠；patch 类操作秒级完成不会堆积）
func (h *H) runDueTasks() {
	var tasks []model.ScheduleTask
	h.db.Where("enabled = ?", true).Find(&tasks)
	now := time.Now()
	for i := range tasks {
		t := &tasks[i]
		due := false
		switch t.Trigger {
		case "once":
			if t.RunAt != nil && !t.RunAt.After(now) {
				// 过期 10 分钟内补跑（服务重启等短暂窗口），超过则标记 missed 并停用
				if now.Sub(*t.RunAt) > 10*time.Minute {
					h.finishRun(t, "missed", "错过执行时间超过 10 分钟，已跳过", true)
				} else {
					due = true
				}
			}
		case "cron":
			s, err := cron.ParseStandard(t.Cron)
			if err != nil {
				continue // 坏表达式（创建时已校验），静默跳过
			}
			base := t.CreatedAt
			if t.LastRunAt != nil {
				base = *t.LastRunAt
			}
			// 上次执行后的第一个触发点已到 → 到期（服务重启错过的时间窗自然补跑一次）
			if !s.Next(base).After(now) {
				due = true
			}
		}
		if due {
			h.executeTask(t)
		}
	}
}

// executeTask 执行单个任务：先原子认领（条件更新 last_run_at 防多实例重复执行），
// 复核创建者权限后执行动作，结果回写 + 审计
func (h *H) executeTask(t *model.ScheduleTask) {
	now := time.Now()
	// 原子认领：仅当 last_run_at 仍是扫描时看到的值才执行。本地/生产两实例共用库时，
	// 两边同一分钟都扫到到期任务，先到的实例改写 last_run_at，后到的条件更新落空即跳过，
	// 避免重启/扩缩容被执行两次。手动执行同样受保护（与调度循环撞车时让给对方）。
	claimed := h.db.Model(&model.ScheduleTask{}).
		Where("id = ? AND (last_run_at = ? OR (last_run_at IS NULL AND ? IS NULL))", t.ID, t.LastRunAt, t.LastRunAt).
		Updates(map[string]any{"last_run_at": now, "last_status": "running", "last_error": ""})
	if claimed.Error != nil || claimed.RowsAffected == 0 {
		return // 已被其他实例认领（或条件竞争失败），不重复执行
	}

	var creator model.User
	if err := h.db.First(&creator, t.CreatedBy).Error; err != nil {
		creator = model.User{}
	}
	auditUID, auditUser := creator.ID, t.CreatorName
	status, errMsg := "success", ""
	terminal := t.Trigger == "once" // once 执行一次后即停用（无论成败）

	if creator.ID == 0 {
		status, errMsg = "failed", "任务创建者已不存在，任务未执行"
	} else if creator.Role != model.RoleSuperAdmin {
		// 权限复核：创建者权限被收回则跳过，不让任务带着旧权限继续跑
		switch {
		case !middleware.Allowed(h.db, creator.Role, creator.ID, t.Cluster, t.Namespace, "update", t.Resource):
			status, errMsg = "failed", "权限复核未通过：创建者已无 update 权限"
		case !middleware.ActionAllowed(h.db, creator.Role, creator.ID, t.Cluster, t.Namespace, t.Resource, actionOfTask(t)):
			status, errMsg = "failed", "权限复核未通过：创建者已无「"+actionLabelOf(t)+"」动作权限"
		}
	}
	if status == "success" {
		if err := h.doTaskAction(t); err != nil {
			status, errMsg = "failed", err.Error()
		}
	}
	h.finishRun(t, status, errMsg, terminal)

	auditStatus := "success"
	if status == "failed" {
		auditStatus = "failed"
	}
	detail := fmt.Sprintf("定时任务「%s」（%s）执行%s", t.Name, triggerDesc(t), resultDesc(status, errMsg))
	audit.Log(audit.Record(auditUID, auditUser, "schedule."+t.Type, t.Cluster, t.Namespace, t.Resource, t.ResName, detail, auditStatus))
}

// doTaskAction 对目标执行重启 / 扩缩容（与手动接口同款 patch）
func (h *H) doTaskAction(t *model.ScheduleTask) error {
	e, err := h.mgr.Get(t.Cluster)
	if err != nil {
		return fmt.Errorf("集群不可用: %w", err)
	}
	gvr := e.PreferredGVR("apps", "v1", t.Resource)
	ri := dynamicRes(e, gvr, t.Namespace)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch t.Type {
	case "restart":
		patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"%s":%q}}}}}`, restartAnnotation, time.Now().Format(time.RFC3339))
		_, err = ri.Patch(ctx, t.ResName, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	case "scale":
		patch := fmt.Sprintf(`{"spec":{"replicas":%d}}`, *t.Replicas)
		_, err = ri.Patch(ctx, t.ResName, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	}
	return err
}

// finishRun 回写执行结果；terminal=true 时停用任务（once 到期处理），并刷新 next_run_at
func (h *H) finishRun(t *model.ScheduleTask, status, errMsg string, terminal bool) {
	updates := map[string]any{"last_status": status, "last_error": errMsg}
	if terminal {
		updates["enabled"] = false
	}
	t.Enabled = !terminal
	t.LastStatus = status
	t.LastError = errMsg
	updates["next_run_at"] = nextRunOf(t, time.Now())
	h.db.Model(t).Updates(updates)
}

func triggerDesc(t *model.ScheduleTask) string {
	if t.Trigger == "cron" {
		return "cron " + t.Cron
	}
	return "单次"
}

func resultDesc(status, errMsg string) string {
	if status == "success" {
		return "成功"
	}
	if status == "missed" {
		return "跳过：" + errMsg
	}
	return "失败：" + errMsg
}

func actionLabelOf(t *model.ScheduleTask) string {
	if t.Type == "scale" {
		return "副本数"
	}
	return "重启"
}

// ---------- 接口 ----------

// ListScheduleTasks GET /api/v1/schedule-tasks — 任务列表，按用户对目标资源的 list 权限过滤
func (h *H) ListScheduleTasks(c *gin.Context) {
	role, uid := middleware.Role(c), middleware.UID(c)
	super := role == model.RoleSuperAdmin
	var grants []model.UserGrant
	if !super {
		grants = middleware.EffectiveGrants(h.db, uid)
	}
	var clusters []model.Cluster
	h.db.Find(&clusters)
	clID := map[string]uint{}
	for i := range clusters {
		clID[clusters[i].Name] = clusters[i].ID
	}
	// (集群, 资源) → 可 list 的命名空间，避免逐任务查库
	type nsList struct {
		nss []string
		all bool
		ok  bool
	}
	nsCache := map[string]nsList{}

	var tasks []model.ScheduleTask
	h.db.Order("created_at DESC").Find(&tasks)
	out := []model.ScheduleTask{}
	for i := range tasks {
		t := &tasks[i]
		if super {
			out = append(out, *t)
			continue
		}
		key := t.Cluster + "/" + t.Resource
		v, cached := nsCache[key]
		if !cached {
			nss, all, ok := middleware.ListableNSFromGrants(grants, clID[t.Cluster], t.Resource)
			v = nsList{nss, all, ok}
			nsCache[key] = v
		}
		if !v.ok {
			continue
		}
		if !v.all && !containsStr(v.nss, t.Namespace) {
			continue
		}
		out = append(out, *t)
	}
	c.JSON(http.StatusOK, out)
}

// canManageTask 管理权限（改/删/立即执行/暂停）：SuperAdmin、任务创建者、
// 或对目标有 update+对应动作权限的用户
func (h *H) canManageTask(c *gin.Context, t *model.ScheduleTask) bool {
	role, uid := middleware.Role(c), middleware.UID(c)
	if role == model.RoleSuperAdmin || uid == t.CreatedBy {
		return true
	}
	return middleware.Allowed(h.db, role, uid, t.Cluster, t.Namespace, "update", t.Resource) &&
		middleware.ActionAllowed(h.db, role, uid, t.Cluster, t.Namespace, t.Resource, actionOfTask(t))
}

// CreateScheduleTask POST /api/v1/clusters/:cluster/schedule-tasks
func (h *H) CreateScheduleTask(c *gin.Context) {
	clusterName := c.Param("cluster")
	var req struct {
		Name      string     `json:"name" binding:"required"`
		Type      string     `json:"type" binding:"required"`
		Namespace string     `json:"namespace" binding:"required"`
		Resource  string     `json:"resource" binding:"required"`
		ResName   string     `json:"res_name" binding:"required"`
		Trigger   string     `json:"trigger" binding:"required"`
		RunAt     *time.Time `json:"run_at"`
		Cron      string     `json:"cron"`
		Replicas  *int32     `json:"replicas"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误：任务名/命名空间/资源/目标必填"})
		return
	}
	if _, err := h.mgr.Get(clusterName); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	// 形态校验
	if req.Type != "restart" && req.Type != "scale" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "任务类型仅支持 restart / scale"})
		return
	}
	if !restartableResource(req.Resource) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "仅 Deployment / StatefulSet / DaemonSet 支持定时任务"})
		return
	}
	if req.Type == "scale" && req.Resource == "daemonsets" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "DaemonSet 副本由调度器按节点管理，不支持定时扩缩容"})
		return
	}
	if req.Trigger != "once" && req.Trigger != "cron" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "触发方式仅支持 once / cron"})
		return
	}
	if req.Trigger == "once" {
		if req.RunAt == nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": "单次任务需指定执行时间"})
			return
		}
		if !req.RunAt.After(time.Now()) {
			c.JSON(http.StatusBadRequest, gin.H{"message": "执行时间需晚于当前时间"})
			return
		}
	} else if _, err := cron.ParseStandard(req.Cron); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "cron 表达式无效（5 段：分 时 日 月 周），如 0 4 * * 6"})
		return
	}
	if req.Type == "scale" && (req.Replicas == nil || *req.Replicas < 0) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "扩缩容任务需指定目标副本数（≥0）"})
		return
	}
	// 权限：与手动重启 / 扩缩容同口径（update 动词 + 对应细粒度动作）
	action := "restart"
	if req.Type == "scale" {
		action = "replicas"
	}
	if !middleware.Allowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, req.Namespace, "update", req.Resource) {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "schedule.create", clusterName, req.Namespace, req.Resource, req.ResName, "权限拒绝：无 update 权限", "denied"))
		denyResource403(c, "update", req.Resource)
		return
	}
	if !middleware.ActionAllowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, req.Namespace, req.Resource, action) {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "schedule.create", clusterName, req.Namespace, req.Resource, req.ResName, "权限拒绝：未授权 "+action+" 动作", "denied"))
		c.JSON(http.StatusForbidden, gin.H{"message": "无「" + map[bool]string{true: "副本数", false: "重启"}[action == "replicas"] + "」动作权限"})
		return
	}

	t := model.ScheduleTask{
		Name: req.Name, Type: req.Type, Cluster: clusterName, Namespace: req.Namespace,
		Resource: req.Resource, ResName: req.ResName, Trigger: req.Trigger,
		RunAt: req.RunAt, Cron: req.Cron, Replicas: req.Replicas,
		Enabled: true, CreatedBy: middleware.UID(c), CreatorName: middleware.Username(c),
	}
	t.NextRunAt = nextRunOf(&t, time.Now())
	if err := h.db.Create(&t).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "创建失败: " + err.Error()})
		return
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "schedule.create", clusterName, req.Namespace, req.Resource, req.ResName,
		fmt.Sprintf("创建定时任务「%s」（%s，%s）", req.Name, map[bool]string{true: "定时重启", false: "定时扩缩容"}[req.Type == "restart"], triggerDesc(&t)), "success"))
	c.JSON(http.StatusOK, t)
}

// UpdateScheduleTask PUT /api/v1/schedule-tasks/:id — 改名/cron/时间/副本数/启停
func (h *H) UpdateScheduleTask(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var t model.ScheduleTask
	if err := h.db.First(&t, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "任务不存在"})
		return
	}
	if !h.canManageTask(c, &t) {
		c.JSON(http.StatusForbidden, gin.H{"message": "仅任务创建者、管理员或有对应权限的用户可管理该任务"})
		return
	}
	var req struct {
		Name     *string    `json:"name"`
		Cron     *string    `json:"cron"`
		RunAt    *time.Time `json:"run_at"`
		Replicas *int32     `json:"replicas"`
		Enabled  *bool      `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	wasEnabled := t.Enabled
	if req.Name != nil && *req.Name != "" {
		t.Name = *req.Name
	}
	if t.Trigger == "cron" && req.Cron != nil {
		if _, err := cron.ParseStandard(*req.Cron); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": "cron 表达式无效（5 段：分 时 日 月 周）"})
			return
		}
		t.Cron = *req.Cron
	}
	if t.Trigger == "once" && req.RunAt != nil {
		// 启用中的单次任务改到过去时间没有意义（下一轮 tick 就会判定为错过/补跑）
		if t.Enabled && !req.RunAt.After(time.Now()) {
			c.JSON(http.StatusBadRequest, gin.H{"message": "执行时间需晚于当前时间"})
			return
		}
		t.RunAt = req.RunAt
	}
	if t.Type == "scale" && req.Replicas != nil && *req.Replicas >= 0 {
		t.Replicas = req.Replicas
	}
	if req.Enabled != nil {
		t.Enabled = *req.Enabled
		// 重新启用 cron 任务时重置 last_run_at：暂停期间错过的触发点不补跑，等下一个自然周期
		if t.Enabled && !wasEnabled && t.Trigger == "cron" {
			now := time.Now()
			t.LastRunAt = &now
		}
		// 重新启用 once 任务需给出新的未来时间
		if t.Enabled && !wasEnabled && t.Trigger == "once" && (t.RunAt == nil || !t.RunAt.After(time.Now())) {
			c.JSON(http.StatusBadRequest, gin.H{"message": "单次任务已过期，请先修改执行时间为未来时间再启用"})
			return
		}
	}
	t.NextRunAt = nextRunOf(&t, time.Now())
	if err := h.db.Save(&t).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "保存失败: " + err.Error()})
		return
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "schedule.update", t.Cluster, t.Namespace, t.Resource, t.ResName,
		fmt.Sprintf("修改定时任务「%s」", t.Name), "success"))
	c.JSON(http.StatusOK, t)
}

// DeleteScheduleTask DELETE /api/v1/schedule-tasks/:id
func (h *H) DeleteScheduleTask(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var t model.ScheduleTask
	if err := h.db.First(&t, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "任务不存在"})
		return
	}
	if !h.canManageTask(c, &t) {
		c.JSON(http.StatusForbidden, gin.H{"message": "仅任务创建者、管理员或有对应权限的用户可管理该任务"})
		return
	}
	h.db.Delete(&t)
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "schedule.delete", t.Cluster, t.Namespace, t.Resource, t.ResName,
		fmt.Sprintf("删除定时任务「%s」", t.Name), "success"))
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// RunScheduleTask POST /api/v1/schedule-tasks/:id/run — 立即执行一次（不影响原调度节奏）
func (h *H) RunScheduleTask(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var t model.ScheduleTask
	if err := h.db.First(&t, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "任务不存在"})
		return
	}
	if !h.canManageTask(c, &t) {
		c.JSON(http.StatusForbidden, gin.H{"message": "仅任务创建者、管理员或有对应权限的用户可管理该任务"})
		return
	}
	manual := t // 手动执行不终态停用 once 任务（terminal=false）
	h.executeTask(&manual)
	var fresh model.ScheduleTask
	h.db.First(&fresh, t.ID)
	c.JSON(http.StatusOK, fresh)
}

// PreviewCron GET /api/v1/schedule-tasks/preview?cron=xxx — 预览未来 5 次执行时间
func (h *H) PreviewCron(c *gin.Context) {
	expr := c.Query("cron")
	s, err := cron.ParseStandard(expr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "cron 表达式无效（5 段：分 时 日 月 周），如 0 4 * * 6（每周六 04:00）"})
		return
	}
	out := []time.Time{}
	next := time.Now()
	for i := 0; i < 5; i++ {
		next = s.Next(next)
		out = append(out, next)
	}
	c.JSON(http.StatusOK, out)
}
