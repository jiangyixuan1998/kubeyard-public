package api

import (
	"crypto/rand"
	"crypto/sha256"
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
	"kubeyard/internal/notify"
)

// ---------- API 令牌：供 Jenkins / 工单平台等服务端调用授权接口 ----------
// 鉴权方式：请求头 X-API-Token: kyb-xxxx（令牌明文仅创建时返回一次，库里只存 sha256）

// TokenAuth 集成令牌鉴权中间件
func (h *H) TokenAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := strings.TrimSpace(c.GetHeader("X-API-Token"))
		if tok == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "缺少 X-API-Token 请求头"})
			return
		}
		sum := sha256.Sum256([]byte(tok))
		var t model.ApiToken
		if err := h.db.Where("token_hash = ?", hex.EncodeToString(sum[:])).First(&t).Error; err != nil || t.RevokedAt != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "令牌无效或已吊销"})
			return
		}
		now := time.Now()
		h.db.Model(&t).Update("last_used_at", now)
		c.Set("uid", t.CreatedBy)
		c.Set("username", "api:"+t.Name)
		c.Set("role", model.RoleSuperAdmin) // 仅用于 /integ/* 接口的身份标识与审计
		c.Next()
	}
}

// ListApiTokens GET /api/v1/admin/tokens
func (h *H) ListApiTokens(c *gin.Context) {
	var tokens []model.ApiToken
	h.db.Order("id DESC").Find(&tokens)
	c.JSON(http.StatusOK, tokens)
}

// CreateApiToken POST /api/v1/admin/tokens {name} - 生成 kyb- 前缀令牌，明文仅此一次返回
func (h *H) CreateApiToken(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请填写令牌用途名称（如 jenkins）"})
		return
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "生成令牌失败"})
		return
	}
	token := "kyb-" + hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	t := model.ApiToken{
		Name:        strings.TrimSpace(req.Name),
		TokenHash:   hex.EncodeToString(sum[:]),
		TokenPrefix: token[:12],
		CreatedBy:   middleware.UID(c),
	}
	if err := h.db.Create(&t).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "保存失败: " + err.Error()})
		return
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "token_create", "", "", "", t.Name, "创建 API 令牌", "success"))
	c.JSON(http.StatusOK, gin.H{"token": t, "plain_token": token})
}

// RevokeApiToken DELETE /api/v1/admin/tokens/:id
func (h *H) RevokeApiToken(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var t model.ApiToken
	if err := h.db.First(&t, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "令牌不存在"})
		return
	}
	now := time.Now()
	h.db.Model(&t).Update("revoked_at", now)
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "token_revoke", "", "", "", t.Name, "吊销 API 令牌", "success"))
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// ---------- 集成接口（/api/v1/integ/*，X-API-Token 鉴权）----------
// 工单平台审批通过后调用，为用户授予集群权限；时限四档：7 / 30 / 90 天、0=永久。

var integDurations = map[int]bool{0: true, 7: true, 30: true, 90: true}

// integRoles 工单可开的平台角色；SuperAdmin 不允许走工单通道（防提权，只能在管理页设置）
var integRoles = map[string]bool{model.RoleViewer: true, model.RoleDeveloper: true, model.RoleClusterAdmin: true}

// integPresets 申请单「角色」对应的权限预设键（内容在 系统设置→权限预设 在线可维护）
var integPresets = map[string]bool{"viewer": true, "dev": true, "admin": true}

type integGrantReq struct {
	Username     string   `json:"username"`      // 登录用户名（AD 账号 / sid）
	Cluster      string   `json:"cluster"`       // 集群名（单选，兼容旧参数）
	Clusters     []string `json:"clusters"`      // 集群名（多选；与 cluster 合并去重，逐集群写同规则授权）
	Namespace    string   `json:"namespace"`     // "ns1,ns2" 或 "*"（空默认 *）
	Resource     string   `json:"resource"`      // "pods,deployments" 或 "*"（空默认 *）
	Verb         string   `json:"verb"`          // "get,list,logs" 或 "*"（空默认 *）
	Actions      string   `json:"actions"`       // 细粒度字段，默认 *
	Preset       string   `json:"preset"`        // viewer/dev/admin 角色预设；指定时优先于 verb/actions
	DurationDays int      `json:"duration_days"` // 7 / 30 / 90 / 0(永久)
	ExpiresAt    string   `json:"expires_at"`    // 可选：绝对到期时间 "2026-09-30 00:00:00"（优先于 duration_days）
	DisplayName  string   `json:"display_name"`  // 可选：申请人姓名，随授权写入用户资料
	Team         string   `json:"team"`          // 可选：申请人团队
}

// integExpiry 时限换账号/授权到期时间；0/负数=永久（NULL）
func integExpiry(days int) *time.Time {
	if days <= 0 {
		return nil
	}
	t := time.Now().AddDate(0, 0, days)
	return &t
}

// integResolveExpiry 有效期参数解析：优先 expires_at（绝对时间，支持 "2026-09-30 00:00:00" /
// "2026-09-30" / RFC3339，本地时区），否则 duration_days（7/30/90/0=永久）。
// 返回到期时间与审计用时限文案；不合法返回错误（历史不允许）
func integResolveExpiry(expiresAt string, days int) (*time.Time, string, error) {
	if s := strings.TrimSpace(expiresAt); s != "" {
		for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04:05", time.RFC3339, "2006-01-02"} {
			if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
				if t.Before(time.Now()) {
					return nil, "", fmt.Errorf("expires_at 不能早于当前时间")
				}
				return &t, "至 " + t.Format("2006-01-02 15:04:05"), nil
			}
		}
		return nil, "", fmt.Errorf(`expires_at 格式需为 "2026-09-30 00:00:00" 或 "2026-09-30"`)
	}
	if !integDurations[days] {
		return nil, "", fmt.Errorf("duration_days 仅支持 7 / 30 / 90 / 0(永久)，或改传 expires_at 指定到期时间")
	}
	return integExpiry(days), durationLabel(days), nil
}

// integProfile 工单申请单携带的用户资料（姓名/团队），授权与入组接口可选携带一并落库
type integProfile struct {
	DisplayName string `json:"display_name"`
	Team        string `json:"team"`
}

// integListParam 合并单复数参数（去空白、去重）：旧参数单选，新参数多选，两者可混用
func integListParam(single string, multi []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(single)
	for _, s := range multi {
		add(s)
	}
	return out
}

// integPresetVerbActions 权限预设展开为 (verb, actions)；预设内容随系统设置可变
func integPresetVerbActions(db *gorm.DB, preset string) (string, string, bool) {
	pp := middleware.EffectivePermPresets(db)
	p, ok := pp.Presets[preset]
	if !ok {
		return "", "", false
	}
	return strings.Join(p.Verbs, ","), strings.Join(p.Actions, ","), true
}

// IntegCreateGrant POST /api/v1/integ/grants
// 同一用户同集群同规则（命名空间/资源/动作相同）重复申请会覆盖旧规则并重置时限。
func (h *H) IntegCreateGrant(c *gin.Context) {
	var req integGrantReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误：需要 username 与 cluster(s)"})
		return
	}
	clusterNames := integListParam(req.Cluster, req.Clusters)
	if len(clusterNames) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误：需要 username 与 cluster(s)"})
		return
	}
	expiresAt, expLabel, err := integResolveExpiry(req.ExpiresAt, req.DurationDays)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if req.Preset != "" && !integPresets[req.Preset] {
		c.JSON(http.StatusBadRequest, gin.H{"message": "preset 仅支持 viewer / dev / admin"})
		return
	}
	// 全部集群校验通过才执行（防授出一半）；cluster 单选与 clusters 多选合并去重
	cls := make([]model.Cluster, 0, len(clusterNames))
	for _, cn := range clusterNames {
		var cl model.Cluster
		if err := h.db.Where("name = ?", cn).First(&cl).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"message": "集群不存在: " + cn})
			return
		}
		cls = append(cls, cl)
	}

	// 用户不存在则预建（个人账号走 NSSO，登录时按 sid 匹配补全信息）
	u, err := h.integFindOrCreateUser(req.Username, &integProfile{DisplayName: req.DisplayName, Team: req.Team})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "创建用户失败: " + err.Error()})
		return
	}
	if u.Role == model.RoleSuperAdmin {
		c.JSON(http.StatusForbidden, gin.H{"message": "平台管理员账号请在管理页操作，不支持工单通道"})
		return
	}

	ns := orStr(strings.TrimSpace(req.Namespace), "*")
	res := orStr(strings.TrimSpace(req.Resource), "*")
	// preset 指定时按角色预设展开（申请单「角色」字段），并按申请的资源范围裁剪
	//（Pod 专属动词 / 非对应资源的动作令牌剔除，与授权页预设按钮同一口径），否则用显式传参
	verb := strings.TrimSpace(req.Verb)
	actions := strings.TrimSpace(req.Actions)
	if req.Preset != "" {
		v, a, ok := integPresetVerbActions(h.db, req.Preset)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "权限预设加载失败"})
			return
		}
		verb, actions = v, a
		eff := middleware.EffectivePermPresets(h.db)
		if _, ok := eff.Presets[req.Preset]; ok {
			trimmed := middleware.TrimPresetForResources(middleware.EffectiveRolePreset(eff, req.Preset, res), res, eff.Actions)
			verb, actions = strings.Join(trimmed.Verbs, ","), strings.Join(trimmed.Actions, ",")
		}
	}
	verb = orStr(verb, "*")
	actions = orStr(actions, "*")

	// 逐集群覆盖同规则旧授权（软删），再写入带时限的新授权（五元组全同才覆盖，含 actions）
	replaced := int64(0)
	for _, cl := range cls {
		del := h.db.Where("user_id = ? AND cluster_id = ? AND namespace = ? AND resource = ? AND verb = ? AND actions = ?",
			u.ID, cl.ID, ns, res, verb, actions).Delete(&model.UserGrant{})
		replaced += del.RowsAffected
		grant := model.UserGrant{
			UserID:    u.ID,
			ClusterID: cl.ID,
			Namespace: ns,
			Resource:  res,
			Verb:      verb,
			Actions:   actions,
			ExpiresAt: expiresAt,
			CreatedBy: middleware.UID(c),
		}
		if err := h.db.Create(&grant).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "授权写入失败（" + cl.Name + "）: " + err.Error()})
			return
		}
	}
	auth.RevokeAllOfUser(h.db, u.ID, "integ-grant") // 在线用户强制重新登录以刷新权限
	syncAuthz(h.db)

	// 站内消息：覆盖了同形旧授权 = 续期（时限重置），否则为新增
	scope := "集群 " + strings.Join(clusterNames, "、") + " / 命名空间 " + ns + " / " + res + "（" + verb + "）"
	if replaced > 0 {
		notify.Push(h.db, u.ID, notify.TypeGrantRenewed, "权限已续期", "授权已覆盖更新："+scope+expirySuffix(expiresAt), nil)
	} else {
		notify.Push(h.db, u.ID, notify.TypeGrantAdded, "权限已新增", "新增授权："+scope+expirySuffix(expiresAt), nil)
	}

	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "integ_grant", strings.Join(clusterNames, ","), ns, res,
		req.Username, verb+" / 时限 "+expLabel, "success"))
	resp := gin.H{
		"message":    "授权成功",
		"username":   u.Username,
		"clusters":   clusterNames,
		"namespace":  ns,
		"resource":   res,
		"verb":       verb,
		"expires_at": expiresAt,
	}
	if len(cls) == 1 {
		resp["cluster"] = cls[0].Name
	}
	c.JSON(http.StatusOK, resp)
}

func durationLabel(days int) string {
	if days == 0 {
		return "永久"
	}
	return strconv.Itoa(days) + " 天"
}

// IntegRevokeGrants POST /api/v1/integ/grants/revoke {username, cluster, namespace?}
// 撤销该用户在指定集群（可选命名空间过滤）的全部直接授权；权限组授予的不受影响。
func (h *H) IntegRevokeGrants(c *gin.Context) {
	var req struct {
		Username  string   `json:"username"`
		Cluster   string   `json:"cluster"`   // 单选（兼容旧参数）
		Clusters  []string `json:"clusters"`  // 多选（与 cluster 合并去重）
		Namespace string   `json:"namespace"` // 可选，"ns1,ns2"，空=该集群全部
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误：需要 username 与 cluster(s)"})
		return
	}
	clusterNames := integListParam(req.Cluster, req.Clusters)
	if len(clusterNames) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误：需要 username 与 cluster(s)"})
		return
	}
	var u model.User
	if err := h.db.Where("username = ?", req.Username).First(&u).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "用户不存在"})
		return
	}
	if u.Role == model.RoleSuperAdmin {
		c.JSON(http.StatusForbidden, gin.H{"message": "平台管理员账号请在管理页操作，不支持工单通道"})
		return
	}
	// 全部集群校验通过再撤销（防撤销一半）
	ids := make([]uint, 0, len(clusterNames))
	for _, cn := range clusterNames {
		var cl model.Cluster
		if err := h.db.Where("name = ?", cn).First(&cl).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"message": "集群不存在: " + cn})
			return
		}
		ids = append(ids, cl.ID)
	}
	q := h.db.Where("user_id = ? AND cluster_id IN ?", u.ID, ids)
	if ns := strings.TrimSpace(req.Namespace); ns != "" {
		q = q.Where("namespace = ?", ns)
	}
	del := q.Delete(&model.UserGrant{})
	auth.RevokeAllOfUser(h.db, u.ID, "integ-revoke")
	syncAuthz(h.db)
	scope := "集群 " + strings.Join(clusterNames, "、")
	if ns := strings.TrimSpace(req.Namespace); ns != "" {
		scope += " / 命名空间 " + ns
	}
	notify.Push(h.db, u.ID, notify.TypeGrantRemoved, "权限已移除",
		"已移除"+scope+" 的 "+strconv.FormatInt(del.RowsAffected, 10)+" 条授权", nil)
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "integ_revoke", strings.Join(clusterNames, ","),
		orStr(req.Namespace, "*"), "", req.Username, "撤销授权", "success"))
	c.JSON(http.StatusOK, gin.H{"message": "已撤销", "clusters": clusterNames})
}

// IntegListGroups GET /api/v1/integ/groups - 工单平台可用的权限组清单（选组用）
func (h *H) IntegListGroups(c *gin.Context) {
	var groups []model.GrantGroup
	h.db.Order("id ASC").Find(&groups)
	// 集群名映射（组规则里的 cluster_id 转可读名称，供申请单「角色」下拉展示覆盖范围）
	var cls []model.Cluster
	h.db.Find(&cls)
	clusterName := map[uint]string{}
	for _, cl := range cls {
		clusterName[cl.ID] = cl.Name
	}
	out := []gin.H{}
	for _, g := range groups {
		var rules []model.GrantGroupRule
		h.db.Where("group_id = ?", g.ID).Find(&rules)
		clusterSet := map[string]bool{}
		for _, r := range rules {
			if n, ok := clusterName[r.ClusterID]; ok {
				clusterSet[n] = true
			}
		}
		clusters := make([]string, 0, len(clusterSet))
		for n := range clusterSet {
			clusters = append(clusters, n)
		}
		sort.Strings(clusters)
		out = append(out, gin.H{"id": g.ID, "name": g.Name, "description": g.Description, "rule_count": len(rules), "clusters": clusters})
	}
	c.JSON(http.StatusOK, out)
}

// 展示别名（动词与前端 web/src/utils/grant.ts verbAliases 保持一致，改一处记得同步另一处）
var integVerbAliases = map[string]string{
	"get": "get（查看详情）", "list": "list（列表）", "watch": "watch（监听）",
	"create": "create（创建）", "update": "update（更新）", "delete": "delete（删除）",
	"exec": "exec（终端）", "logs": "logs（日志）", "download": "download（下载文件）", "upload": "upload（上传文件）",
}

// aliasJoin 逗号分隔的枚举逐项套别名，无别名的原样保留（* / CR 名等）
func aliasJoin(s string, m map[string]string) string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if v, ok := m[p]; ok {
			out = append(out, v)
		} else {
			out = append(out, p)
		}
	}
	return strings.Join(out, ", ")
}

// integVerbDisplay 生成工单侧可读动词；update 会展开同规则里的细粒度动作，
// 让申请单能看出“更新镜像 / 重启 / 副本数”这类具体能力，而不是笼统的 update。
func integVerbDisplay(db *gorm.DB, resource, verb, actions string) string {
	display := aliasJoin(verb, integVerbAliases)
	if !strings.Contains(","+strings.TrimSpace(verb)+",", ",update,") {
		return display
	}
	detail := integUpdateActionDisplay(db, resource, actions)
	if detail == "" {
		return display
	}
	return strings.Replace(display, integVerbAliases["update"], "update（"+detail+"）", 1)
}

// integUpdateActionDisplay 按生效动作目录把 actions 转成展示名。
// * = 全部更新；目录外资源的 update 令牌 = 完整更新；字段路径令牌原样展示。
func integUpdateActionDisplay(db *gorm.DB, resource, actions string) string {
	actions = strings.TrimSpace(actions)
	if actions == "" {
		return ""
	}
	resources := strings.Split(resource, ",")
	knownResource := false
	for _, r := range resources {
		r = strings.TrimSpace(r)
		if r == "*" {
			knownResource = true
			break
		}
		if r != "" && middleware.EffectivePermPresets(db).Actions[r] != nil {
			knownResource = true
			break
		}
	}

	labels := make([]string, 0, 4)
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			labels = append(labels, s)
		}
	}
	for _, token := range strings.Split(actions, ",") {
		token = strings.TrimSpace(token)
		switch {
		case token == "":
			continue
		case token == "*":
			return "全部更新"
		case token == "update" && !knownResource:
			add("完整更新")
		case strings.Contains(token, "."):
			add(token)
		default:
			resolved := false
			for _, r := range resources {
				r = strings.TrimSpace(r)
				if r == "*" {
					for resourceKey := range middleware.EffectivePermPresets(db).Actions {
						if label := middleware.ActionLabel(db, resourceKey, token); label != token {
							add(label)
							resolved = true
						}
					}
					continue
				}
				if r == "" {
					continue
				}
				if label := middleware.ActionLabel(db, r, token); label != token {
					add(label)
					resolved = true
				}
			}
			if !resolved {
				add(token)
			}
		}
	}
	return strings.Join(labels, "、")
}

// IntegGroupDetail GET /api/v1/integ/groups/:id — 权限组权限详情（申请单勾选组后展示规则明细）
// :id 支持数字 ID 或组名；集群 ID 已转可读名称；verb/resource 附带展示别名（verb_display / resource_display）
func (h *H) IntegGroupDetail(c *gin.Context) {
	key := strings.TrimSpace(c.Param("id"))
	var g model.GrantGroup
	if id, err := strconv.Atoi(key); err == nil {
		if err := h.db.First(&g, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"message": "权限组不存在"})
			return
		}
	} else if err := h.db.Where("name = ?", key).First(&g).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "权限组不存在"})
		return
	}
	var rules []model.GrantGroupRule
	h.db.Where("group_id = ?", g.ID).Order("cluster_id ASC, namespace ASC").Find(&rules)
	var cls []model.Cluster
	h.db.Find(&cls)
	clusterName := map[uint]string{}
	for _, cl := range cls {
		clusterName[cl.ID] = cl.Name
	}
	var memberCount int64
	h.db.Model(&model.UserGroup{}).Where("group_id = ?", g.ID).Count(&memberCount)
	out := []gin.H{}
	for _, r := range rules {
		cn := strconv.FormatUint(uint64(r.ClusterID), 10)
		if n, ok := clusterName[r.ClusterID]; ok {
			cn = n
		}
		out = append(out, gin.H{
			"cluster": cn, "namespace": r.Namespace, "resource": r.Resource,
			"resource_display": aliasJoin(r.Resource, resDisplayAliases),
			"verb":             r.Verb, "verb_display": integVerbDisplay(h.db, r.Resource, r.Verb, r.Actions),
			"actions": r.Actions,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"id": g.ID, "name": g.Name, "description": g.Description,
		"member_count": memberCount, "rules": out,
	})
}

// integFindOrCreateUser 用户不存在则预建（个人账号走 NSSO，登录时按 sid 匹配补全）；携带资料则一并更新
func (h *H) integFindOrCreateUser(username string, prof *integProfile) (*model.User, error) {
	var u model.User
	if err := h.db.Where("username = ?", username).First(&u).Error; err == nil {
		if prof != nil && (prof.DisplayName != "" || prof.Team != "") {
			if prof.DisplayName != "" {
				u.DisplayName = prof.DisplayName
			}
			if prof.Team != "" {
				u.Team = prof.Team
			}
			h.db.Save(&u)
		}
		return &u, nil
	}
	u = model.User{Username: username, Source: "sso", Role: model.RoleViewer, Status: 1}
	if prof != nil {
		u.DisplayName = strings.TrimSpace(prof.DisplayName)
		u.Team = strings.TrimSpace(prof.Team)
	}
	if err := h.db.Create(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// IntegAddGroupMember POST /api/v1/integ/group-members {username, group, duration_days}
// 把用户加入权限组，时限绑成员关系（7/30/90 天或 0=永久），到期自动失去整组权限；重复加入会重置时限。
func (h *H) IntegAddGroupMember(c *gin.Context) {
	var req struct {
		Username     string   `json:"username"`
		Group        string   `json:"group"`         // 权限组名（单选，兼容旧参数）
		Groups       []string `json:"groups"`        // 权限组名（多选；与 group 合并去重）
		GroupID      uint     `json:"group_id"`      // 或直接传组 ID（单选，与 name 参数合并）
		DurationDays int      `json:"duration_days"` // 7 / 30 / 90 / 0(永久)
		ExpiresAt    string   `json:"expires_at"`    // 可选：绝对到期时间 "2026-09-30 00:00:00"（优先于 duration_days）
		DisplayName  string   `json:"display_name"`  // 可选：申请人姓名，随入组写入用户资料
		Team         string   `json:"team"`          // 可选：申请人团队
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误：需要 username 与 group(s)"})
		return
	}
	names := integListParam(req.Group, req.Groups)
	if len(names) == 0 && req.GroupID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误：需要 username 与 group(s)"})
		return
	}
	expiresAt, expLabel, err := integResolveExpiry(req.ExpiresAt, req.DurationDays)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	// 全部组校验通过才执行（防加入一半）
	gList := map[uint]model.GrantGroup{}
	for _, name := range names {
		var g model.GrantGroup
		if err := h.db.Where("name = ?", name).First(&g).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"message": "权限组不存在: " + name})
			return
		}
		gList[g.ID] = g
	}
	if req.GroupID != 0 {
		var g model.GrantGroup
		if err := h.db.First(&g, req.GroupID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"message": "权限组不存在"})
			return
		}
		gList[g.ID] = g
	}
	u, err := h.integFindOrCreateUser(req.Username, &integProfile{DisplayName: req.DisplayName, Team: req.Team})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "创建用户失败: " + err.Error()})
		return
	}
	if u.Role == model.RoleSuperAdmin {
		c.JSON(http.StatusForbidden, gin.H{"message": "平台管理员账号请在管理页操作，不支持工单通道"})
		return
	}

	// 重复加入视为续期：逐组删旧成员关系再写入新时限
	joined := make([]string, 0, len(gList))
	rejoined := false
	for _, g := range gList {
		del := h.db.Where("user_id = ? AND group_id = ?", u.ID, g.ID).Delete(&model.UserGroup{})
		if del.RowsAffected > 0 {
			rejoined = true
		}
		member := model.UserGroup{UserID: u.ID, GroupID: g.ID, ExpiresAt: expiresAt}
		if err := h.db.Create(&member).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "加入权限组失败: " + err.Error()})
			return
		}
		joined = append(joined, g.Name)
	}
	auth.RevokeAllOfUser(h.db, u.ID, "integ-group-join") // 在线用户强制重新登录以刷新权限
	syncAuthz(h.db)

	// 站内消息：重复加入（删了旧成员关系）= 续期，否则为加入
	if rejoined {
		notify.Push(h.db, u.ID, notify.TypeGrantRenewed, "权限已续期",
			"权限组「"+strings.Join(joined, "、")+"」成员时限已重置"+expirySuffix(expiresAt), nil)
	} else {
		notify.Push(h.db, u.ID, notify.TypeGrantAdded, "权限已新增",
			"已加入权限组「"+strings.Join(joined, "、")+"」"+expirySuffix(expiresAt), nil)
	}

	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "integ_group_join", "", "", "", strings.Join(joined, ","),
		req.Username+" 加入权限组（时限 "+expLabel+"）", "success"))
	resp := gin.H{
		"message":    "已加入权限组",
		"username":   u.Username,
		"groups":     joined,
		"expires_at": expiresAt,
	}
	if len(joined) == 1 {
		resp["group"] = joined[0]
	}
	c.JSON(http.StatusOK, resp)
}

// IntegRemoveGroupMember POST /api/v1/integ/group-members/remove {username, group(s)}
func (h *H) IntegRemoveGroupMember(c *gin.Context) {
	var req struct {
		Username string   `json:"username"`
		Group    string   `json:"group"`    // 单选（兼容旧参数）
		Groups   []string `json:"groups"`   // 多选（与 group 合并去重）
		GroupID  uint     `json:"group_id"` // 或直接传组 ID（单选）
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误：需要 username 与 group(s)"})
		return
	}
	names := integListParam(req.Group, req.Groups)
	if len(names) == 0 && req.GroupID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误：需要 username 与 group(s)"})
		return
	}
	var u model.User
	if err := h.db.Where("username = ?", req.Username).First(&u).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "用户不存在"})
		return
	}
	if u.Role == model.RoleSuperAdmin {
		c.JSON(http.StatusForbidden, gin.H{"message": "平台管理员账号请在管理页操作，不支持工单通道"})
		return
	}
	// 组名 / 组 ID 全部校验通过再移出
	gIDs := make([]uint, 0, len(names)+1)
	gNames := make([]string, 0, len(names)+1)
	for _, name := range names {
		var g model.GrantGroup
		if err := h.db.Where("name = ?", name).First(&g).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"message": "权限组不存在: " + name})
			return
		}
		gIDs = append(gIDs, g.ID)
		gNames = append(gNames, g.Name)
	}
	if req.GroupID != 0 {
		var g model.GrantGroup
		if err := h.db.First(&g, req.GroupID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"message": "权限组不存在"})
			return
		}
		gIDs = append(gIDs, g.ID)
		gNames = append(gNames, g.Name)
	}
	h.db.Where("user_id = ? AND group_id IN ?", u.ID, gIDs).Delete(&model.UserGroup{})
	auth.RevokeAllOfUser(h.db, u.ID, "integ-group-leave")
	syncAuthz(h.db)
	notify.Push(h.db, u.ID, notify.TypeGrantRemoved, "权限已移除", "已移出权限组「"+strings.Join(gNames, "、")+"」", nil)
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "integ_group_leave", "", "", "", strings.Join(gNames, ","),
		req.Username+" 移出权限组", "success"))
	resp := gin.H{"message": "已移出权限组", "username": u.Username, "groups": gNames}
	if len(gNames) == 1 {
		resp["group"] = gNames[0]
	}
	c.JSON(http.StatusOK, resp)
}

// IntegListGrants GET /api/v1/integ/grants?username=&cluster= - 工单平台查询用户当前有效授权
func (h *H) IntegListGrants(c *gin.Context) {
	username := strings.TrimSpace(c.Query("username"))
	cluster := strings.TrimSpace(c.Query("cluster"))
	var u model.User
	if err := h.db.Where("username = ?", username).First(&u).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "用户不存在"})
		return
	}
	grants := middleware.EffectiveGrants(h.db, u.ID) // 已过滤过期
	var cls []model.Cluster
	h.db.Find(&cls)
	names := map[uint]string{}
	for i := range cls {
		names[cls[i].ID] = cls[i].Name
	}
	out := []gin.H{}
	for _, g := range grants {
		name := names[g.ClusterID]
		if cluster != "" && name != cluster {
			continue
		}
		out = append(out, gin.H{
			"cluster":    name,
			"namespace":  g.Namespace,
			"resource":   g.Resource,
			"verb":       g.Verb,
			"actions":    g.Actions,
			"expires_at": g.ExpiresAt,
			"expired":    false,
		})
	}
	// 含已过期但未清理的直接授权（工单续期参考）
	var expired []model.UserGrant
	h.db.Where("user_id = ? AND expires_at IS NOT NULL AND expires_at <= ?", u.ID, time.Now()).
		Find(&expired)
	for _, g := range expired {
		name := names[g.ClusterID]
		if cluster != "" && name != cluster {
			continue
		}
		out = append(out, gin.H{
			"cluster":    name,
			"namespace":  g.Namespace,
			"resource":   g.Resource,
			"verb":       g.Verb,
			"actions":    g.Actions,
			"expires_at": g.ExpiresAt,
			"expired":    true,
		})
	}
	c.JSON(http.StatusOK, out)
}

// ---------- 工单申请单账号开通（集群/角色/sid/姓名/团队/账号类型/有效期）----------

// IntegProvisionUser POST /api/v1/integ/users — 按工单申请单开通/续期账号
// 个人账号：预建 NSSO 用户（登录时按 sid 匹配补全）；服务账号：本地密码账号（明文密码仅本次返回，转交申请人）。
// 「角色」= 权限组（管理页维护，组内规则含集群/命名空间/动作，组内容调整组员权限即时跟随）；
// groups 支持多选（与旧单选参数 group 合并去重），空组名不允许——避免悄悄开成无权限账号。
// clusters 可选（多选）：传了则校验每个所选组都配置了所有所选集群的规则。
// 有效期同时作用于账号与全部组成员关系（到期自动失去整组权限）。
func (h *H) IntegProvisionUser(c *gin.Context) {
	var req struct {
		Username     string   `json:"username"`      // 授权 sid（AD 账号）
		DisplayName  string   `json:"display_name"`  // 申请人姓名
		Team         string   `json:"team"`          // 申请人团队
		AccountType  string   `json:"account_type"`  // personal / service
		Role         string   `json:"role"`          // 平台角色：Viewer/Developer/ClusterAdmin（可选）
		DurationDays int      `json:"duration_days"` // 账号有效期 7 / 30 / 90 / 0(永久)
		ExpiresAt    string   `json:"expires_at"`    // 可选：绝对到期时间 "2026-09-30 00:00:00"（优先于 duration_days）
		Group        string   `json:"group"`         // 权限组名（单选，兼容旧参数）
		Groups       []string `json:"groups"`        // 权限组名（多选；与 group 合并去重，申请单「角色」下拉）
		Cluster      string   `json:"cluster"`       // 可选：校验所选组已配置该集群的规则（单选，兼容旧参数）
		Clusters     []string `json:"clusters"`      // 可选：校验每个所选组都配置了所有所选集群的规则（多选）
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误：需要 username"})
		return
	}
	at := orStr(strings.TrimSpace(req.AccountType), "personal")
	if at != "personal" && at != "service" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "account_type 仅支持 personal / service"})
		return
	}
	// 有效期：expires_at（绝对时间）优先于 duration_days；账号与组成员关系同届
	expiresAt, expLabel, err := integResolveExpiry(req.ExpiresAt, req.DurationDays)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if req.Role != "" && !integRoles[req.Role] {
		c.JSON(http.StatusBadRequest, gin.H{"message": "role 仅支持 Viewer / Developer / ClusterAdmin（平台管理员请在管理页设置）"})
		return
	}
	// 权限组：申请单「角色」，内容在管理页维护；空组名（含未创建的组）不允许——避免悄悄开成无权限账号。
	// 支持多选（groups），全部校验通过才执行（防半开通）；与旧单选参数 group 合并去重
	groupNames := integListParam(req.Group, req.Groups)
	var gList []model.GrantGroup
	for _, name := range groupNames {
		var g model.GrantGroup
		if err := h.db.Where("name = ?", name).First(&g).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"message": "权限组不存在: " + name + "（请在管理页先创建）"})
			return
		}
		gList = append(gList, g)
	}
	// 可选集群校验（支持多选）：每个所选组都必须已配置所有所选集群的规则（防申请单集群与角色选错组合）
	clusterNames := integListParam(req.Cluster, req.Clusters)
	for _, cn := range clusterNames {
		var cl model.Cluster
		if err := h.db.Where("name = ?", cn).First(&cl).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"message": "集群不存在: " + cn})
			return
		}
		for _, g := range gList {
			var n int64
			h.db.Model(&model.GrantGroupRule{}).Where("group_id = ? AND cluster_id = ?", g.ID, cl.ID).Count(&n)
			if n == 0 {
				c.JSON(http.StatusBadRequest, gin.H{"message": "权限组 " + g.Name + " 未配置集群 " + cl.Name + " 的规则，请检查申请单或先在管理页补充组规则"})
				return
			}
		}
	}

	// 用户：不存在则按账号类型开户；存在则更新资料/角色/有效期（重复提交=续期）
	var u model.User
	created := false
	plainPassword := ""
	if err := h.db.Where("username = ?", req.Username).First(&u).Error; err != nil {
		u = model.User{
			Username:    req.Username,
			DisplayName: strings.TrimSpace(req.DisplayName),
			Team:        strings.TrimSpace(req.Team),
			AccountType: at,
			Role:        orStr(req.Role, model.RoleViewer),
			Status:      1,
			ExpiresAt:   expiresAt,
		}
		if at == "service" {
			pw, err := integGenPassword(&u)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": "生成服务账号密码失败"})
				return
			}
			plainPassword = pw
		} else {
			u.Source = "sso" // 个人账号走 NSSO 统一认证，登录时按 sid 匹配
		}
		if err := h.db.Create(&u).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "创建用户失败: " + err.Error()})
			return
		}
		created = true
	} else {
		if u.Role == model.RoleSuperAdmin {
			c.JSON(http.StatusForbidden, gin.H{"message": "平台管理员账号请在管理页操作，不支持工单通道"})
			return
		}
		if req.DisplayName != "" {
			u.DisplayName = strings.TrimSpace(req.DisplayName)
		}
		if req.Team != "" {
			u.Team = strings.TrimSpace(req.Team)
		}
		u.AccountType = at
		if req.Role != "" {
			u.Role = req.Role
		}
		u.ExpiresAt = expiresAt
		// 转/补服务账号：非本地来源或尚未设密码时生成新密码（明文仅本次返回）
		if at == "service" && (u.Source != "local" || u.PasswordHash == "") {
			pw, err := integGenPassword(&u)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": "生成服务账号密码失败"})
				return
			}
			plainPassword = pw
		}
		// 服务转个人：关闭本地密码通道，回到 NSSO 登录
		if at == "personal" && u.Source == "local" {
			u.Source = "sso"
			u.PasswordHash = ""
		}
		if err := h.db.Save(&u).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "更新用户失败: " + err.Error()})
			return
		}
	}

	resp := gin.H{
		"message":      "开通成功",
		"username":     u.Username,
		"account_type": u.AccountType,
		"role":         u.Role,
		"expires_at":   u.ExpiresAt,
		"created":      created,
	}
	// 可选：加入权限组（申请单「角色」，支持多选），时限与账号同届；重复提交=续期（重置成员时限）
	if len(gList) > 0 {
		for _, g := range gList {
			h.db.Where("user_id = ? AND group_id = ?", u.ID, g.ID).Delete(&model.UserGroup{})
			member := model.UserGroup{UserID: u.ID, GroupID: g.ID, ExpiresAt: expiresAt}
			if err := h.db.Create(&member).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"message": "加入权限组失败: " + err.Error()})
				return
			}
		}
		if len(gList) == 1 {
			resp["group"] = gList[0].Name
		}
		resp["groups"] = groupNames
	}
	auth.RevokeAllOfUser(h.db, u.ID, "integ-user-provision") // 开通/续期后强制重登，权限与有效期即时生效
	syncAuthz(h.db)
	// 站内消息：开通 = 新增，重复提交 = 续期（账号时限重置；入组时限与账号同届）
	notifContent := "账号权限时限：" + notifyExpiryText(u.ExpiresAt)
	if len(groupNames) > 0 {
		notifContent += "，已加入权限组「" + strings.Join(groupNames, "、") + "」"
	}
	if created {
		notify.Push(h.db, u.ID, notify.TypeGrantAdded, "权限已开通", notifContent, nil)
	} else {
		notify.Push(h.db, u.ID, notify.TypeGrantRenewed, "权限已续期", notifContent, nil)
	}
	detail := fmt.Sprintf("账号开通/更新 type=%s role=%s 时限=%s", u.AccountType, u.Role, expLabel)
	if len(gList) > 0 {
		detail += " groups=" + strings.Join(groupNames, ",")
	}
	if len(clusterNames) > 0 {
		detail += " clusters=" + strings.Join(clusterNames, ",")
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "integ_user_provision", "", "", "", u.Username, detail, "success"))
	if plainPassword != "" {
		resp["plain_password"] = plainPassword // 服务账号密码仅此一次返回，工单平台转交申请人
	}
	c.JSON(http.StatusOK, resp)
}

// integGenPassword 生成服务账号随机密码并写入用户（Source=local + bcrypt 哈希），返回明文
func integGenPassword(u *model.User) (string, error) {
	pw := randomPassword()
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	u.Source = "local"
	u.PasswordHash = string(hash)
	return pw, nil
}

// IntegGetUser GET /api/v1/integ/users/:username — 工单平台查询账号状态（续期/回收前参考）
func (h *H) IntegGetUser(c *gin.Context) {
	var u model.User
	if err := h.db.Where("username = ?", c.Param("username")).First(&u).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "用户不存在"})
		return
	}
	grants := middleware.EffectiveGrants(h.db, u.ID) // 已过滤过期（含权限组规则展开）
	// 所属权限组（工单申请单「角色」的落点），含成员时限
	groups := []gin.H{}
	var mbs []model.UserGroup
	h.db.Where("user_id = ?", u.ID).Find(&mbs)
	if len(mbs) > 0 {
		ids := make([]uint, 0, len(mbs))
		for _, m := range mbs {
			ids = append(ids, m.GroupID)
		}
		var gs []model.GrantGroup
		h.db.Where("id IN ?", ids).Find(&gs)
		nameOf := map[uint]string{}
		for _, g := range gs {
			nameOf[g.ID] = g.Name
		}
		for _, m := range mbs {
			expired := m.ExpiresAt != nil && m.ExpiresAt.Before(time.Now())
			groups = append(groups, gin.H{"name": nameOf[m.GroupID], "expires_at": m.ExpiresAt, "expired": expired})
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"username":      u.Username,
		"display_name":  u.DisplayName,
		"team":          u.Team,
		"account_type":  u.AccountType,
		"role":          u.Role,
		"status":        u.Status,
		"expires_at":    u.ExpiresAt,
		"expired":       u.Expired(),
		"groups":        groups,
		"active_grants": len(grants),
	})
}

// IntegListClusters GET /api/v1/integ/clusters — 工单平台集群枚举（申请单「集群」下拉）
func (h *H) IntegListClusters(c *gin.Context) {
	var cls []model.Cluster
	h.db.Order("id ASC").Find(&cls)
	out := []gin.H{}
	for _, cl := range cls {
		out = append(out, gin.H{
			"name":        cl.Name,
			"description": cl.Description,
			"status":      cl.Status,
			"version":     cl.Version,
			"node_count":  cl.NodeCount,
		})
	}
	c.JSON(http.StatusOK, out)
}

// IntegListUsers GET /api/v1/integ/users — 全量用户 + 每人权限清单（供安全部门盘点）
// 口径与 /admin/users/export（审计部门 Excel）一致：直接授权 + 权限组规则并集，
// 含禁用账号与已过期条目并各自标记，不删数据；不含密码哈希 / 令牌等敏感字段。
// 可选 ?username=xxx 只查单个账号。
func (h *H) IntegListUsers(c *gin.Context) {
	q := h.db.Order("id ASC")
	if un := strings.TrimSpace(c.Query("username")); un != "" {
		q = q.Where("username = ?", un)
	}
	var users []model.User
	q.Find(&users)

	// 全平台授权数据一次拉齐，内存组装（避免逐用户 N+1）
	var cls []model.Cluster
	h.db.Find(&cls)
	names := map[uint]string{}
	for _, cl := range cls {
		names[cl.ID] = cl.Name
	}
	var grants []model.UserGrant
	h.db.Find(&grants)
	var mbs []model.UserGroup
	h.db.Find(&mbs)
	var rules []model.GrantGroupRule
	h.db.Find(&rules)
	var groups []model.GrantGroup
	h.db.Find(&groups)
	gname := map[uint]string{}
	for _, g := range groups {
		gname[g.ID] = g.Name
	}
	grantsByUser := map[uint][]model.UserGrant{}
	for _, g := range grants {
		grantsByUser[g.UserID] = append(grantsByUser[g.UserID], g)
	}
	mbsByUser := map[uint][]model.UserGroup{}
	for _, m := range mbs {
		mbsByUser[m.UserID] = append(mbsByUser[m.UserID], m)
	}
	rulesByGroup := map[uint][]model.GrantGroupRule{}
	for _, r := range rules {
		rulesByGroup[r.GroupID] = append(rulesByGroup[r.GroupID], r)
	}

	frozen := time.Now() // 过期判定统一取本次查询的快照时刻
	out := make([]gin.H, 0, len(users))
	for _, u := range users {
		groups := []gin.H{}
		grantsOut := []gin.H{}

		if u.Role == model.RoleSuperAdmin {
			// 平台管理员：隐式全权限，单行 * 汇总
			grantsOut = append(grantsOut, gin.H{
				"source": "平台管理员", "cluster": "*", "namespace": "*", "resource": "*",
				"verb": "*", "actions": "*", "created_at": nil, "expires_at": nil, "expired": false,
			})
		} else {
			// 直接授权（含已过期并标记）
			for _, g := range grantsByUser[u.ID] {
				grantsOut = append(grantsOut, gin.H{
					"source":     "直接授权",
					"cluster":    clusterNameOf(names, g.ClusterID),
					"namespace":  orStr(g.Namespace, "*"),
					"resource":   orStr(g.Resource, "*"),
					"verb":       orStr(g.Verb, "*"),
					"actions":    orStr(g.Actions, "*"),
					"created_at": g.CreatedAt,
					"expires_at": g.ExpiresAt, // NULL=永久
					"expired":    g.ExpiresAt != nil && g.ExpiresAt.Before(frozen),
				})
			}
			// 权限组规则（到期 = 成员关系时限，created_at = 入组时间）
			for _, m := range mbsByUser[u.ID] {
				groups = append(groups, gin.H{
					"name":              gname[m.GroupID],
					"joined_at":         m.CreatedAt,
					"member_expires_at": m.ExpiresAt, // NULL=永久
					"member_expired":    m.ExpiresAt != nil && m.ExpiresAt.Before(frozen),
				})
				for _, r := range rulesByGroup[m.GroupID] {
					src := "权限组"
					if n := gname[m.GroupID]; n != "" {
						src = "权限组·" + n
					}
					grantsOut = append(grantsOut, gin.H{
						"source":     src,
						"cluster":    clusterNameOf(names, r.ClusterID),
						"namespace":  orStr(r.Namespace, "*"),
						"resource":   orStr(r.Resource, "*"),
						"verb":       orStr(r.Verb, "*"),
						"actions":    orStr(r.Actions, "*"),
						"created_at": m.CreatedAt,
						"expires_at": m.ExpiresAt,
						"expired":    m.ExpiresAt != nil && m.ExpiresAt.Before(frozen),
					})
				}
			}
		}

		out = append(out, gin.H{
			"username":      u.Username,
			"display_name":  u.DisplayName,
			"email":         u.Email,
			"team":          u.Team,
			"account_type":  u.AccountType, // personal 个人 / service 服务
			"source":        u.Source,      // local / ldap / sso
			"role":          u.Role,
			"status":        map[bool]string{true: "enabled", false: "disabled"}[u.Status == 1],
			"expires_at":    u.ExpiresAt, // 账号有效期，NULL=永久
			"expired":       u.Expired(),
			"last_login_at": u.LastLoginAt,
			"created_at":    u.CreatedAt,
			"groups":        groups,
			"grants":        grantsOut,
		})
	}

	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "integ_users_export", "", "", "", "",
		fmt.Sprintf("全量用户权限清单查询（%d 个账号）", len(users)), "success"))
	c.JSON(http.StatusOK, gin.H{"total": len(users), "users": out})
}
