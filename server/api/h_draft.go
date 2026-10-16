package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"kubeyard/internal/audit"
	"kubeyard/internal/middleware"
	"kubeyard/internal/model"
)

// ---------- 资源暂存草稿 ----------
// 表单编辑的「暂存」：payload 与 resedit 完全同构，只存平台 DB、不写集群（不触发滚动重启）；
// 应用时前端把暂存的 payload 原样走 PUT resedit（权限校验/字段 diff/审计全部复用），成功后删草稿。
// 暂存/放弃要求与编辑相同的权限（update + 模块动作），不引入提审模式。

// draftSectionAllowed 模块与资源类型的合法组合（与 ResourceFormEdit 的放行规则对齐）
func draftSectionAllowed(resource, section string) bool {
	switch {
	case resource == "services" && serviceFormSections[section]:
		return true
	case isBuiltInWorkload(resource) && workloadFormSections[section]:
		return true
	case (resource == "configmaps" || resource == "secrets") && section == "data":
		return true
	case resource == "persistentvolumeclaims" && section == "storage":
		return true
	case metaFormSections[section] && resource != "pods":
		return true
	}
	return false
}

// draftPermission 暂存/放弃的权限：与直接保存一致（update 动词 + 模块动作；labels/annotations 仅 update）
func (h *H) draftPermission(c *gin.Context, clusterName, namespace, resource, section string) bool {
	allowedResKey := resKeyForAuth("", resource)
	if !middleware.Allowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, namespace, "update", allowedResKey) {
		return false
	}
	if !metaFormSections[section] {
		if !middleware.ActionAllowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, namespace, allowedResKey, section) {
			return false
		}
	}
	return true
}

// DraftStage PUT /clusters/:cluster/resdraft — 暂存（同目标同模块 = 覆盖）
func (h *H) DraftStage(c *gin.Context) {
	clusterName := c.Param("cluster")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4*1024*1024)
	var req struct {
		Resource     string         `json:"resource" binding:"required"`
		Group        string         `json:"group"`
		Version      string         `json:"version"`
		Namespace    string         `json:"namespace"`
		Name         string         `json:"name" binding:"required"`
		Section      string         `json:"section" binding:"required"`
		SectionLabel string         `json:"section_label"`
		ResourceVer  string         `json:"resource_version"`
		Payload      map[string]any `json:"payload"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Payload == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	if !draftSectionAllowed(req.Resource, req.Section) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "该资源类型不支持所选编辑模块"})
		return
	}
	var cl model.Cluster
	if err := h.db.Where("name = ?", clusterName).First(&cl).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "集群不存在"})
		return
	}
	if !h.draftPermission(c, clusterName, req.Namespace, req.Resource, req.Section) {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "draft_stage", clusterName, req.Namespace, req.Resource, req.Name, "权限拒绝", "denied"))
		c.JSON(http.StatusForbidden, gin.H{"message": "无该模块的编辑权限"})
		return
	}
	payload, err := json.Marshal(req.Payload)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "payload 序列化失败"})
		return
	}
	var d model.ResourceDraft
	err = h.db.Where("cluster_id = ? AND namespace = ? AND resource = ? AND name = ? AND section = ?",
		cl.ID, req.Namespace, req.Resource, req.Name, req.Section).First(&d).Error
	if err != nil {
		d = model.ResourceDraft{
			ClusterID: cl.ID, Namespace: req.Namespace, Resource: req.Resource,
			Name: req.Name, Section: req.Section,
		}
	}
	d.Group = req.Group
	d.Version = req.Version
	d.Payload = string(payload)
	d.ResourceVer = req.ResourceVer
	if strings.TrimSpace(req.SectionLabel) != "" {
		d.SectionLabel = strings.TrimSpace(req.SectionLabel)
	}
	d.CreatedBy = middleware.UID(c)
	d.CreatorName = middleware.Username(c)
	if err := h.db.Save(&d).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "暂存失败: " + err.Error()})
		return
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "draft_stage", clusterName, req.Namespace, req.Resource, req.Name,
		"暂存模块 "+orStr(req.SectionLabel, req.Section)+"（未写入集群）", "success"))
	c.JSON(http.StatusOK, gin.H{"message": "已暂存（未修改集群，稍后可在本页或暂存列表应用）", "id": d.ID})
}

// draftOut 草稿行序列化（payload 原样透传 JSON，前端应用时直接走 resedit）
func draftOut(d model.ResourceDraft, clusterName string) gin.H {
	var payload any
	_ = json.Unmarshal([]byte(d.Payload), &payload)
	return gin.H{
		"id": d.ID, "cluster": clusterName, "namespace": d.Namespace, "group": d.Group,
		"version": d.Version, "resource": d.Resource,
		"name": d.Name, "section": d.Section, "section_label": d.SectionLabel, "payload": payload,
		"resource_version": d.ResourceVer, "created_by": d.CreatorName, "updated_at": d.UpdatedAt,
	}
}

// DraftList GET /clusters/:cluster/resdrafts?namespace=&resource=&name= — 指定目标的暂存（详情抽屉用）
func (h *H) DraftList(c *gin.Context) {
	clusterName := c.Param("cluster")
	var cl model.Cluster
	if err := h.db.Where("name = ?", clusterName).First(&cl).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "集群不存在"})
		return
	}
	q := h.db.Where("cluster_id = ?", cl.ID)
	if ns := c.Query("namespace"); ns != "" {
		q = q.Where("namespace = ?", ns)
	}
	if res := c.Query("resource"); res != "" {
		q = q.Where("resource = ?", res)
	}
	if name := c.Query("name"); name != "" {
		q = q.Where("name = ?", name)
	}
	var ds []model.ResourceDraft
	if err := q.Order("updated_at DESC").Find(&ds).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "查询失败"})
		return
	}
	out := make([]gin.H, 0, len(ds))
	for _, d := range ds {
		// 草稿 payload 与编辑表单同构（可能含 Secret data 等敏感内容）：仅暴露当前用户
		// 有编辑权限（update + 模块动作，与暂存/放弃同口径）的目标的暂存，其余静默过滤。
		// 此前无任何授权检查，已登录用户可读取同集群内任意命名空间/他人的草稿（外部评审核实）
		if !h.draftPermission(c, clusterName, d.Namespace, d.Resource, d.Section) {
			continue
		}
		out = append(out, draftOut(d, cl.Name))
	}
	c.JSON(http.StatusOK, out)
}

// DraftDelete DELETE /clusters/:cluster/resdraft {namespace, resource, name, section} — 放弃暂存
func (h *H) DraftDelete(c *gin.Context) {
	clusterName := c.Param("cluster")
	var req struct {
		Namespace string `json:"namespace"`
		Resource  string `json:"resource" binding:"required"`
		Name      string `json:"name" binding:"required"`
		Section   string `json:"section" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	var cl model.Cluster
	if err := h.db.Where("name = ?", clusterName).First(&cl).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "集群不存在"})
		return
	}
	if !h.draftPermission(c, clusterName, req.Namespace, req.Resource, req.Section) {
		c.JSON(http.StatusForbidden, gin.H{"message": "无该模块的编辑权限"})
		return
	}
	res := h.db.Where("cluster_id = ? AND namespace = ? AND resource = ? AND name = ? AND section = ?",
		cl.ID, req.Namespace, req.Resource, req.Name, req.Section).Delete(&model.ResourceDraft{})
	if res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"message": "暂存不存在或已被应用/放弃"})
		return
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "draft_discard", clusterName, req.Namespace, req.Resource, req.Name,
		"放弃暂存模块 "+req.Section, "success"))
	c.JSON(http.StatusOK, gin.H{"message": "已放弃"})
}

// AdminListDrafts GET /admin/resource-drafts — 全平台暂存列表（管理页；跨集群）。
// 路由挂 SuperAdmin 中间件，全量返回是有意为之（管理页需要跨用户视角）
func (h *H) AdminListDrafts(c *gin.Context) {
	var ds []model.ResourceDraft
	if err := h.db.Order("updated_at DESC").Find(&ds).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "查询失败"})
		return
	}
	var cls []model.Cluster
	h.db.Find(&cls)
	names := map[uint]string{}
	for i := range cls {
		names[cls[i].ID] = cls[i].Name
	}
	out := make([]gin.H, 0, len(ds))
	for _, d := range ds {
		out = append(out, draftOut(d, names[d.ClusterID]))
	}
	c.JSON(http.StatusOK, out)
}
