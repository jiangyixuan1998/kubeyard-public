package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"kubeyard/internal/middleware"
	"kubeyard/internal/model"
	"kubeyard/internal/notify"
)

// ---------- 站内消息（权限变更通知 + 到期预警）----------

// notifyScanLoop 后台到期预警扫描：启动 1 分钟后先跑一次，此后每小时一次。
// 预警消息带去重键（实体 ID + 到期时间戳），同一到期事件只提示一次。
func (h *H) notifyScanLoop() {
	time.Sleep(time.Minute)
	notify.ScanExpiring(h.db)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		notify.ScanExpiring(h.db)
	}
}

// MyNotifications GET /api/v1/me/notifications?limit=50 — 当前用户消息（含已读，倒序）+ 未读总数
func (h *H) MyNotifications(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	uid := middleware.UID(c)
	var items []model.Notification
	h.db.Where("user_id = ?", uid).Order("id DESC").Limit(limit).Find(&items)
	var unread int64
	h.db.Model(&model.Notification{}).Where("user_id = ? AND read_at IS NULL", uid).Count(&unread)
	if items == nil {
		items = []model.Notification{}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "unread": unread})
}

// ReadNotifications POST /api/v1/me/notifications/read — 标记已读（指定 ids 或 all，仅限自己的消息）
func (h *H) ReadNotifications(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids"`
		All bool   `json:"all"`
	}
	_ = c.BindJSON(&req)
	if !req.All && len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请指定 ids 或 all"})
		return
	}
	now := time.Now()
	q := h.db.Model(&model.Notification{}).
		Where("user_id = ? AND read_at IS NULL", middleware.UID(c))
	if !req.All {
		q = q.Where("id IN ?", req.IDs)
	}
	q.Update("read_at", now)
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// ---------- 变更事件的站内消息构造（授权页 / 权限组 / 工单通道共用文案口径）----------

// notifyExpiryText 时限文案：NULL=永久（导出的 expiryText 面向 Excel 列，口径不同，不复用）
func notifyExpiryText(t *time.Time) string {
	if t == nil {
		return "永久"
	}
	return t.Local().Format("2006-01-02 15:04") + " 到期"
}

// expirySuffix 追加式时限文案（「，2026-10-21 15:04 到期」/「，永久有效」）
func expirySuffix(t *time.Time) string {
	if t == nil {
		return "，永久有效"
	}
	return "，" + notifyExpiryText(t)
}

// notifyGrantChanges 直接授权整体替换后的站内消息：按规则键（集群/命名空间/资源/动词/动作）
// 对比新增/移除，同键时限变化视为续期。在事务提交成功后调用（只通知已落库的变更）。
func notifyGrantChanges(db *gorm.DB, uid uint, clusterNames map[uint]string, old []model.UserGrant, reqGrants []grantItem) {
	key := func(cid uint, ns, res, verb, actions string) string {
		return strings.Join([]string{clusterNameOf(clusterNames, cid), orStr(ns, "*"), orStr(res, "*"), orStr(verb, "*"), orStr(actions, "*")}, " / ")
	}
	oldExp := map[string]*time.Time{}
	for _, g := range old {
		oldExp[key(g.ClusterID, g.Namespace, g.Resource, g.Verb, g.Actions)] = g.ExpiresAt
	}
	newExp := map[string]*time.Time{}
	for _, g := range reqGrants {
		if g.ClusterID == 0 {
			continue
		}
		k := key(g.ClusterID, g.Namespace, g.Resource, g.Verb, g.Actions)
		if _, dup := newExp[k]; dup {
			continue
		}
		newExp[k] = g.ExpiresAt
	}
	for k, ne := range newExp {
		oe, existed := oldExp[k]
		switch {
		case !existed:
			notify.Push(db, uid, notify.TypeGrantAdded, "权限已新增", "新增授权："+k+expirySuffix(ne), nil)
		case !sameExpiry(oe, ne):
			notify.Push(db, uid, notify.TypeGrantRenewed, "权限已续期", "授权时限调整："+k+"，"+notifyExpiryText(oe)+" → "+notifyExpiryText(ne), nil)
		}
	}
	for k := range oldExp {
		if _, kept := newExp[k]; !kept {
			notify.Push(db, uid, notify.TypeGrantRemoved, "权限已移除", "移除授权："+k, nil)
		}
	}
}

// notifyGroupMemberChanges 权限组成员变更的站内消息：加入/移出/成员时限变化（时限=组权限的生效到期）。
// CreateGroup 传 oldMembers=nil（全员为加入）；DeleteGroup 传空 groupReq（全员为移出）。
func notifyGroupMemberChanges(db *gorm.DB, groupName string, oldMembers []model.UserGroup, req groupReq) {
	oldMap := map[uint]*time.Time{}
	for _, m := range oldMembers {
		oldMap[m.UserID] = m.ExpiresAt
	}
	newMap := map[uint]*time.Time{}
	if len(req.Users) > 0 {
		for _, m := range req.Users {
			if m.ID != 0 {
				newMap[m.ID] = m.ExpiresAt
			}
		}
	} else {
		for _, id := range req.UserIDs {
			newMap[id] = nil
		}
	}
	for id, ne := range newMap {
		oe, existed := oldMap[id]
		switch {
		case !existed:
			notify.Push(db, id, notify.TypeGrantAdded, "权限已新增", "已加入权限组「"+groupName+"」"+expirySuffix(ne), nil)
		case !sameExpiry(oe, ne):
			notify.Push(db, id, notify.TypeGrantRenewed, "权限已续期",
				"权限组「"+groupName+"」成员时限："+notifyExpiryText(oe)+" → "+notifyExpiryText(ne), nil)
		}
	}
	for id := range oldMap {
		if _, kept := newMap[id]; !kept {
			notify.Push(db, id, notify.TypeGrantRemoved, "权限已移除", "已移出权限组「"+groupName+"」", nil)
		}
	}
}
