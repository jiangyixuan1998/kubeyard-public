package api

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"kubeyard/internal/audit"
	"kubeyard/internal/auth"
	"kubeyard/internal/middleware"
	"kubeyard/internal/model"
)

// ---------- 权限组管理 ----------

type groupRuleItem struct {
	ClusterID uint   `json:"cluster_id"`
	Namespace string `json:"namespace"`
	Resource  string `json:"resource"`
	Verb      string `json:"verb"`
	Actions   string `json:"actions"`
}

// groupUserItem 组成员：时限绑在成员关系上（NULL=永久），规则本身长期复用
type groupUserItem struct {
	ID        uint       `json:"id"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type groupReq struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Rules       []groupRuleItem `json:"rules"`
	Users       []groupUserItem `json:"users"`    // 成员（含各自时限；创建/更新时一并设置）
	UserIDs     []uint          `json:"user_ids"` // 兼容旧客户端：不带时限的成员列表
}

// ListGroups GET /api/v1/admin/groups — 列表 + 规则数/成员数
func (h *H) ListGroups(c *gin.Context) {
	var groups []model.GrantGroup
	h.db.Order("id ASC").Find(&groups)
	ruleCounts := map[uint]int64{}
	userCounts := map[uint]int64{}
	var rc []model.GrantGroupRule
	h.db.Find(&rc)
	for _, r := range rc {
		ruleCounts[r.GroupID]++
	}
	var uc []model.UserGroup
	h.db.Find(&uc)
	for _, m := range uc {
		userCounts[m.GroupID]++
	}
	out := []map[string]any{}
	for _, g := range groups {
		out = append(out, map[string]any{
			"id": g.ID, "name": g.Name, "description": g.Description,
			"rule_count": ruleCounts[g.ID], "user_count": userCounts[g.ID],
		})
	}
	c.JSON(http.StatusOK, out)
}

// GetGroup GET /api/v1/admin/groups/:id — 详情（规则 + 成员）
func (h *H) GetGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g model.GrantGroup
	if err := h.db.First(&g, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "权限组不存在"})
		return
	}
	var rules []model.GrantGroupRule
	h.db.Where("group_id = ?", g.ID).Find(&rules)
	var mbs []model.UserGroup
	h.db.Where("group_id = ?", g.ID).Find(&mbs)
	uids := []uint{}
	users := []groupUserItem{}
	for _, m := range mbs {
		uids = append(uids, m.UserID)
		users = append(users, groupUserItem{ID: m.UserID, ExpiresAt: m.ExpiresAt})
	}
	c.JSON(http.StatusOK, gin.H{"group": g, "rules": rules, "user_ids": uids, "users": users})
}

// saveGroupRules 事务内整体替换规则与成员（成员带各自的时限）
func (h *H) saveGroupRules(tx *gorm.DB, groupID uint, req groupReq) error {
	if err := tx.Where("group_id = ?", groupID).Delete(&model.GrantGroupRule{}).Error; err != nil {
		return err
	}
	for _, r := range req.Rules {
		if r.ClusterID == 0 {
			continue
		}
		rule := model.GrantGroupRule{
			GroupID:   groupID,
			ClusterID: r.ClusterID,
			Namespace: orStr(r.Namespace, "*"),
			Resource:  orStr(r.Resource, "*"),
			Verb:      orStr(r.Verb, "*"),
			Actions:   orStr(r.Actions, "*"),
		}
		if err := tx.Create(&rule).Error; err != nil {
			return err
		}
	}
	if err := tx.Where("group_id = ?", groupID).Delete(&model.UserGroup{}).Error; err != nil {
		return err
	}
	// users 优先（带时限）；旧客户端只传 user_ids 时按永久处理
	members := req.Users
	if len(members) == 0 && len(req.UserIDs) > 0 {
		for _, uid := range req.UserIDs {
			members = append(members, groupUserItem{ID: uid})
		}
	}
	for _, m := range members {
		if m.ID == 0 {
			continue
		}
		if err := tx.Create(&model.UserGroup{UserID: m.ID, GroupID: groupID, ExpiresAt: m.ExpiresAt}).Error; err != nil {
			return err
		}
	}
	return nil
}

// reqMemberIDs 请求里的全部成员 ID（users 优先，兼容 user_ids）
func reqMemberIDs(req groupReq) []uint {
	if len(req.Users) > 0 {
		ids := make([]uint, 0, len(req.Users))
		for _, m := range req.Users {
			if m.ID != 0 {
				ids = append(ids, m.ID)
			}
		}
		return ids
	}
	return req.UserIDs
}

// groupDiffDetail 权限组变更差异（审计详情用）：成员增删/时限变化/规则增删
func groupDiffDetail(db *gorm.DB, groupName string, oldMembers []model.UserGroup, oldRules []model.GrantGroupRule, req groupReq) string {
	// 用户名缓存（审计里显示用户名而不是 #id）
	var users []model.User
	db.Find(&users)
	usernames := map[uint]string{}
	for i := range users {
		usernames[users[i].ID] = users[i].Username
	}
	nameOf := func(id uint) string {
		if n := usernames[id]; n != "" {
			return n
		}
		return fmt.Sprintf("#%d", id)
	}
	expiryStr := func(t *time.Time) string {
		if t == nil {
			return "永久"
		}
		return t.Local().Format("2006-01-02 15:04 到期")
	}

	var diff []string
	// 成员：old → new 对比（加入 / 移出 / 时限变化）
	type memberState struct{ expires *time.Time }
	oldMap := map[uint]memberState{}
	for _, m := range oldMembers {
		oldMap[m.UserID] = memberState{m.ExpiresAt}
	}
	newMap := map[uint]memberState{}
	for _, m := range req.Users {
		if m.ID != 0 {
			newMap[m.ID] = memberState{m.ExpiresAt}
		}
	}
	if len(req.Users) == 0 {
		for _, id := range req.UserIDs {
			newMap[id] = memberState{nil}
		}
	}
	for id, ns := range newMap {
		if os, ok := oldMap[id]; !ok {
			diff = append(diff, fmt.Sprintf("成员加入: %s（%s）", nameOf(id), expiryStr(ns.expires)))
		} else if !sameExpiry(os.expires, ns.expires) {
			diff = append(diff, fmt.Sprintf("成员时限: %s %s → %s", nameOf(id), expiryStr(os.expires), expiryStr(ns.expires)))
		}
	}
	for id := range oldMap {
		if _, ok := newMap[id]; !ok {
			diff = append(diff, "成员移出: "+nameOf(id))
		}
	}
	// 规则：文本化对比
	ruleText := func(clusterID uint, ns, res, verb, actions string) string {
		var cl model.Cluster
		cn := fmt.Sprintf("#%d", clusterID)
		if err := db.Where("id = ?", clusterID).First(&cl).Error; err == nil {
			cn = cl.Name
		}
		return strings.Join([]string{cn, orStr(ns, "*"), orStr(res, "*"), orStr(verb, "*"), orStr(actions, "*")}, " / ")
	}
	oldRuleSet := map[string]bool{}
	for _, r := range oldRules {
		oldRuleSet[ruleText(r.ClusterID, r.Namespace, r.Resource, r.Verb, r.Actions)] = true
	}
	newRuleSet := map[string]bool{}
	for _, r := range req.Rules {
		if r.ClusterID == 0 {
			continue
		}
		k := ruleText(r.ClusterID, r.Namespace, r.Resource, r.Verb, r.Actions)
		if !newRuleSet[k] && !oldRuleSet[k] {
			diff = append(diff, "新增规则: "+k)
		}
		newRuleSet[k] = true
	}
	for k := range oldRuleSet {
		if !newRuleSet[k] {
			diff = append(diff, "移除规则: "+k)
		}
	}

	if len(diff) == 0 {
		return "组名/描述或成员时限无实质变化"
	}
	sort.Strings(diff)
	return fmt.Sprintf("权限组「%s」变更明细（集群 / 命名空间 / 资源 / 动词 / 字段动作）:\n - ", groupName) + strings.Join(diff, "\n - ")
}

func sameExpiry(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// CreateGroup POST /api/v1/admin/groups
func (h *H) CreateGroup(c *gin.Context) {
	var req groupReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误：组名必填"})
		return
	}
	g := model.GrantGroup{Name: req.Name, Description: req.Description}
	if err := h.db.Create(&g).Error; err != nil {
		c.JSON(http.StatusConflict, gin.H{"message": "创建失败：组名可能已存在"})
		return
	}
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		return h.saveGroupRules(tx, g.ID, req)
	}); err != nil {
		// 规则/成员写入失败：回滚删除半成品组，避免留下空组且返回成功
		h.db.Delete(&g)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "保存失败: " + err.Error()})
		return
	}
	memberIDs := reqMemberIDs(req)
	for _, uid := range memberIDs {
		auth.RevokeAllOfUser(h.db, uid, "group-update")
	}
	syncAuthz(h.db)
	notifyGroupMemberChanges(h.db, req.Name, nil, req) // 站内消息：新组成员全部为「加入」
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "group_create", "", "", "", req.Name,
		groupDiffDetail(h.db, req.Name, nil, nil, req), "success"))
	c.JSON(http.StatusOK, gin.H{"id": g.ID})
}

// UpdateGroup PUT /api/v1/admin/groups/:id
func (h *H) UpdateGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g model.GrantGroup
	if err := h.db.First(&g, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "权限组不存在"})
		return
	}
	var req groupReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误：组名必填"})
		return
	}
	oldName := g.Name
	auditDetail := ""
	// 成员变更前快照（事务内刷新 + 事务后发站内消息共用）
	var beforeMembers []model.UserGroup
	err := h.db.Transaction(func(tx *gorm.DB) error {
		// 审计用：成员/规则变更前快照
		tx.Where("group_id = ?", g.ID).Find(&beforeMembers)
		var beforeRules []model.GrantGroupRule
		tx.Where("group_id = ?", g.ID).Find(&beforeRules)
		g.Name, g.Description = req.Name, req.Description
		if err := tx.Save(&g).Error; err != nil {
			return err
		}
		// 成员变更前后的用户令牌都要吊销
		affected := map[uint]bool{}
		for _, m := range beforeMembers {
			affected[m.UserID] = true
		}
		for _, uid := range reqMemberIDs(req) {
			affected[uid] = true
		}
		tx.Where("group_id = ?", g.ID).Delete(&model.UserGroup{})
		for uid := range affected {
			auth.RevokeAllOfUser(tx, uid, "group-update")
		}
		if err := h.saveGroupRules(tx, g.ID, req); err != nil {
			return err
		}
		auditDetail = groupDiffDetail(h.db, g.Name, beforeMembers, beforeRules, req)
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "保存失败: " + err.Error()})
		return
	}
	name := oldName
	if req.Name != "" {
		name = req.Name
	}
	notifyGroupMemberChanges(h.db, name, beforeMembers, req) // 站内消息：加入/移出/时限变化
	syncAuthz(h.db)
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "group_update", "", "", "", name,
		auditDetail, "success"))
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// DeleteGroup DELETE /api/v1/admin/groups/:id
func (h *H) DeleteGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g model.GrantGroup
	if err := h.db.First(&g, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "权限组不存在"})
		return
	}
	var mbs []model.UserGroup
	h.db.Where("group_id = ?", g.ID).Find(&mbs)
	var rules []model.GrantGroupRule
	h.db.Where("group_id = ?", g.ID).Find(&rules)
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("group_id = ?", g.ID).Delete(&model.GrantGroupRule{}).Error; err != nil {
			return err
		}
		if err := tx.Where("group_id = ?", g.ID).Delete(&model.UserGroup{}).Error; err != nil {
			return err
		}
		return tx.Delete(&g).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "删除失败: " + err.Error()})
		return
	}
	for _, m := range mbs {
		auth.RevokeAllOfUser(h.db, m.UserID, "group-delete")
	}
	syncAuthz(h.db)
	notifyGroupMemberChanges(h.db, g.Name, mbs, groupReq{}) // 站内消息：删组对全体成员为「移出」
	// 删除前成员/规则明细（此后查不到了，审计是唯一记录）
	var users []model.User
	h.db.Find(&users)
	usernames := map[uint]string{}
	for i := range users {
		usernames[users[i].ID] = users[i].Username
	}
	memberNames := make([]string, 0, len(mbs))
	for _, m := range mbs {
		if n := usernames[m.UserID]; n != "" {
			memberNames = append(memberNames, n)
		} else {
			memberNames = append(memberNames, fmt.Sprintf("#%d", m.UserID))
		}
	}
	sort.Strings(memberNames)
	detail := fmt.Sprintf("删除权限组「%s」（%d 条规则，成员: %s）",
		g.Name, len(rules), strings.Join(memberNames, "、"))
	if len(memberNames) == 0 {
		detail = fmt.Sprintf("删除权限组「%s」（%d 条规则，无成员）", g.Name, len(rules))
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "group_delete", "", "", "", g.Name, detail, "success"))
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// GetUserGroups GET /api/v1/admin/users/:id/groups — 用户所属权限组
func (h *H) GetUserGroups(c *gin.Context) {
	uid, _ := strconv.Atoi(c.Param("id"))
	var mbs []model.UserGroup
	h.db.Where("user_id = ?", uid).Find(&mbs)
	ids := []uint{}
	for _, m := range mbs {
		ids = append(ids, m.GroupID)
	}
	out := []map[string]any{}
	if len(ids) > 0 {
		var gs []model.GrantGroup
		h.db.Where("id IN ?", ids).Find(&gs)
		for _, g := range gs {
			out = append(out, map[string]any{"id": g.ID, "name": g.Name, "description": g.Description})
		}
	}
	c.JSON(http.StatusOK, out)
}
