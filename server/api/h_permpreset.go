package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"kubeyard/internal/audit"
	"kubeyard/internal/middleware"
	"kubeyard/internal/model"
)

// ---------- 权限预设（系统设置 → 权限预设，免发版维护动作目录与角色快捷预设）----------

// permPresetKeys 角色快捷预设仅允许这三个键
var permPresetKeys = map[string]bool{"viewer": true, "dev": true, "admin": true}

// reservedActionKeys 动作目录禁用的键：与动词/CR 简单令牌冲突会混淆鉴权语义
var reservedActionKeys = map[string]bool{
	"*": true, "get": true, "list": true, "watch": true, "create": true,
	"update": true, "delete": true, "exec": true, "logs": true, "download": true, "upload": true,
}

// GetPermPresets GET /api/v1/admin/perm-presets — 合并后的生效配置（内置 ⊕ 自定义整体替换）
func (h *H) GetPermPresets(c *gin.Context) {
	c.JSON(http.StatusOK, middleware.EffectivePermPresets(h.db))
}

// PutPermPresets PUT /api/v1/admin/perm-presets — 保存自定义预设（校验后存 Setting，缓存立即失效）
func (h *H) PutPermPresets(c *gin.Context) {
	var req middleware.PermPresets
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误: " + err.Error()})
		return
	}
	if err := validatePermPresets(&req); err != "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": err})
		return
	}
	raw, _ := json.Marshal(req)
	var s model.Setting
	if err := h.db.Where("`key` = ?", "perm_presets").First(&s).Error; err == nil {
		s.Value = string(raw)
		h.db.Save(&s)
	} else {
		h.db.Create(&model.Setting{Key: "perm_presets", Value: string(raw)})
	}
	middleware.InvalidatePermPresets()
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "perm_preset_update", "", "", "", "",
		"权限预设已更新（动作目录 "+strconv.Itoa(len(req.Actions))+" 资源 / 角色预设 "+strconv.Itoa(len(req.Presets))+" 项）", "success"))
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// DeletePermPresets DELETE /api/v1/admin/perm-presets — 恢复内置默认
func (h *H) DeletePermPresets(c *gin.Context) {
	h.db.Where("`key` = ?", "perm_presets").Delete(&model.Setting{})
	middleware.InvalidatePermPresets()
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "perm_preset_reset", "", "", "", "", "权限预设恢复默认", "success"))
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// validatePermPresets 返回错误文案（空 = 通过）
func validatePermPresets(pp *middleware.PermPresets) string {
	if pp.ResourcePresetMode != "" && pp.ResourcePresetMode != "legacy" && pp.ResourcePresetMode != "scoped" {
		return "resource_preset_mode 仅支持 legacy / scoped"
	}
	for res, actions := range pp.Actions {
		if res == "" || len(res) > 255 || strings.ContainsAny(res, ", ") {
			return "动作目录资源名非法: " + res
		}
		for key, meta := range actions {
			if key == "" || len(key) > 64 || strings.ContainsAny(key, ", ") {
				return "动作键非法: " + key + "（禁止逗号/空格）"
			}
			if reservedActionKeys[key] {
				return "动作键与动词/CR 令牌冲突: " + key
			}
			if len(meta.Label) > 128 {
				return "动作显示名过长: " + key
			}
			for _, f := range meta.Fields {
				if f == "" || strings.ContainsAny(f, ", ") {
					return "动作 " + key + " 的字段路径非法（禁止逗号/空格；顶层字段如 data 也合法）: " + f
				}
			}
		}
	}
	for name, p := range pp.Presets {
		if !permPresetKeys[name] {
			return "角色预设仅支持 viewer/dev/admin，收到: " + name
		}
		if len(p.Verbs) == 0 {
			return "角色预设 " + name + " 的动词/动作不能为空"
		}
	}
	for res, roles := range pp.ResourcePresets {
		if res == "" || len(res) > 255 || strings.ContainsAny(res, ", ") {
			return "资源级预设的资源名非法: " + res
		}
		for name, p := range roles {
			if !permPresetKeys[name] {
				return "资源级预设仅支持 viewer/dev/admin，收到: " + name
			}
			if p == nil {
				continue
			}
			if len(p.Verbs) == 0 {
				return "资源级预设 " + res + "/" + name + " 的动词/动作不能为空"
			}
			if msg := validateResourcePresetAffinity(res, *p, pp); msg != "" {
				return msg
			}
		}
	}
	return ""
}

// validateResourcePresetAffinity 资源级预设必须与该资源的动词/动作目录匹配；
// 全局预设仍是跨资源模板（应用时裁剪），但单资源预设没有理由保存不适配令牌。
func validateResourcePresetAffinity(res string, p middleware.RolePreset, pp *middleware.PermPresets) string {
	catalog := map[string]middleware.ActionMeta{}
	if m := middleware.BuiltinActionCatalog()[res]; m != nil {
		for k, v := range m {
			catalog[k] = v
		}
	}
	if m, ok := pp.Actions[res]; ok {
		if len(m) == 0 {
			catalog = map[string]middleware.ActionMeta{}
		} else {
			for k, v := range m {
				catalog[k] = v
			}
		}
	}
	isCR := strings.Contains(res, "/")
	for _, v := range p.Verbs {
		if v == "*" {
			continue
		}
		switch v {
		case "get", "list", "watch", "create", "update", "delete":
		case "exec", "logs", "download", "upload":
			if res != "pods" {
				return "资源级预设 " + res + " 不能配置 Pod 专属动词: " + v
			}
		default:
			return "资源级预设 " + res + " 的动词不合法: " + v
		}
	}
	for _, a := range p.Actions {
		switch {
		case a == "*" || a == "update" || strings.Contains(a, "."):
			continue
		case isCR && (a == "create" || a == "delete"):
			continue
		}
		if _, ok := catalog[a]; !ok {
			return "资源级预设 " + res + " 的动作不在该资源目录中: " + a
		}
	}
	return ""
}

// ---------- 批量同步：清理已有授权中已从动作目录移除的令牌 ----------

// staleTokens 计算规则 actions 里已失效的令牌（不在该规则任何所选资源的目录中）。
// 保留：*、字段路径（含 .）、动词与 CR 简单令牌（create/update/delete）。
func staleTokens(actions, resource string, catalog map[string]map[string]middleware.ActionMeta) []string {
	var resList []string
	allRes := strings.TrimSpace(resource) == "" || strings.TrimSpace(resource) == "*"
	if !allRes {
		for _, r := range strings.Split(resource, ",") {
			if r = strings.TrimSpace(r); r != "" {
				resList = append(resList, r)
			}
		}
	}
	var stale []string
	for _, tok := range strings.Split(actions, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" || tok == "*" || strings.Contains(tok, ".") || reservedActionKeys[tok] {
			continue
		}
		if allRes {
			ok := false
			for _, m := range catalog {
				if _, hit := m[tok]; hit {
					ok = true
					break
				}
			}
			if !ok {
				stale = append(stale, tok)
			}
			continue
		}
		ok := false
		for _, r := range resList {
			if m := catalog[r]; m != nil {
				if _, hit := m[tok]; hit {
					ok = true
					break
				}
			}
		}
		if !ok {
			stale = append(stale, tok)
		}
	}
	return stale
}

type staleRule struct {
	Kind       string   `json:"kind"` // user / group
	ID         uint     `json:"id"`
	Owner      string   `json:"owner"` // 用户名 / 权限组名
	ClusterID  uint     `json:"cluster_id"`
	Namespace  string   `json:"namespace"`
	Resource   string   `json:"resource"`
	Verb       string   `json:"verb"`
	Actions    string   `json:"actions"`
	Stale      []string `json:"stale"`
	NewVerb    string   `json:"new_verb,omitempty"`    // 预设对齐模式：对齐后的动词
	NewActions string   `json:"new_actions,omitempty"` // 预设对齐模式：对齐后的动作
	SyncType   string   `json:"sync_type"`             // align:<role> / stale / targeted（全量检查前端分组用）
	Preset     string   `json:"preset,omitempty"`      // 对齐到的角色
}

// SyncPermPresets POST /api/v1/admin/perm-presets/sync body {apply, mode, resource, action, preset}
// 四种模式（互斥；未传 mode 时按旧参数判定，保持既有调用兼容）：
//   - 默认：dry-run/清理已从动作目录移除的失效令牌
//   - resource+action：从已有授权移除指定动作令牌（目录条目保留，可随时重新授权）
//   - preset：把与该角色预设「同形态」（动词集合一致）的已有授权刷新为预设最新动词+动作
//   - mode=all：一次检查三种角色预设对齐与失效令牌清理，并按各自类型应用
func (h *H) SyncPermPresets(c *gin.Context) {
	var req struct {
		Apply    bool   `json:"apply"`
		Mode     string `json:"mode"`     // all = 一键检查并对齐；空 = 兼容旧模式
		Resource string `json:"resource"` // 指定动作清理模式：资源名
		Action   string `json:"action"`   // 指定动作清理模式：动作键（须在该资源目录中）
		Preset   string `json:"preset"`   // 预设对齐模式：viewer / dev / admin
	}
	_ = c.BindJSON(&req)
	fullMode := req.Mode == "all"
	if req.Mode != "" && req.Mode != "all" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "mode 仅支持 all"})
		return
	}
	if fullMode && req.Resource != "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "mode=all 不支持指定资源清理"})
		return
	}
	eff := middleware.EffectivePermPresets(h.db)
	catalog := eff.Actions

	targeted := req.Action != ""
	if targeted {
		if m := catalog[req.Resource]; m == nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": "资源 " + req.Resource + " 不在动作目录中"})
			return
		} else if _, ok := m[req.Action]; !ok {
			c.JSON(http.StatusBadRequest, gin.H{"message": "动作 " + req.Action + " 不在 " + req.Resource + " 的动作目录中"})
			return
		}
	}
	staleOf := func(actions, resource string) []string {
		if targeted {
			if !resourceListed(resource, req.Resource) || !hasActionToken(actions, req.Action) {
				return nil
			}
			return []string{req.Action}
		}
		return staleTokens(actions, resource, catalog)
	}

	// 预设对齐：同形态 = 动词集合与预设当前（或内置——预设动词被改过时）一致，
	// 或与预设按该规则资源裁剪后的动词集一致（已对齐过的裁剪形态，预设再调整时仍能被匹配）；
	// 动词为 * 的授权（管理员全量）与 CR 规则不参与（内置预设的动词/动作对 CR 无意义）
	presetMode := req.Preset != ""
	var curPreset, builtinPreset middleware.RolePreset
	if presetMode {
		var okCur, okBuiltin bool
		curPreset, okCur = eff.Presets[req.Preset]
		builtinPreset, okBuiltin = middleware.BuiltinRolePreset(req.Preset)
		if !okCur || !okBuiltin {
			c.JSON(http.StatusBadRequest, gin.H{"message": "角色预设仅支持 viewer/dev/admin"})
			return
		}
	}

	// 用户名 / 组名映射
	userNames := map[uint]string{}
	var users []model.User
	h.db.Find(&users)
	for _, u := range users {
		userNames[u.ID] = u.Username
	}
	groupNames := map[uint]string{}
	var groups []model.GrantGroup
	h.db.Find(&groups)
	for _, g := range groups {
		groupNames[g.ID] = g.Name
	}

	presetKinds := []string{"viewer", "dev", "admin"}
	buildItem := func(kind string, id uint, owner string, clusterID uint, ns, res, verb, actions string) (staleRule, bool) {
		if fullMode {
			if item, ok := presetAlignmentItem(kind, id, owner, clusterID, ns, res, verb, actions, eff, presetKinds); ok {
				return item, true
			}
			st := staleOf(actions, res)
			if len(st) == 0 {
				return staleRule{}, false
			}
			return staleRule{
				Kind: kind, ID: id, Owner: owner, ClusterID: clusterID, Namespace: ns,
				Resource: res, Verb: verb, Actions: actions, Stale: st, SyncType: "stale",
			}, true
		}
		if presetMode {
			if strings.Contains(res, "/") {
				return staleRule{}, false
			}
			// 对齐目标 = 预设按该规则资源范围裁剪后的形态（非 pods 资源不带 exec/logs/download
			// 等 Pod 专属动词，动作仅保留该资源目录支持的项；目录外资源 update 令牌 = 完整更新）
			trimmed := middleware.TrimPresetForResources(middleware.EffectiveRolePreset(eff, req.Preset, res), res, catalog)
			v := splitSet(verb)
			if len(v) == 0 || v["*"] {
				return staleRule{}, false
			}
			if !(verbSetEqual(v, curPreset.Verbs) || verbSetEqual(v, builtinPreset.Verbs) || verbSetEqual(v, trimmed.Verbs)) {
				return staleRule{}, false // 与预设任何形态都不同形，不参与对齐
			}
			if verbSetEqual(v, trimmed.Verbs) && verbSetEqual(splitSet(actions), trimmed.Actions) {
				return staleRule{}, false // 已是最新（裁剪形态）
			}
			return staleRule{
				Kind: kind, ID: id, Owner: owner, ClusterID: clusterID, Namespace: ns,
				Resource: res, Verb: verb, Actions: actions,
				NewVerb: strings.Join(trimmed.Verbs, ","), NewActions: strings.Join(trimmed.Actions, ","),
				SyncType: "align:" + req.Preset, Preset: req.Preset,
			}, true
		}
		st := staleOf(actions, res)
		if len(st) == 0 {
			return staleRule{}, false
		}
		return staleRule{
			Kind: kind, ID: id, Owner: owner, ClusterID: clusterID, Namespace: ns,
			Resource: res, Verb: verb, Actions: actions, Stale: st,
			SyncType: map[bool]string{true: "targeted", false: "stale"}[targeted],
		}, true
	}

	var items []staleRule
	var grants []model.UserGrant
	h.db.Find(&grants)
	for _, g := range grants {
		if g.ExpiresAt != nil && g.ExpiresAt.Before(time.Now()) {
			continue // 已过期授权不参与清理/对齐（改期续授后可再跑同步）
		}
		if it, ok := buildItem("user", g.ID, userNames[g.UserID], g.ClusterID, g.Namespace, g.Resource, g.Verb, g.Actions); ok {
			items = append(items, it)
		}
	}
	var rules []model.GrantGroupRule
	h.db.Find(&rules)
	for _, r := range rules {
		if it, ok := buildItem("group", r.ID, groupNames[r.GroupID], r.ClusterID, r.Namespace, r.Resource, r.Verb, r.Actions); ok {
			items = append(items, it)
		}
	}
	if !req.Apply {
		c.JSON(http.StatusOK, gin.H{"items": items, "applied": 0})
		return
	}

	updated := 0
	for _, it := range items {
		if presetMode || (fullMode && strings.HasPrefix(it.SyncType, "align:")) {
			// 对齐：动词+动作整体刷新为预设最新内容（范围三要素不动）
			updates := map[string]any{"verb": it.NewVerb, "actions": it.NewActions}
			if it.Kind == "user" {
				h.db.Model(&model.UserGrant{}).Where("id = ?", it.ID).Updates(updates)
			} else {
				h.db.Model(&model.GrantGroupRule{}).Where("id = ?", it.ID).Updates(updates)
			}
			updated++
			audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "perm_preset_sync", "", it.Namespace, it.Resource, it.Owner,
				"对齐预设 "+it.Preset+": "+it.Verb+" / "+it.Actions+" → "+it.NewVerb+" / "+it.NewActions, "success"))
			continue
		}
		staleSet := map[string]bool{}
		for _, s := range it.Stale {
			staleSet[s] = true
		}
		var keep []string
		for _, tok := range strings.Split(it.Actions, ",") {
			if tok = strings.TrimSpace(tok); tok != "" && !staleSet[tok] {
				keep = append(keep, tok)
			}
		}
		newActions := strings.Join(keep, ",") // 清空 = update 仍授权但无可改字段（最严格），不自动降级动词
		if it.Kind == "user" {
			h.db.Model(&model.UserGrant{}).Where("id = ?", it.ID).Update("actions", newActions)
		} else {
			h.db.Model(&model.GrantGroupRule{}).Where("id = ?", it.ID).Update("actions", newActions)
		}
		updated++
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "perm_preset_sync", "", it.Namespace, it.Resource, it.Owner,
			"清理动作令牌: "+strings.Join(it.Stale, ",")+" → "+newActions, "success"))
	}
	syncAuthz(h.db)
	aligned := 0
	for _, it := range items {
		if strings.HasPrefix(it.SyncType, "align:") {
			aligned++
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "applied": updated, "aligned": aligned, "cleaned": updated - aligned})
}

// presetAlignmentItem 找出授权匹配到的第一个角色预设；预设本身可配置，角色之间没有
// 数据库外键，这里沿用「同形态才对齐」约定，并按 viewer/dev/admin 顺序去重。
func presetAlignmentItem(kind string, id uint, owner string, clusterID uint, ns, res, verb, actions string,
	eff middleware.PermPresets, presetKinds []string) (staleRule, bool) {
	if strings.Contains(res, "/") {
		return staleRule{}, false
	}
	v := splitSet(verb)
	if len(v) == 0 || v["*"] {
		return staleRule{}, false
	}
	for _, role := range presetKinds {
		cur, okCur := eff.Presets[role]
		builtin, okBuiltin := middleware.BuiltinRolePreset(role)
		if !okCur || !okBuiltin {
			continue
		}
		// scoped 模式的旧存量可能是「内置全局按资源裁剪」的形态；资源覆盖后来才生效，
		// 一键对齐要继续识别这种历史形态，否则升级后永远匹配不到。
		baseline := middleware.TrimPresetForResources(builtin, res, eff.Actions)
		trimmed := middleware.TrimPresetForResources(middleware.EffectiveRolePreset(eff, role, res), res, eff.Actions)
		if !(verbSetEqual(v, cur.Verbs) || verbSetEqual(v, builtin.Verbs) ||
			verbSetEqual(v, baseline.Verbs) || verbSetEqual(v, trimmed.Verbs)) {
			continue
		}
		if verbSetEqual(v, trimmed.Verbs) && verbSetEqual(splitSet(actions), trimmed.Actions) {
			continue
		}
		return staleRule{
			Kind: kind, ID: id, Owner: owner, ClusterID: clusterID, Namespace: ns,
			Resource: res, Verb: verb, Actions: actions,
			NewVerb: strings.Join(trimmed.Verbs, ","), NewActions: strings.Join(trimmed.Actions, ","),
			SyncType: "align:" + role, Preset: role,
		}, true
	}
	return staleRule{}, false
}

// splitSet 逗号串 → 集合（忽略空白项）
func splitSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out[t] = true
		}
	}
	return out
}

// verbSetEqual 集合与切片是否为同一集合（顺序无关）
func verbSetEqual(set map[string]bool, list []string) bool {
	if len(set) != len(list) {
		return false
	}
	for _, v := range list {
		if !set[v] {
			return false
		}
	}
	return true
}

// resourceListed 规则的 resource 串（逗号分隔；* 或空 = 全部资源）是否覆盖目标资源
func resourceListed(ruleResource, target string) bool {
	s := strings.TrimSpace(ruleResource)
	if s == "" || s == "*" {
		return true
	}
	for _, r := range strings.Split(s, ",") {
		if strings.TrimSpace(r) == target {
			return true
		}
	}
	return false
}

// hasActionToken actions 串中是否含指定令牌
func hasActionToken(actions, token string) bool {
	for _, t := range strings.Split(actions, ",") {
		if strings.TrimSpace(t) == token {
			return true
		}
	}
	return false
}
