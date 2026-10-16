package notify

import (
	"log"
	"math"
	"strconv"
	"time"

	"gorm.io/gorm"

	"kubeyard/internal/model"
)

// ---------- 站内消息（权限变更通知 + 到期预警）----------
//
// 变更类事件（新增/移除/续期）在授权变更的接口里同步 Push，不设去重键（每次变更都该通知）；
// 到期预警由后台每小时扫描（ScanExpiring），dedup 键 = 实体ID + 到期时间戳——
// 同一到期事件只提示一次，续期后（新行新到期时间）再次进入 7 天窗口会生成新键再提示。

// 消息类型（前端按类型渲染图标）
const (
	TypeGrantAdded     = "grant_added"     // 权限新增（直接授权 / 加入权限组）
	TypeGrantRemoved   = "grant_removed"   // 权限移除（直接授权撤销 / 移出权限组）
	TypeGrantRenewed   = "grant_renewed"   // 权限续期（覆盖续授 / 成员时限延长 / 账号续期）
	TypeGrantExpiring  = "grant_expiring"  // 授权/组权限即将到期（<7 天，扫描生成）
	TypeAccountExpiring = "account_expiring" // 账号即将到期（<7 天，扫描生成）
)

// Push 写入一条站内消息；dedup 非空且已存在时静默跳过（唯一索引兜底并发）。
// 服务账号不登录界面，跳过以免永不阅读的消息堆积。
func Push(db *gorm.DB, userID uint, typ, title, content string, dedup *string) {
	push(db, nil, userID, typ, title, content, dedup)
}

// push 批量场景（到期扫描）传预取的可通知用户集合 human，避免逐条查 users 表；
// nil 表示未预取，现查。集合不含的用户（服务账号/已删除）直接跳过。
func push(db *gorm.DB, human map[uint]bool, userID uint, typ, title, content string, dedup *string) {
	if human != nil {
		if !human[userID] {
			return // 服务账号 / 用户已不存在，无需通知
		}
	} else {
		var u model.User
		if err := db.First(&u, userID).Error; err != nil {
			return // 用户已不存在，无需通知
		}
		if u.AccountType == "service" {
			return
		}
	}
	if dedup != nil {
		var n int64
		db.Model(&model.Notification{}).Where("dedup_key = ?", *dedup).Count(&n)
		if n > 0 {
			return
		}
	}
	n := model.Notification{
		UserID: userID, Type: typ, Title: title, Content: content,
		DedupKey: dedup, ReadAt: nil,
	}
	if err := db.Create(&n).Error; err != nil {
		log.Printf("[notify] 写入失败: %v", err)
	}
}

// daysLeft 剩余整天数（向上取整，与前端 ExpiryTag/utils 的 daysLeft 同口径：
// 两边都 ceil，整除天时不虚报一天——截断+1 会在剩整 24h 时报成 2 天）
func daysLeft(t time.Time, now time.Time) int {
	d := int(math.Ceil(t.Sub(now).Hours() / 24))
	if d < 1 {
		return 1
	}
	return d
}

// ScanExpiring 到期预警扫描（后台每小时调用）：
// 直接授权 / 权限组成员关系 / 账号有效期三类，各按 (ID, 到期时间) 去重；
// 顺带清理过期消息（已读 30 天 / 未读 90 天），保证表容量有界。
func ScanExpiring(db *gorm.DB) {
	now := time.Now()
	window := now.Add(7 * 24 * time.Hour)

	var grants []model.UserGrant
	db.Where("expires_at IS NOT NULL AND expires_at > ? AND expires_at <= ?", now, window).Find(&grants)
	var mbs []model.UserGroup
	db.Where("expires_at IS NOT NULL AND expires_at > ? AND expires_at <= ?", now, window).Find(&mbs)
	var users []model.User
	db.Where("expires_at IS NOT NULL AND expires_at > ? AND expires_at <= ?", now, window).Find(&users)

	// 一次批量预取可通知用户（排除服务账号），后续逐条推送不再各查一次 users 表
	ids := make([]uint, 0, len(grants)+len(mbs)+len(users))
	for _, g := range grants {
		ids = append(ids, g.UserID)
	}
	for _, m := range mbs {
		ids = append(ids, m.UserID)
	}
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	human := map[uint]bool{}
	if len(ids) > 0 {
		var hit []uint
		db.Model(&model.User{}).Where("id IN ? AND account_type <> 'service'", ids).Pluck("id", &hit)
		for _, id := range hit {
			human[id] = true
		}
	}

	// 集群名映射（消息里显示名称而不是 ID）——只有存在到期授权时才需要
	clusterName := map[uint]string{}
	if len(grants) > 0 {
		var cls []model.Cluster
		db.Find(&cls)
		for _, cl := range cls {
			clusterName[cl.ID] = cl.Name
		}
	}
	clusterOf := func(id uint) string {
		if n := clusterName[id]; n != "" {
			return n
		}
		return "未知集群"
	}

	// 直接授权
	for _, g := range grants {
		// 去重键用规则元组 + 到期时间（不用行 ID）：管理页整体保存是硬删重建，行 ID 会变，
		// 按 ID 会让同一到期事件在管理员无改动保存后再提示一次；K8s 名称不含冒号，元组拼键安全
		dedup := "exp:g:" + strconv.FormatUint(uint64(g.UserID), 10) + ":" + strconv.FormatUint(uint64(g.ClusterID), 10) +
			":" + g.Namespace + ":" + g.Resource + ":" + g.Verb + ":" + g.Actions + ":" + strconv.FormatInt(g.ExpiresAt.Unix(), 10)
		push(db, human, g.UserID, TypeGrantExpiring, "授权即将到期",
			"集群 "+clusterOf(g.ClusterID)+" / 命名空间 "+orStar(g.Namespace)+" / "+orStar(g.Resource)+
				" 将于 "+strconv.Itoa(daysLeft(*g.ExpiresAt, now))+" 天后（"+g.ExpiresAt.Format("01-02 15:04")+"）到期，请及时到统一权限门户（portal.example.com）申请续期", &dedup)
	}

	// 权限组成员关系（到期 = 整组权限失效）
	gname := map[uint]string{}
	if len(mbs) > 0 {
		var groups []model.GrantGroup
		db.Find(&groups)
		for _, g := range groups {
			gname[g.ID] = g.Name
		}
	}
	for _, m := range mbs {
		name := gname[m.GroupID]
		if name == "" {
			name = "已删除的权限组"
		}
		// 去重键用 用户+组+到期时间（不用行 ID）：组保存/重复入组都是删了重建，行 ID 会变
		dedup := "exp:ug:" + strconv.FormatUint(uint64(m.UserID), 10) + ":" + strconv.FormatUint(uint64(m.GroupID), 10) +
			":" + strconv.FormatInt(m.ExpiresAt.Unix(), 10)
		push(db, human, m.UserID, TypeGrantExpiring, "权限组权限即将到期",
			"权限组「"+name+"」的成员时限将于 "+strconv.Itoa(daysLeft(*m.ExpiresAt, now))+" 天后（"+m.ExpiresAt.Format("01-02 15:04")+
				"）到期，届时整组权限自动失效，请及时到统一权限门户（portal.example.com）申请续期", &dedup)
	}

	// 账号有效期（与 Layout 黄横幅互补，铃铛里留一条记录）
	for _, u := range users {
		dedup := "exp:u:" + strconv.FormatUint(uint64(u.ID), 10) + ":" + strconv.FormatInt(u.ExpiresAt.Unix(), 10)
		push(db, human, u.ID, TypeAccountExpiring, "账号即将到期",
			"你的账号将于 "+strconv.Itoa(daysLeft(*u.ExpiresAt, now))+" 天后（"+u.ExpiresAt.Format("2006-01-02")+"）到期，"+
				"到期后集群资源将不再显示，请及时到统一权限门户（portal.example.com）申请续期", &dedup)
	}

	// 消息清理：已读 30 天、未读 90 天；顺带清 AI 用量留痕（>90 天，配额只看当天）
	db.Where("read_at IS NOT NULL AND created_at < ?", now.Add(-30*24*time.Hour)).Delete(&model.Notification{})
	db.Where("read_at IS NULL AND created_at < ?", now.Add(-90*24*time.Hour)).Delete(&model.Notification{})
	db.Where("created_at < ?", now.Add(-90*24*time.Hour)).Delete(&model.AiUsageLog{})
}

func orStar(s string) string {
	if s == "" {
		return "*"
	}
	return s
}
