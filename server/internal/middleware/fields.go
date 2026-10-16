package middleware

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"

	"gorm.io/gorm"

	"kubeyard/internal/authz"
	"kubeyard/internal/model"
)

// ActionMeta 可配置动作元数据：Label 供前端展示，Fields 为字段路径模式；
// Fields 为空的键是纯动作令牌（restart/stop/start 等），由 ActionAllowed 端点级检查放行，
// 不产生可修改字段。含 "." 的动作令牌视为原始字段路径直接放行（管理员自定义场景）。
type ActionMeta struct {
	Label  string   `json:"label"`
	Fields []string `json:"fields"`
}

// RolePreset 授权页「只读/开发/管理员」快捷预设的默认勾选内容
type RolePreset struct {
	Verbs   []string `json:"verbs"`
	Actions []string `json:"actions"`
}

// PermPresets 系统设置里可维护的权限预设（Setting 键 perm_presets，JSON）
type PermPresets struct {
	Actions            map[string]map[string]ActionMeta  `json:"actions"`              // 资源 → 动作键 → 元数据；整体替换内置
	Presets            map[string]RolePreset             `json:"presets"`              // viewer/dev/admin → 默认勾选；整体替换内置
	ResourcePresetMode string                            `json:"resource_preset_mode"` // legacy = 全局预设；scoped = 资源级预设
	ResourcePresets    map[string]map[string]*RolePreset `json:"resource_presets"`     // 资源 → 角色 → 覆盖；nil = 删除覆盖
}

// builtinActions 内置动作目录（与前端 utils/grant.ts 的内置回落保持一致）。
// 动作键同时用于端点级检查（replicas/restart）；纯动作令牌不出现在此表的 Fields 中。
var builtinActions = map[string]map[string]ActionMeta{
	"deployments": {
		"image":     {"更新镜像", []string{"spec.template.spec.containers.*.image", "spec.template.spec.initContainers.*.image"}},
		"command":   {"入口/命令 (Entrypoint/CMD)", []string{"spec.template.spec.containers.*.command", "spec.template.spec.containers.*.args", "spec.template.spec.initContainers.*.command", "spec.template.spec.initContainers.*.args"}},
		"ports":     {"更新端口", []string{"spec.template.spec.containers.*.ports", "spec.template.spec.initContainers.*.ports"}},
		"resources": {"资源配置（limits/requests）", []string{"spec.template.spec.containers.*.resources", "spec.template.spec.initContainers.*.resources"}},
		"env":       {"环境变量增删改", []string{"spec.template.spec.containers.*.env", "spec.template.spec.initContainers.*.env"}},
		"probes": {"健康检查（存活/就绪/启动探针）", []string{
			"spec.template.spec.containers.*.livenessProbe", "spec.template.spec.containers.*.readinessProbe", "spec.template.spec.containers.*.startupProbe",
			"spec.template.spec.initContainers.*.livenessProbe", "spec.template.spec.initContainers.*.readinessProbe", "spec.template.spec.initContainers.*.startupProbe"}},
		"storage": {"存储挂载（卷/持久化）", []string{"spec.template.spec.containers.*.volumeMounts", "spec.template.spec.initContainers.*.volumeMounts", "spec.template.spec.volumes"}},
		"scheduling": {"调度策略（节点选择/亲和）", []string{
			"spec.template.spec.nodeSelector", "spec.template.spec.nodeName", "spec.template.spec.affinity"}},
		"replicas": {"副本数", []string{"spec.replicas"}},
		"restart":  {"重启", nil},
		"stop":     {"停止", nil},
		"start":    {"启动", nil},
	},
	"statefulsets": {
		"image":     {"更新镜像", []string{"spec.template.spec.containers.*.image", "spec.template.spec.initContainers.*.image"}},
		"command":   {"入口/命令 (Entrypoint/CMD)", []string{"spec.template.spec.containers.*.command", "spec.template.spec.containers.*.args", "spec.template.spec.initContainers.*.command", "spec.template.spec.initContainers.*.args"}},
		"ports":     {"更新端口", []string{"spec.template.spec.containers.*.ports", "spec.template.spec.initContainers.*.ports"}},
		"resources": {"资源配置（limits/requests）", []string{"spec.template.spec.containers.*.resources", "spec.template.spec.initContainers.*.resources"}},
		"env":       {"环境变量增删改", []string{"spec.template.spec.containers.*.env", "spec.template.spec.initContainers.*.env"}},
		"probes": {"健康检查（存活/就绪/启动探针）", []string{
			"spec.template.spec.containers.*.livenessProbe", "spec.template.spec.containers.*.readinessProbe", "spec.template.spec.containers.*.startupProbe",
			"spec.template.spec.initContainers.*.livenessProbe", "spec.template.spec.initContainers.*.readinessProbe", "spec.template.spec.initContainers.*.startupProbe"}},
		"storage": {"存储挂载（卷/持久化）", []string{"spec.template.spec.containers.*.volumeMounts", "spec.template.spec.initContainers.*.volumeMounts", "spec.template.spec.volumes"}},
		"scheduling": {"调度策略（节点选择/亲和）", []string{
			"spec.template.spec.nodeSelector", "spec.template.spec.nodeName", "spec.template.spec.affinity"}},
		"replicas": {"副本数", []string{"spec.replicas"}},
		"restart":  {"重启", nil},
		"stop":     {"停止", nil},
		"start":    {"启动", nil},
	},
	"daemonsets": {
		"image":     {"更新镜像", []string{"spec.template.spec.containers.*.image", "spec.template.spec.initContainers.*.image"}},
		"command":   {"入口/命令 (Entrypoint/CMD)", []string{"spec.template.spec.containers.*.command", "spec.template.spec.containers.*.args", "spec.template.spec.initContainers.*.command", "spec.template.spec.initContainers.*.args"}},
		"ports":     {"更新端口", []string{"spec.template.spec.containers.*.ports", "spec.template.spec.initContainers.*.ports"}},
		"resources": {"资源配置（limits/requests）", []string{"spec.template.spec.containers.*.resources", "spec.template.spec.initContainers.*.resources"}},
		"env":       {"环境变量增删改", []string{"spec.template.spec.containers.*.env", "spec.template.spec.initContainers.*.env"}},
		"probes": {"健康检查（存活/就绪/启动探针）", []string{
			"spec.template.spec.containers.*.livenessProbe", "spec.template.spec.containers.*.readinessProbe", "spec.template.spec.containers.*.startupProbe",
			"spec.template.spec.initContainers.*.livenessProbe", "spec.template.spec.initContainers.*.readinessProbe", "spec.template.spec.initContainers.*.startupProbe"}},
		"storage": {"存储挂载（卷/持久化）", []string{"spec.template.spec.containers.*.volumeMounts", "spec.template.spec.initContainers.*.volumeMounts", "spec.template.spec.volumes"}},
		"scheduling": {"调度策略（节点选择/亲和）", []string{
			"spec.template.spec.nodeSelector", "spec.template.spec.nodeName", "spec.template.spec.affinity"}},
		"restart": {"重启", nil},
	},
	"jobs": {
		"image":     {"更新镜像", []string{"spec.template.spec.containers.*.image", "spec.template.spec.initContainers.*.image"}},
		"command":   {"入口/命令 (Entrypoint/CMD)", []string{"spec.template.spec.containers.*.command", "spec.template.spec.containers.*.args", "spec.template.spec.initContainers.*.command", "spec.template.spec.initContainers.*.args"}},
		"ports":     {"更新端口", []string{"spec.template.spec.containers.*.ports", "spec.template.spec.initContainers.*.ports"}},
		"resources": {"资源配置（limits/requests）", []string{"spec.template.spec.containers.*.resources", "spec.template.spec.initContainers.*.resources"}},
		"env":       {"环境变量增删改", []string{"spec.template.spec.containers.*.env", "spec.template.spec.initContainers.*.env"}},
		"probes": {"健康检查（存活/就绪/启动探针）", []string{
			"spec.template.spec.containers.*.livenessProbe", "spec.template.spec.containers.*.readinessProbe", "spec.template.spec.containers.*.startupProbe",
			"spec.template.spec.initContainers.*.livenessProbe", "spec.template.spec.initContainers.*.readinessProbe", "spec.template.spec.initContainers.*.startupProbe"}},
		"storage": {"存储挂载（卷/持久化）", []string{"spec.template.spec.containers.*.volumeMounts", "spec.template.spec.initContainers.*.volumeMounts", "spec.template.spec.volumes"}},
		"scheduling": {"调度策略（节点选择/亲和）", []string{
			"spec.template.spec.nodeSelector", "spec.template.spec.nodeName", "spec.template.spec.affinity"}},
	},
	"cronjobs": {
		"image":     {"更新镜像", []string{"spec.jobTemplate.spec.template.spec.containers.*.image", "spec.jobTemplate.spec.template.spec.initContainers.*.image"}},
		"command":   {"入口/命令 (Entrypoint/CMD)", []string{"spec.jobTemplate.spec.template.spec.containers.*.command", "spec.jobTemplate.spec.template.spec.containers.*.args", "spec.jobTemplate.spec.template.spec.initContainers.*.command", "spec.jobTemplate.spec.template.spec.initContainers.*.args"}},
		"ports":     {"更新端口", []string{"spec.jobTemplate.spec.template.spec.containers.*.ports", "spec.jobTemplate.spec.template.spec.initContainers.*.ports"}},
		"resources": {"资源配置（limits/requests）", []string{"spec.jobTemplate.spec.template.spec.containers.*.resources", "spec.jobTemplate.spec.template.spec.initContainers.*.resources"}},
		"env":       {"环境变量增删改", []string{"spec.jobTemplate.spec.template.spec.containers.*.env", "spec.jobTemplate.spec.template.spec.initContainers.*.env"}},
		"probes": {"健康检查（存活/就绪/启动探针）", []string{
			"spec.jobTemplate.spec.template.spec.containers.*.livenessProbe", "spec.jobTemplate.spec.template.spec.containers.*.readinessProbe", "spec.jobTemplate.spec.template.spec.containers.*.startupProbe",
			"spec.jobTemplate.spec.template.spec.initContainers.*.livenessProbe", "spec.jobTemplate.spec.template.spec.initContainers.*.readinessProbe", "spec.jobTemplate.spec.template.spec.initContainers.*.startupProbe"}},
		"storage": {"存储挂载（卷/持久化）", []string{"spec.jobTemplate.spec.template.spec.containers.*.volumeMounts", "spec.jobTemplate.spec.template.spec.initContainers.*.volumeMounts", "spec.jobTemplate.spec.template.spec.volumes"}},
		"scheduling": {"调度策略（节点选择/亲和）", []string{
			"spec.jobTemplate.spec.template.spec.nodeSelector", "spec.jobTemplate.spec.template.spec.nodeName", "spec.jobTemplate.spec.template.spec.affinity"}},
	},
	"pods": {
		"image":     {"更新镜像", []string{"spec.containers.*.image", "spec.initContainers.*.image"}},
		"command":   {"入口/命令 (Entrypoint/CMD)", []string{"spec.containers.*.command", "spec.containers.*.args", "spec.initContainers.*.command", "spec.initContainers.*.args"}},
		"ports":     {"更新端口", []string{"spec.containers.*.ports", "spec.initContainers.*.ports"}},
		"resources": {"资源配置（limits/requests）", []string{"spec.containers.*.resources", "spec.initContainers.*.resources"}},
		"env":       {"环境变量增删改", []string{"spec.containers.*.env", "spec.initContainers.*.env"}},
	},
	"configmaps": {"data": {"配置数据", []string{"data"}}},
	"secrets":    {"data": {"数据", []string{"data", "stringData"}}},
	"services": {
		"ports":    {"更新端口", []string{"spec.ports"}},
		"selector": {"选择器", []string{"spec.selector"}},
	},
	"ingresses": {
		"routes": {"路由规则（host/路径/后端服务）", []string{"spec.rules"}},
	},
	"persistentvolumeclaims": {"storage": {"容量", []string{"spec.resources.requests.storage"}}},
}

// builtinPresets 内置角色快捷预设（与前端 applyPreset 非 CR 分支一致）。
// dev 动作池覆盖全部内置资源的细粒度动作（image/storage/data/routes/selector/replicas/resources 等），
// 应用时按规则所选资源裁剪（TrimPresetForResources / 前端 trimPreset），非对应资源的令牌自然剔除
var builtinPresets = map[string]RolePreset{
	"viewer": {Verbs: []string{"get", "list", "logs"}, Actions: []string{"*"}},
	"dev":    {Verbs: []string{"get", "list", "watch", "update", "delete", "exec", "logs", "download", "upload"}, Actions: []string{"image", "command", "ports", "resources", "env", "probes", "storage", "scheduling", "data", "routes", "selector", "replicas", "restart", "stop", "start"}},
	"admin":  {Verbs: []string{"*"}, Actions: []string{"*"}},
}

// builtinResourcePresets 推荐的资源级 dev 预设：Pod 允许删除，控制器类资源不默认删除。
// 仅在 resource_preset_mode=scoped 时参与生效，legacy 模式保持旧全局行为。
var builtinResourcePresets = map[string]map[string]*RolePreset{
	"pods": {
		"dev": {Verbs: []string{"get", "list", "watch", "update", "delete", "exec", "logs", "download", "upload"},
			Actions: []string{"image", "command", "ports", "resources", "env"}},
	},
	"deployments": {
		"dev": {Verbs: []string{"get", "list", "watch", "update"},
			Actions: []string{"image", "command", "ports", "resources", "env", "probes", "storage", "scheduling", "replicas", "restart", "stop", "start"}},
	},
	"statefulsets": {
		"dev": {Verbs: []string{"get", "list", "watch", "update"},
			Actions: []string{"image", "command", "ports", "resources", "env", "probes", "storage", "scheduling", "replicas", "restart", "stop", "start"}},
	},
	"daemonsets": {
		"dev": {Verbs: []string{"get", "list", "watch", "update"},
			Actions: []string{"image", "command", "ports", "resources", "env", "probes", "storage", "scheduling", "restart"}},
	},
	"jobs": {
		"dev": {Verbs: []string{"get", "list", "watch", "update"},
			Actions: []string{"image", "command", "ports", "resources", "env", "probes", "storage", "scheduling"}},
	},
	"cronjobs": {
		"dev": {Verbs: []string{"get", "list", "watch", "update"},
			Actions: []string{"image", "command", "ports", "resources", "env", "probes", "storage", "scheduling"}},
	},
}

// TrimPresetForResources 角色预设按授权的资源范围裁剪（应用时/预设对齐/工单通道共用）：
// 动词去掉 Pod 专属项（exec/logs/download/upload 仅范围含 pods 时保留）；
// 动作与范围内各资源动作目录键求交（*. / 字段路径 / * 保留）。
// 范围含 * 或空 = 不限资源，原样返回；裁剪结果为空时原样返回（防御：不产出空授权）。
func TrimPresetForResources(p RolePreset, resource string, catalog map[string]map[string]ActionMeta) RolePreset {
	s := strings.TrimSpace(resource)
	if s == "" || s == "*" {
		return p
	}
	hasPods, hasCR := false, false
	resKeys := map[string]bool{}
	for _, r := range strings.Split(s, ",") {
		r = strings.TrimSpace(r)
		if r == "" || r == "*" {
			return p
		}
		if r == "pods" {
			hasPods = true
		}
		if strings.Contains(r, "/") {
			hasCR = true
			continue
		}
		resKeys[r] = true
	}
	// 动词裁剪：* 语义保持；Pod 专属动词仅在含 pods 时有意义；CR 范围补增删改动词
	if !slices.Contains(p.Verbs, "*") {
		var verbs []string
		for _, v := range p.Verbs {
			if podOnlyVerb(v) && !hasPods {
				continue
			}
			verbs = append(verbs, v)
		}
		if hasCR {
			for _, v := range []string{"create", "update", "delete"} {
				if !slices.Contains(verbs, v) {
					verbs = append(verbs, v)
				}
			}
		}
		if len(verbs) > 0 {
			p.Verbs = verbs
		}
	}
	// 动作裁剪：与所选资源目录键求交（CR 无目录条目，仅保留字段路径/动词型令牌）。
	// 所选资源均不在动作目录（endpoints/storageclasses/nodes/namespaces 等）时，
	// 沿用 CR 同一约定：动词含 update → update 令牌 = 完整更新（否则动词裁剪后 update 是死的）
	if !slices.Contains(p.Actions, "*") {
		valid := map[string]bool{}
		hasKnown := false
		for r := range resKeys {
			if len(catalog[r]) > 0 {
				hasKnown = true
				for k := range catalog[r] {
					valid[k] = true
				}
			}
		}
		var actions []string
		for _, a := range p.Actions {
			if strings.Contains(a, ".") || valid[a] {
				actions = append(actions, a)
			}
		}
		if len(actions) == 0 && !hasKnown && slices.Contains(p.Verbs, "update") {
			actions = []string{"update"}
		}
		if len(actions) > 0 {
			p.Actions = actions
		}
	}
	return p
}

// EffectiveRolePreset 返回角色在指定资源范围上的预设。
// scoped 模式下单资源使用资源级覆盖；多资源取交集，避免 pods+deployments 合成规则时
// 把 Pod 的 delete/exec 权限扩散到 Deployment。
func EffectiveRolePreset(pp PermPresets, kind, resource string) RolePreset {
	global := pp.Presets[kind]
	if pp.ResourcePresetMode != "scoped" {
		return global
	}
	s := strings.TrimSpace(resource)
	if s == "" || s == "*" || strings.Contains(s, "/") {
		return global
	}
	selected := make([]RolePreset, 0)
	for _, r := range strings.Split(s, ",") {
		r = strings.TrimSpace(r)
		if r == "" || r == "*" {
			return global
		}
		p := global
		if pp.ResourcePresets[r] != nil && pp.ResourcePresets[r][kind] != nil {
			p = *pp.ResourcePresets[r][kind]
		}
		selected = append(selected, p)
	}
	if len(selected) == 0 {
		return global
	}
	restVerbs := make([][]string, 0, len(selected)-1)
	restActions := make([][]string, 0, len(selected)-1)
	for _, p := range selected[1:] {
		restVerbs = append(restVerbs, p.Verbs)
		restActions = append(restActions, p.Actions)
	}
	return RolePreset{
		Verbs:   intersectTokens(selected[0].Verbs, restVerbs...),
		Actions: intersectTokens(selected[0].Actions, restActions...),
	}
}

func intersectTokens(first []string, rest ...[]string) []string {
	if len(rest) == 0 {
		return first
	}
	out := make([]string, 0, len(first))
	for _, token := range first {
		ok := true
		for _, other := range rest {
			if !slices.Contains(other, token) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, token)
		}
	}
	return out
}

// podOnlyVerb Pod 专属动词（资源选择联动同款口径）
func podOnlyVerb(v string) bool {
	return v == "exec" || v == "logs" || v == "download" || v == "upload"
}

// ---------- 预设加载与缓存 ----------

var (
	presetMu    sync.RWMutex
	presetCache *PermPresets // 合并后的生效配置（nil = 未加载）
)

const permPresetsKey = "perm_presets"

// storedPermPresets 读 Setting 里的自定义预设；无配置返回零值
func storedPermPresets(db *gorm.DB) PermPresets {
	var pp PermPresets
	var s model.Setting
	if err := db.Where("`key` = ?", permPresetsKey).First(&s).Error; err != nil {
		return pp
	}
	_ = json.Unmarshal([]byte(s.Value), &pp)
	return pp
}

// EffectivePermPresets 返回合并后的生效配置（内置默认 ⊕ 自定义整体替换）；
// 结果每次重新计算（仅设置页读取，频率低），鉴权路径走 actionCatalog 缓存
func EffectivePermPresets(db *gorm.DB) PermPresets {
	out := PermPresets{
		Actions:         map[string]map[string]ActionMeta{},
		Presets:         map[string]RolePreset{},
		ResourcePresets: map[string]map[string]*RolePreset{},
	}
	for r, m := range builtinActions {
		out.Actions[r] = m
	}
	for k, p := range builtinPresets {
		out.Presets[k] = p
	}
	stored := storedPermPresets(db)
	for r, m := range stored.Actions {
		if len(m) == 0 {
			delete(out.Actions, r) // 存空表 = 清空该资源目录（该资源按 CR 语义处理）
			continue
		}
		out.Actions[r] = m
	}
	for k, p := range stored.Presets {
		out.Presets[k] = p
	}
	out.ResourcePresetMode = "legacy"
	if stored.ResourcePresetMode == "scoped" {
		out.ResourcePresetMode = "scoped"
		for r, roles := range builtinResourcePresets {
			out.ResourcePresets[r] = map[string]*RolePreset{}
			for k, p := range roles {
				copy := *p
				out.ResourcePresets[r][k] = &copy
			}
		}
	}
	for r, roles := range stored.ResourcePresets {
		if out.ResourcePresets[r] == nil {
			out.ResourcePresets[r] = map[string]*RolePreset{}
		}
		for k, p := range roles {
			if p == nil {
				delete(out.ResourcePresets[r], k)
				continue
			}
			copy := *p
			out.ResourcePresets[r][k] = &copy
		}
		if len(out.ResourcePresets[r]) == 0 {
			delete(out.ResourcePresets, r)
		}
	}
	return out
}

// actionCatalog 鉴权用合并目录（进程内缓存；PUT 预设后 InvalidatePermPresets 失效）
func actionCatalog(db *gorm.DB) map[string]map[string]ActionMeta {
	presetMu.RLock()
	if presetCache != nil {
		c := presetCache.Actions
		presetMu.RUnlock()
		return c
	}
	presetMu.RUnlock()
	eff := EffectivePermPresets(db)
	presetMu.Lock()
	presetCache = &eff
	presetMu.Unlock()
	return eff.Actions
}

// InvalidatePermPresets 预设更新后失效缓存（下次鉴权重新加载）
func InvalidatePermPresets() {
	presetMu.Lock()
	presetCache = nil
	presetMu.Unlock()
}

// ActionLabel 查 (resource, token) 的展示名——与鉴权同一份目录（含管理员自定义），
// 供 403 文案引用授权界面显示的动作名；未配置回退 token 原名
func ActionLabel(db *gorm.DB, resource, token string) string {
	if m, ok := actionCatalog(db)[resource]; ok {
		if meta, ok := m[token]; ok && meta.Label != "" {
			return meta.Label
		}
	}
	return token
}

// BuiltinRolePreset 内置角色预设（供同步对齐时识别「旧形态」授权——预设动词被改过的情况）
func BuiltinRolePreset(kind string) (RolePreset, bool) {
	p, ok := builtinPresets[kind]
	return p, ok
}

// BuiltinActionCatalog 设置接口校验资源级预设时读取内置目录，再叠加同请求里的自定义目录
func BuiltinActionCatalog() map[string]map[string]ActionMeta {
	return builtinActions
}

// diffIgnore 这些顶层子树不参与字段鉴权：status 由控制器维护。
// metadata 不整体忽略（外部评审核实：整体忽略让字段限定授权可顺带改 finalizers 等
// 元数据，绕过清理逻辑），改为逐字段忽略服务端管理/无害字段——finalizers、
// ownerReferences、labels、annotations 等真实改动会被差异路径捕获并拒绝
var diffIgnore = map[string]bool{
	"status": true,
}

// serverManagedMetaFields metadata 下比较时忽略的字段。两类：apiserver/控制器维护的
// 服务端字段（uid/resourceVersion 等，精简 YAML apply 缺它们不算差异）；以及
// labels/annotations/name/namespace/generateName——标签注解按平台设计走 update 动词
// 通道（metaFormSections），不纳入字段级管控。不在表内的 metadata 子键
// （finalizers、ownerReferences 等）参与鉴权：字段限定授权不再能顺带改动它们
var serverManagedMetaFields = map[string]bool{
	"uid":                        true,
	"resourceVersion":            true,
	"generation":                 true,
	"creationTimestamp":          true,
	"deletionTimestamp":          true,
	"deletionGracePeriodSeconds": true,
	"managedFields":              true,
	"selfLink":                   true,
	"labels":                     true,
	"annotations":                true,
	"name":                       true,
	"namespace":                  true,
	"generateName":               true,
}

// AllowedUpdateFields 返回某用户对某资源 update 时允许的字段模式；all=true 表示不限
func AllowedUpdateFields(db *gorm.DB, role string, userID uint, clusterName, namespace, resource string) (patterns []string, all bool) {
	if role == model.RoleSuperAdmin {
		return nil, true
	}
	actions := updateActionsOf(db, role, userID, clusterName, namespace, resource)
	if actions == nil {
		return nil, true // 含 * 或无 update 授权（后者已被 verb 检查拦截）
	}
	catalog := actionCatalog(db)
	meta := catalog[resource]
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			patterns = append(patterns, p)
		}
	}
	for _, a := range actions {
		if m, ok := meta[a]; ok {
			for _, p := range m.Fields {
				add(strings.TrimSpace(p))
			}
		}
		if strings.Contains(a, ".") { // 原始字段路径
			add(a)
		}
	}
	return patterns, false
}

// ActionAllowed 端点级细粒度动作检查（如 scale→replicas、restart→restart）
func ActionAllowed(db *gorm.DB, role string, userID uint, clusterName, namespace, resource, action string) bool {
	if role == model.RoleSuperAdmin {
		return true
	}
	actions := updateActionsOf(db, role, userID, clusterName, namespace, resource)
	if actions == nil {
		return true
	}
	for _, a := range actions {
		if a == action {
			return true
		}
	}
	return false
}

// updateActionsOf 汇总命中的 update 授权（含权限组）中的动作集；含 * 返回 nil；
// CR（无预设）时显式勾选 "update" 令牌 = 完整更新返回 nil；字段路径（含 .）保留由
// AllowedUpdateFields 匹配限定——此前任意非空 actions 都返回 nil，导致字段路径限定完全失效
func updateActionsOf(db *gorm.DB, role string, userID uint, clusterName, namespace, resource string) []string {
	legacy := legacyUpdateActionsOf(db, role, userID, clusterName, namespace, resource)
	detail := fmt.Sprintf("user=%d cluster=%s namespace=%q resource=%s", userID, clusterName, namespace, resource)
	return authz.CompareActions("update-actions", detail, legacy, func() ([]string, error) {
		catalog := actionCatalog(db)
		return authz.UpdateActions(db, userID, clusterName, namespace, resource, catalog[resource] != nil)
	})
}

func legacyUpdateActionsOf(db *gorm.DB, role string, userID uint, clusterName, namespace, resource string) []string {
	var cl model.Cluster
	if err := db.Where("name = ?", clusterName).First(&cl).Error; err != nil {
		return []string{}
	}
	out := []string{}
	catalog := actionCatalog(db)
	presetKnown := catalog[resource] != nil // CR 与目录外资源通常不在动作目录
	for _, g := range EffectiveGrants(db, userID) {
		if g.ClusterID != cl.ID {
			continue
		}
		if !entryMatch(g.Namespace, namespace) || !entryMatch(g.Resource, resource) {
			continue
		}
		if !entryMatch(g.Verb, "update") {
			continue
		}
		for _, a := range strings.Split(g.Actions, ",") {
			a = strings.TrimSpace(a)
			if a == "" {
				continue
			}
			if a == "*" {
				return nil
			}
			out = append(out, a)
		}
	}
	// CR 资源：勾选 "update" 动作令牌 = 授权完整更新；含 . 的字段路径令牌保留（限定可修改字段）；
	// 其余令牌（create/delete 属动词、image 等属内置预设键）对 CR 无对应字段 → 不产生可修改字段（最严格）
	if !presetKnown {
		for _, a := range out {
			if a == "update" {
				return nil
			}
		}
	}
	return out
}

// FieldViolations 计算新旧对象差异路径，返回不在授权模式内的越权路径
func FieldViolations(oldObj, newObj map[string]any, allowed []string) []string {
	// 先做类型归一化：apiserver 对象的数字是 int64，JSON 往返后是 float64，
	// 不归一化会把未改动的数字字段误判为差异
	o, n := normNode(oldObj), normNode(newObj)
	om, _ := o.(map[string]any)
	nm, _ := n.(map[string]any)
	var paths []string
	diffTree(om, nm, "", &paths)
	var bad []string
	for _, p := range paths {
		if !pathAllowed(p, allowed) {
			bad = append(bad, p)
		}
	}
	return bad
}

// normNode 数字统一为 float64，容器/切片重建后比较
func normNode(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, vv := range t {
			m[k] = normNode(vv)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, vv := range t {
			s[i] = normNode(vv)
		}
		return s
	case int64:
		return float64(t)
	case int32:
		return float64(t)
	case int:
		return float64(t)
	default:
		return v
	}
}

// pathAllowed 精确匹配或按路径段前缀匹配
func pathAllowed(path string, allowed []string) bool {
	for _, a := range allowed {
		if a == "*" || a == path || strings.HasPrefix(path, a+".") {
			return true
		}
	}
	return false
}

// diffTree 递归收集差异路径；数组下标归一化为 *（第 0 个容器的 image 与第 3 个的视为同一路径）
func diffTree(oldN, newN any, path string, out *[]string) {
	if reflect.DeepEqual(oldN, newN) {
		return
	}
	om, ook := oldN.(map[string]any)
	nm, nok := newN.(map[string]any)
	if ook && nok {
		keys := map[string]bool{}
		for k := range om {
			keys[k] = true
		}
		for k := range nm {
			keys[k] = true
		}
		for k := range keys {
			child := k
			if path != "" {
				child = path + "." + k
			}
			if diffIgnore[child] {
				continue
			}
			// metadata 下的服务端管理字段忽略（uid/resourceVersion 等）；其余（finalizers/
			// ownerReferences/labels/annotations…）正常参与 diff 供字段授权判定
			if path == "metadata" && serverManagedMetaFields[k] {
				continue
			}
			diffTree(om[k], nm[k], child, out)
		}
		return
	}
	os, ook := oldN.([]any)
	ns, nok := newN.([]any)
	if ook && nok {
		if len(os) != len(ns) {
			*out = append(*out, path)
			return
		}
		for i := range os {
			diffTree(os[i], ns[i], path+".*", out)
		}
		return
	}
	// 叶子节点且不等
	if path != "" {
		*out = append(*out, path)
	}
}
