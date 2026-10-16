package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"kubeyard/internal/audit"
	"kubeyard/internal/auth"
	"kubeyard/internal/middleware"
	"kubeyard/internal/model"
)

type userReq struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	Password    string `json:"password"`
	Status      *int   `json:"status"`
}

func randomPassword() string {
	b := make([]byte, 9)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ListUsers GET /api/v1/admin/users
func (h *H) ListUsers(c *gin.Context) {
	var users []model.User
	h.db.Order("id ASC").Find(&users)
	c.JSON(http.StatusOK, users)
}

// CreateUser POST /api/v1/admin/users — 密码缺省则随机生成并在响应中返回一次
func (h *H) CreateUser(c *gin.Context) {
	var req userReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	var exist model.User
	if err := h.db.Where("username = ?", req.Username).First(&exist).Error; err == nil {
		c.JSON(http.StatusConflict, gin.H{"message": "用户名已存在"})
		return
	}
	if req.Role == "" {
		req.Role = model.RoleViewer
	}
	password := req.Password
	if password == "" {
		password = randomPassword()
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "密码加密失败"})
		return
	}
	u := model.User{
		Username:     req.Username,
		DisplayName:  req.DisplayName,
		Email:        req.Email,
		Source:       "local",
		Role:         req.Role,
		Status:       1,
		PasswordHash: string(hash),
	}
	if err := h.db.Create(&u).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "创建失败: " + err.Error()})
		return
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "user_create", "", "", "", req.Username,
		fmt.Sprintf("显示名 %s  邮箱 %s  角色 %s  来源 local  初始密码 %s",
			orUnsetStr(u.DisplayName), orUnsetStr(u.Email), u.Role,
			map[bool]string{true: "管理员输入", false: "随机生成"}[req.Password != ""]), "success"))
	c.JSON(http.StatusOK, gin.H{"user": u, "initial_password": req.Password == "", "password": password})
}

// UpdateUser PUT /api/v1/admin/users/:id
func (h *H) UpdateUser(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var u model.User
	if err := h.db.First(&u, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "用户不存在"})
		return
	}
	var req userReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	before := u // 审计用：修改前快照
	if u.ID == middleware.UID(c) && ((req.Role != "" && req.Role != model.RoleSuperAdmin) || (req.Status != nil && *req.Status != 1)) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "不能降级或禁用当前登录账号"})
		return
	}
	if req.Role != "" && req.Role != u.Role && u.Role == model.RoleSuperAdmin {
		if !h.notLastSuperAdmin(u.ID) {
			c.JSON(http.StatusBadRequest, gin.H{"message": "系统至少需要保留一名启用的超级管理员"})
			return
		}
	}
	if req.DisplayName != "" {
		u.DisplayName = req.DisplayName
	}
	if req.Email != "" {
		u.Email = req.Email
	}
	if req.Role != "" {
		u.Role = req.Role
	}
	if req.Status != nil {
		u.Status = *req.Status
	}
	if req.Password != "" {
		if u.Source != "local" {
			c.JSON(http.StatusBadRequest, gin.H{"message": "LDAP 用户不支持本地密码"})
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err == nil {
			u.PasswordHash = string(hash)
		}
	}
	// 审计用变更快照（改库前逐字段对比）
	auditDetail := userDiffDetail(&before, &u, req.Password != "")
	h.db.Save(&u)
	auth.RevokeAllOfUser(h.db, u.ID, "user-update")
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "user_update", "", "", "", u.Username, auditDetail, "success"))
	c.JSON(http.StatusOK, gin.H{"user": u})
}

// DeleteUser DELETE /api/v1/admin/users/:id
func (h *H) DeleteUser(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if uint(id) == middleware.UID(c) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "不能删除当前登录账号"})
		return
	}
	var u model.User
	if err := h.db.First(&u, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "用户不存在"})
		return
	}
	if u.Role == model.RoleSuperAdmin && !h.notLastSuperAdmin(u.ID) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "系统至少需要保留一名启用的超级管理员"})
		return
	}
	grantCount := int64(0)
	h.db.Model(&model.UserGrant{}).Unscoped().Where("user_id = ?", u.ID).Count(&grantCount)
	h.db.Where("user_id = ?", u.ID).Unscoped().Delete(&model.UserGrant{})
	h.db.Where("user_id = ?", u.ID).Unscoped().Delete(&model.UserGroup{})
	h.db.Where("user_id = ?", u.ID).Delete(&model.Notification{}) // 站内消息随账号清除
	h.db.Delete(&u)
	auth.RevokeAllOfUser(h.db, u.ID, "user-delete")
	syncAuthz(h.db)
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "user_delete", "", "", "", u.Username,
		fmt.Sprintf("删除用户（显示名 %s  角色 %s  来源 %s），同时清除其 %d 条直接授权与权限组成员关系",
			orUnsetStr(u.DisplayName), u.Role, u.Source, grantCount), "success"))
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// notLastSuperAdmin 除 excludeID 外还存在启用的超管
func (h *H) notLastSuperAdmin(excludeID uint) bool {
	var n int64
	h.db.Model(&model.User{}).
		Where("role = ? AND status = 1 AND id <> ?", model.RoleSuperAdmin, excludeID).Count(&n)
	return n > 0
}

// userDiffDetail 用户信息变更差异（审计详情用）：逐字段对比，只列出变化项
func userDiffDetail(before, after *model.User, pwChanged bool) string {
	var diff []string
	if before.DisplayName != after.DisplayName {
		diff = append(diff, fmt.Sprintf("显示名: %s → %s", orUnsetStr(before.DisplayName), orUnsetStr(after.DisplayName)))
	}
	if before.Email != after.Email {
		diff = append(diff, fmt.Sprintf("邮箱: %s → %s", orUnsetStr(before.Email), orUnsetStr(after.Email)))
	}
	if before.Role != after.Role {
		diff = append(diff, fmt.Sprintf("角色: %s → %s", before.Role, after.Role))
	}
	if before.Status != after.Status {
		diff = append(diff, fmt.Sprintf("状态: %s → %s", statusLabel(before.Status), statusLabel(after.Status)))
	}
	if pwChanged {
		diff = append(diff, "密码: 已重置")
	}
	if len(diff) == 0 {
		return "无字段变化"
	}
	return "变更明细:\n - " + strings.Join(diff, "\n - ")
}

func statusLabel(s int) string {
	if s == 1 {
		return "启用"
	}
	return "禁用"
}

func orUnsetStr(s string) string {
	if s == "" {
		return "（空）"
	}
	return s
}

// ---------- 授权 ----------

// MyGrants GET /api/v1/me/grants — 当前用户授权概览（资源导航过滤 +「我的权限」抽屉共用）
// 返回含已过期条目（expires_at 原样返回，由消费方判定：导航过滤剔除、抽屉标灰展示「已到期」，
// 数据不删除，管理员改期即续授）；组权限的到期 = 成员关系时限，created_at = 入组时间
func (h *H) MyGrants(c *gin.Context) {
	if middleware.Role(c) == model.RoleSuperAdmin {
		c.JSON(http.StatusOK, []map[string]any{{"cluster": "*", "resources": "*", "verbs": "*", "actions": "*"}})
		return
	}
	c.JSON(http.StatusOK, h.grantsOverview(middleware.UID(c)))
}

// grantsOverview 某用户的生效授权明细 = 直接授权 + 所属权限组规则（并集），
// 含已过期条目（前端标灰）；组权限的到期 = 成员关系时限，created_at = 入组时间。
// 「我的权限」抽屉（自查）与用户管理页「权限」抽屉（管理员查他人）共用。
func (h *H) grantsOverview(uid uint) []map[string]any {
	var cls []model.Cluster
	h.db.Find(&cls)
	names := map[uint]string{}
	labels := map[uint]string{} // 显示名（描述(名称)，与集群下拉同口径）
	for _, cl := range cls {
		names[cl.ID] = cl.Name
		if d := strings.TrimSpace(cl.Description); d != "" {
			labels[cl.ID] = d + "(" + cl.Name + ")"
		} else {
			labels[cl.ID] = cl.Name
		}
	}
	out := []map[string]any{}
	// 直接授权（含已过期，前端按 expires_at 自行判定）
	var grants []model.UserGrant
	h.db.Where("user_id = ?", uid).Find(&grants)
	for _, g := range grants {
		out = append(out, map[string]any{
			"cluster":       names[g.ClusterID],
			"cluster_label": labels[g.ClusterID],
			"namespace":     g.Namespace,
			"resources":     g.Resource,
			"verbs":         g.Verb,
			"actions":       g.Actions,
			"expires_at":    g.ExpiresAt, // NULL=永久
			"created_at":    g.CreatedAt, // 授权时间
			"source":        "直接授权",
		})
	}
	// 权限组：规则长期复用，生效时限绑在成员关系上（到期不删除、不失组籍，仅权限失效）
	var mbs []model.UserGroup
	h.db.Where("user_id = ?", uid).Find(&mbs)
	if len(mbs) > 0 {
		ids := make([]uint, 0, len(mbs))
		for _, m := range mbs {
			ids = append(ids, m.GroupID)
		}
		var groups []model.GrantGroup
		h.db.Where("id IN ?", ids).Find(&groups)
		gname := map[uint]string{}
		for _, g := range groups {
			gname[g.ID] = g.Name
		}
		var rules []model.GrantGroupRule
		h.db.Where("group_id IN ?", ids).Find(&rules)
		for _, r := range rules {
			var m *model.UserGroup
			for i := range mbs {
				if mbs[i].GroupID == r.GroupID {
					m = &mbs[i]
					break
				}
			}
			if m == nil {
				continue // 理论不可达（rules 按 mbs 的组 ID 查询），防御性跳过
			}
			out = append(out, map[string]any{
				"cluster":       names[r.ClusterID],
				"cluster_label": labels[r.ClusterID],
				"namespace":     r.Namespace,
				"resources":     r.Resource,
				"verbs":         r.Verb,
				"actions":       r.Actions,
				"expires_at":    m.ExpiresAt, // 成员关系时限即组权限的生效到期
				"created_at":    m.CreatedAt, // 入组时间（存量行为零值，前端显示 —）
				"source":        "权限组·" + gname[r.GroupID],
			})
		}
	}
	return out
}

// GetUserPerms GET /api/v1/admin/users/:id/perms — 管理员查看指定用户的权限明细（用户管理页「权限」抽屉）
func (h *H) GetUserPerms(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var u model.User
	if err := h.db.First(&u, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "用户不存在"})
		return
	}
	if u.Role == model.RoleSuperAdmin {
		c.JSON(http.StatusOK, []map[string]any{{"cluster": "*", "resources": "*", "verbs": "*", "actions": "*"}})
		return
	}
	c.JSON(http.StatusOK, h.grantsOverview(u.ID))
}

type grantItem struct {
	ClusterID uint       `json:"cluster_id"`
	Cluster   string     `json:"cluster"`
	Namespace string     `json:"namespace"`
	Resource  string     `json:"resource"`
	Verb      string     `json:"verb"`
	Actions   string     `json:"actions"`              // 细粒度动作/字段，仅 verb 含 update 时生效
	ExpiresAt *time.Time `json:"expires_at,omitempty"` // NULL=永久；集成 API 下发，管理页保存时原样保留
}

// GetUserGrants GET /api/v1/admin/users/:id/grants
func (h *H) GetUserGrants(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var grants []model.UserGrant
	h.db.Where("user_id = ?", id).Find(&grants)
	clusters := map[uint]string{}
	var cls []model.Cluster
	h.db.Find(&cls)
	for _, cl := range cls {
		clusters[cl.ID] = cl.Name
	}
	out := []grantItem{}
	for _, g := range grants {
		out = append(out, grantItem{
			ClusterID: g.ClusterID,
			Cluster:   clusters[g.ClusterID],
			Namespace: g.Namespace,
			Resource:  g.Resource,
			Verb:      g.Verb,
			Actions:   g.Actions,
			ExpiresAt: g.ExpiresAt, // 必须回传：管理页保存时原样保留时限，缺失会把工单下发的时限清成永久
		})
	}
	c.JSON(http.StatusOK, out)
}

// PutUserGrants PUT /api/v1/admin/users/:id/grants — 事务内整体替换
func (h *H) PutUserGrants(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var u model.User
	if err := h.db.First(&u, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "用户不存在"})
		return
	}
	var req struct {
		Grants []grantItem `json:"grants"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	// 变更前快照（用于审计差异）
	var oldGrants []model.UserGrant
	h.db.Where("user_id = ?", id).Find(&oldGrants)
	var cls []model.Cluster
	h.db.Find(&cls)
	clusterNames := map[uint]string{}
	for _, cl := range cls {
		clusterNames[cl.ID] = cl.Name
	}

	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", id).Unscoped().Delete(&model.UserGrant{}).Error; err != nil {
			return err
		}
		for _, g := range req.Grants {
			if g.ClusterID == 0 {
				continue
			}
			grant := model.UserGrant{
				UserID:    uint(id),
				ClusterID: g.ClusterID,
				Namespace: orStr(g.Namespace, "*"),
				Resource:  orStr(g.Resource, "*"),
				Verb:      orStr(g.Verb, "*"),
				Actions:   orStr(g.Actions, "*"),
				ExpiresAt: g.ExpiresAt, // 管理页编辑不改动时限（集成 API 下发的到期时间原样保留）
				CreatedBy: middleware.UID(c),
			}
			if err := tx.Create(&grant).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "保存失败: " + err.Error()})
		return
	}
	auth.RevokeAllOfUser(h.db, u.ID, "grants-update")
	notifyGrantChanges(h.db, u.ID, clusterNames, oldGrants, req.Grants) // 站内消息：新增/移除/时限变化
	syncAuthz(h.db)
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "grants_update", "", "", "", u.Username,
		grantDiffDetail(clusterNames, oldGrants, req.Grants), "success"))
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// grantDiffDetail 生成授权变更差异文本（新增/移除的规则，用于审计详情）
func grantDiffDetail(clusterNames map[uint]string, old []model.UserGrant, reqGrants []grantItem) string {
	rule := func(cluster string, ns, res, verb, actions string) string {
		return strings.Join([]string{cluster, orStr(ns, "*"), orStr(res, "*"), orStr(verb, "*"), orStr(actions, "*")}, " / ")
	}
	nameOf := func(cid uint) string {
		if n := clusterNames[cid]; n != "" {
			return n
		}
		return fmt.Sprintf("#%d", cid)
	}
	oldSet := map[string]bool{}
	for _, g := range old {
		oldSet[rule(nameOf(g.ClusterID), g.Namespace, g.Resource, g.Verb, g.Actions)] = true
	}
	newSet := map[string]bool{}
	var added []string
	for _, g := range reqGrants {
		if g.ClusterID == 0 {
			continue
		}
		k := rule(nameOf(g.ClusterID), g.Namespace, g.Resource, g.Verb, g.Actions)
		if newSet[k] {
			continue
		}
		newSet[k] = true
		if !oldSet[k] {
			added = append(added, k)
		}
	}
	var removed []string
	for k := range oldSet {
		if !newSet[k] {
			removed = append(removed, k)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	detail := fmt.Sprintf("规则数 %d → %d（集群 / 命名空间 / 资源 / 动词 / 字段动作）", len(oldSet), len(newSet))
	if len(added) > 0 {
		detail += "\n新增:\n - " + strings.Join(added, "\n - ")
	}
	if len(removed) > 0 {
		detail += "\n移除:\n - " + strings.Join(removed, "\n - ")
	}
	if len(added) == 0 && len(removed) == 0 {
		detail += "\n（规则无变化）"
	}
	return detail
}

func orStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
