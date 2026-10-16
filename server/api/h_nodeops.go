package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kubeyard/internal/audit"
	"kubeyard/internal/cluster"
	"kubeyard/internal/middleware"
)

// ---------- 节点运维：封锁/恢复调度（cordon）与排空（drain）----------

// Cordon POST /api/v1/clusters/:cluster/nodes/:name/cordon  body: {unschedulable: bool}
// 权限：nodes 的 update（与 YAML 编辑改 spec.unschedulable 同一授权，不引入新权限面）。
// drain 的前置封锁也走这里同一段逻辑（NodeDrain 内直接调 cordonNode）。
func (h *H) Cordon(c *gin.Context) {
	clusterName := c.Param("cluster")
	name := c.Param("name")
	var req struct {
		Unschedulable bool `json:"unschedulable"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	if !middleware.Allowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, "", "update", "nodes") {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "node_cordon", clusterName, "", "nodes", name, "权限拒绝", "denied"))
		c.JSON(http.StatusForbidden, gin.H{"message": "无节点调度配置权限（需 nodes 的 update 授权）"})
		return
	}
	e, err := h.mgr.Get(clusterName)
	if err != nil {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "node_cordon", clusterName, "", "nodes", name, "获取集群失败: "+err.Error(), "failed"))
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	if err := cordonNode(c.Request.Context(), e, name, req.Unschedulable); err != nil {
		detail := "封锁调度"
		if !req.Unschedulable {
			detail = "恢复调度"
		}
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "node_cordon", clusterName, "", "nodes", name, detail+"失败: "+err.Error(), "failed"))
		c.JSON(http.StatusBadGateway, gin.H{"message": "操作失败: " + err.Error()})
		return
	}
	detail := "封锁调度（unschedulable=true）"
	if !req.Unschedulable {
		detail = "恢复调度（unschedulable=false）"
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "node_cordon", clusterName, "", "nodes", name, detail, "success"))
	message := "已封锁调度"
	if !req.Unschedulable {
		message = "已恢复调度"
	}
	c.JSON(http.StatusOK, gin.H{"message": message, "unschedulable": req.Unschedulable})
}

// cordonNode 封锁/恢复节点调度（spec.unschedulable StrategicMergePatch）
func cordonNode(ctx context.Context, e *cluster.Entry, name string, unschedulable bool) error {
	patch := fmt.Sprintf(`{"spec":{"unschedulable":%t}}`, unschedulable)
	_, err := e.Client.CoreV1().Nodes().Patch(ctx, name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// drainSkip：排空计划里的跳过明细（name 为 ns/name；cluster-scoped Pod 不存在）
type drainSkip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"` // daemonset | bare | mirror | no-permission
}

// drainPod：计划中可驱逐的 Pod（uid 带给前端，evict 时回传做前置校验）
type drainPod struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
	Phase     string `json:"phase"`
}

type drainPlan struct {
	Evictable         []drainPod  `json:"evictable"`
	Skipped           []drainSkip `json:"skipped"`
	NodeUnschedulable bool        `json:"node_unschedulable"`
}

// DrainPlan POST /api/v1/clusters/:cluster/nodes/:name/drain-plan
// 只读分类预览：列出排空将驱逐的 Pod 与跳过明细，不产生任何写操作（前端据此弹二次确认）。
// 权限：nodes 的 update（与执行侧前置封锁同口径，避免「能预览却不能执行」的错位）。
func (h *H) DrainPlan(c *gin.Context) {
	clusterName := c.Param("cluster")
	name := c.Param("name")
	var req struct {
		IncludeDaemonsets bool `json:"include_daemonsets"`
		ForceBarePods     bool `json:"force_bare_pods"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	if !middleware.Allowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, "", "update", "nodes") {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "node_drain", clusterName, "", "nodes", name, "权限拒绝：无 nodes update", "denied"))
		c.JSON(http.StatusForbidden, gin.H{"message": "无节点调度配置权限（需 nodes 的 update 授权）"})
		return
	}
	e, err := h.mgr.Get(clusterName)
	if err != nil {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "node_drain", clusterName, "", "nodes", name, "获取集群失败: "+err.Error(), "failed"))
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	node, err := e.Client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "node_drain", clusterName, "", "nodes", name, "获取节点排空计划失败: "+err.Error(), "failed"))
		c.JSON(http.StatusBadGateway, gin.H{"message": "获取节点失败: " + err.Error()})
		return
	}
	podList, err := e.Client.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + name})
	if err != nil {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "node_drain", clusterName, "", "nodes", name, "获取节点 Pod 排空计划失败: "+err.Error(), "failed"))
		c.JSON(http.StatusBadGateway, gin.H{"message": "获取节点 Pod 失败: " + err.Error()})
		return
	}
	role := middleware.Role(c)
	uid := middleware.UID(c)
	plan := drainPlan{Evictable: []drainPod{}, Skipped: []drainSkip{}, NodeUnschedulable: node.Spec.Unschedulable}
	for i := range podList.Items {
		pod := &podList.Items[i]
		full := pod.Namespace + "/" + pod.Name
		// 静态 Pod 的 mirror（kubelet 上报，删除由静态清单决定，驱逐无意义）
		if _, ok := pod.Annotations[corev1.MirrorPodAnnotationKey]; ok {
			plan.Skipped = append(plan.Skipped, drainSkip{full, "mirror"})
			continue
		}
		// 分类：DS Pod / 裸 Pod 按勾选决定；其余（Deployment/StatefulSet/Job 等）可驱逐
		ownerKind := ""
		hasController := false
		for _, or := range pod.OwnerReferences {
			if or.Controller != nil && *or.Controller {
				hasController = true
				ownerKind = or.Kind
				break
			}
		}
		if !hasController && ownerKind == "" {
			for _, or := range pod.OwnerReferences {
				ownerKind = or.Kind
				break
			}
		}
		if ownerKind == "DaemonSet" && !req.IncludeDaemonsets {
			plan.Skipped = append(plan.Skipped, drainSkip{full, "daemonset"})
			continue
		}
		if !hasController && !req.ForceBarePods {
			// 无控制器托管（手动创建）：删除后不会自动重建，默认不碰
			plan.Skipped = append(plan.Skipped, drainSkip{full, "bare"})
			continue
		}
		// 逐 Pod 驱逐权限（pods delete，命名空间级）
		if !middleware.Allowed(h.db, role, uid, clusterName, pod.Namespace, "delete", "pods") {
			plan.Skipped = append(plan.Skipped, drainSkip{full, "no-permission"})
			continue
		}
		plan.Evictable = append(plan.Evictable, drainPod{
			Namespace: pod.Namespace, Name: pod.Name, UID: string(pod.UID), Phase: string(pod.Status.Phase),
		})
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "node_drain", clusterName, "", "nodes", name,
		fmt.Sprintf("生成排空计划：可驱逐 %d 个 Pod，跳过 %d 个 Pod（DaemonSet=%t，裸 Pod=%t，节点已封锁=%t）",
			len(plan.Evictable), len(plan.Skipped), req.IncludeDaemonsets, req.ForceBarePods, plan.NodeUnschedulable), "success"))
	c.JSON(http.StatusOK, plan)
}

// EvictPod POST /api/v1/clusters/:cluster/nodes/:name/evict
// 排空执行侧的单 Pod 驱逐：前端拿到计划确认后逐个调用（实现逐 Pod 动效与中止——
// 中止即前端停止发后续请求，封锁已生效，剩余 Pod 留在节点）。
// 权限：pods 的 delete（该命名空间）；并校验目标 Pod 确实在本节点且 UID 匹配，
// 防止把该端点当通用删除通道或打到别的节点。
func (h *H) EvictPod(c *gin.Context) {
	clusterName := c.Param("cluster")
	nodeName := c.Param("name")
	var req struct {
		Namespace    string `json:"namespace"`
		Name         string `json:"name"`
		UID          string `json:"uid"`
		GraceSeconds *int64 `json:"grace_seconds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Namespace == "" || req.Name == "" || req.UID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	grace := int64(30)
	if req.GraceSeconds != nil {
		// 与排空界面同口径（0-600）：防止直调 API 绕过界面上限
		if *req.GraceSeconds < 0 || *req.GraceSeconds > 600 {
			c.JSON(http.StatusBadRequest, gin.H{"message": "grace_seconds 需在 0-600 之间"})
			return
		}
		grace = *req.GraceSeconds
	}
	uid := middleware.UID(c)
	uname := middleware.Username(c)
	full := req.Namespace + "/" + req.Name
	if !middleware.Allowed(h.db, middleware.Role(c), uid, clusterName, req.Namespace, "delete", "pods") {
		audit.Log(audit.Record(uid, uname, "node_drain", clusterName, "", "nodes", nodeName, "驱逐 "+full+" 权限拒绝：无 pods delete", "denied"))
		c.JSON(http.StatusForbidden, gin.H{"message": "无 Pod 删除权限（需 pods 的 delete 授权）"})
		return
	}
	e, err := h.mgr.Get(clusterName)
	if err != nil {
		audit.Log(audit.Record(uid, uname, "node_drain", clusterName, "", "nodes", nodeName, "驱逐 "+full+" 获取集群失败: "+err.Error(), "failed"))
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	// PDB 拦截重试最多约 15s，60s 足够单 Pod 全流程
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	pod, err := e.Client.CoreV1().Pods(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
	if err != nil {
		if kerrors.IsNotFound(err) {
			// 前一轮请求实际生效但响应丢失的场景：已不存在视为成功，逐 Pod 循环可幂等重试
			audit.Log(audit.Record(uid, uname, "node_drain", clusterName, "", "nodes", nodeName, "驱逐 "+full+"（Pod 已不存在，视为成功）", "success"))
			c.JSON(http.StatusOK, gin.H{"message": "Pod 已不存在"})
			return
		}
		audit.Log(audit.Record(uid, uname, "node_drain", clusterName, "", "nodes", nodeName, "驱逐 "+full+" 失败: "+err.Error(), "failed"))
		c.JSON(http.StatusBadGateway, gin.H{"message": "获取 Pod 失败: " + err.Error()})
		return
	}
	if pod.Spec.NodeName != nodeName || pod.UID != types.UID(req.UID) {
		audit.Log(audit.Record(uid, uname, "node_drain", clusterName, "", "nodes", nodeName, "驱逐 "+full+" 冲突：Pod 与节点/UID 不匹配", "failed"))
		c.JSON(http.StatusConflict, gin.H{"message": "Pod 与节点/UID 不匹配，请刷新排空计划后重试"})
		return
	}
	detail := fmt.Sprintf("驱逐 %s（grace %ds）", full, grace)
	if err := evictPod(ctx, e, pod, grace); err != nil {
		reason := err.Error()
		if kerrors.IsTooManyRequests(err) {
			reason = "PodDisruptionBudget 拦截（重试后仍无法驱逐）"
		}
		audit.Log(audit.Record(uid, uname, "node_drain", clusterName, "", "nodes", nodeName, detail+"失败: "+reason, "failed"))
		c.JSON(http.StatusBadGateway, gin.H{"message": "驱逐失败: " + reason})
		return
	}
	audit.Log(audit.Record(uid, uname, "node_drain", clusterName, "", "nodes", nodeName, detail, "success"))
	c.JSON(http.StatusOK, gin.H{"message": "已驱逐 " + full})
}

// evictPod 走 Eviction API（尊重 PDB）；429 重试 3 次（间隔 5s）后返回最后一次错误。
// Preconditions 带 Pod UID：防止驱逐请求落到已重建的同名 Pod 上。
func evictPod(ctx context.Context, e *cluster.Entry, pod *corev1.Pod, grace int64) error {
	ev := &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace},
		DeleteOptions: &metav1.DeleteOptions{
			GracePeriodSeconds: &grace,
			Preconditions:      &metav1.Preconditions{UID: &pod.UID},
		},
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
		}
		err := e.Client.PolicyV1().Evictions(pod.Namespace).Evict(ctx, ev)
		if err == nil || !kerrors.IsTooManyRequests(err) {
			return err
		}
		lastErr = err // PDB 拦截：退避重试
	}
	return lastErr
}
