package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"kubeyard/internal/audit"
	"kubeyard/internal/cluster"
	"kubeyard/internal/middleware"
	"kubeyard/internal/model"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextcs "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"
)

// target 资源定位（单资源操作统一用 query 参数）
type target struct {
	Group     string `form:"group"`
	Version   string `form:"version" binding:"required"`
	Resource  string `form:"resource" binding:"required"`
	Namespace string `form:"namespace"`
	Name      string `form:"name" binding:"required"`
}

func (h *H) parseTarget(c *gin.Context, e *cluster.Entry) (*target, schema.GroupVersionResource, bool) {
	var t target
	if err := c.ShouldBindQuery(&t); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误: " + err.Error()})
		return nil, schema.GroupVersionResource{}, false
	}
	// discovery 校正版本：前端写死的新版本（如 batch/v1 cronjobs）在老集群自动落到实际服务的版本
	return &t, e.PreferredGVR(t.Group, t.Version, t.Resource), true
}

// clusterScopedResources 已知集群级资源（dynamic 无法直接判 scope，用白名单兜底——CR 走 mapper 实时判断）
var clusterScopedResources = map[string]bool{
	"nodes": true, "namespaces": true, "clusterroles": true, "clusterrolebindings": true,
	"customresourcedefinitions": true, "persistentvolumes": true, "storageclasses": true,
	"clusterissuers": true, "ingressclasses": true, "priorityclasses": true,
	"volumeattachments": true, "apiservices": true, "mutatingwebhookconfigurations": true,
	"validatingwebhookconfigurations": true, "csidrivers": true, "csinodes": true,
	"runtimeclasses": true, "podsecuritypolicies": true, "remoteclusters": true,
}

// resourceScope 实时判断 GVR 的 scope：白名单兜底内置资源 + RESTMapper 查 CR；查不到时默认 namespaced
func resourceScope(e *cluster.Entry, gvr schema.GroupVersionResource) bool {
	if clusterScopedResources[gvr.Resource] {
		return false
	}
	m, err := e.Mapper().RESTMapping(schema.GroupKind{Group: gvr.Group, Kind: kindFromResource(gvr.Resource)}, gvr.Version)
	if err != nil {
		// 兜底：Mapper 查不到（CRD 未就绪/不识别）→ 当 namespaced，绝大多数 K8s 资源如此；
		// dynamic client 自身的 not found 会兜底集群级 CR 误判的情况
		return true
	}
	return m.Scope.Name() == meta.RESTScopeNameNamespace
}

// kindFromResource 简单 plural → Kind 转换（-ies/-es 去后缀，首字母大写）；mapper 失败时的兜底可忽略
func kindFromResource(resource string) string {
	// 简化版：traefik-ingressroute → TraefikIngressroute；cert-manager.io/certificates 的 plural 是 certificates → Certificate
	// 不强求精确，RESTMapping 失败后兜底走 namespaced
	if len(resource) == 0 {
		return resource
	}
	s := resource
	switch {
	case strings.HasSuffix(s, "ies") && len(s) > 3:
		s = s[:len(s)-3] + "y"
	case strings.HasSuffix(s, "es") && len(s) > 2:
		s = s[:len(s)-2]
	case strings.HasSuffix(s, "s") && len(s) > 1:
		s = s[:len(s)-1]
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// isClusterScoped 保留给 ListResources 旧代码使用（实际已统一走 resourceScope）
func isClusterScoped(gvr schema.GroupVersionResource) bool {
	return clusterScopedResources[gvr.Resource]
}

func dynamicRes(e *cluster.Entry, gvr schema.GroupVersionResource, ns string) dynamic.ResourceInterface {
	if resourceScope(e, gvr) {
		return e.Dynamic.Resource(gvr).Namespace(ns)
	}
	return e.Dynamic.Resource(gvr)
}

// builtInGroups 内置 K8s API 组的白名单（与 ActionPresets/CRs 区分）
// 鉴权时只在「非内置 group」才把资源视为 CR（用 group/plural 全限定名）
var builtInGroups = map[string]bool{
	"":                             true, // core/v1
	"apps":                         true,
	"batch":                        true,
	"autoscaling":                  true,
	"networking.k8s.io":            true,
	"rbac.authorization.k8s.io":    true,
	"storage.k8s.io":               true,
	"policy":                       true,
	"events.k8s.io":                true,
	"coordination.k8s.io":          true,
	"node.k8s.io":                  true,
	"discovery.k8s.io":             true,
	"flowcontrol.apiserver.k8s.io": true,
	"certificates.k8s.io":          true,
	"apiextensions.k8s.io":         true,
	"admissionregistration.k8s.io": true,
	"scheduling.k8s.io":            true,
}

// resKeyForAuth 鉴权用资源 key：内置资源用 plural；CR（group 不在内置白名单）用 group/plural
func resKeyForAuth(group, resource string) string {
	if builtInGroups[group] {
		return resource
	}
	return group + "/" + resource
}

// 权限拒绝文案的友好别名（与前端 grant.ts / 集成接口同一套口径，缺省回退原名）
var resDisplayAliases = map[string]string{
	"deployments":              "应用 (deployments)",
	"pods":                     "运行实例 (pods)",
	"services":                 "服务 (services)",
	"statefulsets":             "有状态应用 (statefulsets)",
	"daemonsets":               "守护进程集 (daemonsets)",
	"jobs":                     "任务 (jobs)",
	"cronjobs":                 "定时任务 (cronjobs)",
	"horizontalpodautoscalers": "自动扩缩容 (HPA)",
	"poddisruptionbudgets":     "中断预算 (PDB)",
	"ingresses":                "路由 (ingresses)",
	"endpoints":                "端点 (endpoints)",
	"networkpolicies":          "网络策略 (networkpolicies)",
	"endpointslices":           "端点切片 (endpointslices)",
	"ingressclasses":           "Ingress 类 (ingressclasses)",
	"configmaps":               "配置 (configmaps)",
	"secrets":                  "密钥 (secrets)",
	"persistentvolumeclaims":   "存储声明 (persistentvolumeclaims)",
	"resourcequotas":           "资源配额 (resourcequotas)",
	"limitranges":              "限制范围 (limitranges)",
	"serviceaccounts":          "服务账号 (serviceaccounts)",
	"roles":                    "角色 (roles)",
	"rolebindings":             "角色绑定 (rolebindings)",
	"clusterroles":             "集群角色 (clusterroles)",
	"clusterrolebindings":      "集群角色绑定 (clusterrolebindings)",
	"storageclasses":           "存储类 (storageclasses)",
	"nodes":                    "节点 (nodes)",
	"namespaces":               "命名空间 (namespaces)",
}

// resDisplayLabel 权限拒绝文案里的资源友好名（与前端 grant.ts / 集成接口同一套口径，缺省回退原名）
func resDisplayLabel(resKey string) string {
	if label, ok := resDisplayAliases[resKey]; ok {
		return label
	}
	return resKey
}

// denyResource403 输出 403：文案带具体资源（如「应用 (deployments)」），方便用户/管理员定位缺哪条授权
func denyResource403(c *gin.Context, verb, resKey string) {
	c.JSON(http.StatusForbidden, gin.H{"message": "无 " + resDisplayLabel(resKey) + " 的 " + verb + " 权限"})
}

// ---------- 命名空间 ----------

// ListCRs GET /api/v1/clusters/:cluster/crs — 枚举集群可用 CRD（按 (group, plural) 合并多版本）
func (h *H) ListCRs(c *gin.Context) {
	clusterName := c.Param("cluster")
	e, err := h.mgr.Get(clusterName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	// 列 CRD（独立 clientset，kubernetes.Clientset 不含 apiextensions）
	ext, err := apiextcs.NewForConfig(e.RestConfig)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": "构造 apiextensions 客户端失败: " + err.Error()})
		return
	}
	crdList, err := ext.ApiextensionsV1().CustomResourceDefinitions().List(ctx, metav1.ListOptions{})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": "列 CRD 失败: " + err.Error()})
		return
	}
	declared := map[schema.GroupKind]*apiextv1.CustomResourceDefinition{}
	for i := range crdList.Items {
		crd := &crdList.Items[i]
		hasServed := false
		for _, v := range crd.Spec.Versions {
			if v.Served {
				hasServed = true
				break
			}
		}
		if !hasServed {
			continue
		}
		gk := schema.GroupKind{Group: crd.Spec.Group, Kind: crd.Spec.Names.Kind}
		declared[gk] = crd
	}
	// 走 discovery 拿真实 GVR 与 shortNames（v1 ServerGroupsAndResources 返回的 APIResource 不带 shortNames；
	// 用 ServerPreferredResources 同样的迭代，逐个 GVR RESTMapping + crd.Spec.Names 拿元信息补齐）
	lists, _ := e.Disco.ServerPreferredResources()
	type key struct{ group, plural string }
	seen := map[key]map[string]any{}
	for _, l := range lists {
		gv, perr := schema.ParseGroupVersion(l.GroupVersion)
		if perr != nil {
			continue
		}
		for _, r := range l.APIResources {
			if strings.Contains(r.Name, "/") {
				continue
			}
			gk := schema.GroupKind{Group: gv.Group, Kind: r.Kind}
			crd, ok := declared[gk]
			if !ok {
				continue
			}
			gvr := gv.WithResource(r.Name)
			namespaced := resourceScope(e, gvr)
			k := key{gv.Group, r.Name}
			if existing, ok := seen[k]; ok {
				if !containsStr(anyToStrSlice(existing["versions"]), gv.Version) {
					existing["versions"] = append(anyToStrSlice(existing["versions"]), gv.Version)
				}
				continue
			}
			seen[k] = map[string]any{
				"group":      gv.Group,
				"version":    gv.Version,
				"versions":   []string{gv.Version},
				"kind":       r.Kind,
				"plural":     r.Name,
				"singular":   crd.Spec.Names.Singular,
				"shortNames": crd.Spec.Names.ShortNames,
				"namespaced": namespaced,
			}
		}
	}
	out := make([]map[string]any, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	// 按授权过滤：SuperAdmin 全量；其他用户仅可见有 list 授权的 CR（未授权的 CRD 不暴露）
	if middleware.Role(c) != model.RoleSuperAdmin {
		var cl model.Cluster
		if err := h.db.Where("name = ?", clusterName).First(&cl).Error; err != nil {
			c.JSON(http.StatusOK, gin.H{"items": []any{}})
			return
		}
		grants := middleware.EffectiveGrants(h.db, middleware.UID(c))
		filtered := out[:0]
		for _, v := range out {
			g, _ := v["group"].(string)
			p, _ := v["plural"].(string)
			if middleware.HasListGrantInCluster(grants, cl.ID, g+"/"+p) {
				filtered = append(filtered, v)
			}
		}
		out = filtered
	}
	sort.Slice(out, func(i, j int) bool {
		gi, _ := out[i]["group"].(string)
		gj, _ := out[j]["group"].(string)
		if gi != gj {
			return gi < gj
		}
		ki, _ := out[i]["kind"].(string)
		kj, _ := out[j]["kind"].(string)
		return ki < kj
	})
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func anyToStrSlice(v any) []string {
	if v == nil {
		return nil
	}
	if s, ok := v.([]string); ok {
		return s
	}
	if a, ok := v.([]any); ok {
		out := make([]string, 0, len(a))
		for _, x := range a {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// ListNamespaces GET /api/v1/clusters/:cluster/namespaces — 按授权过滤
func (h *H) ListNamespaces(c *gin.Context) {
	clusterName := c.Param("cluster")
	e, err := h.mgr.Get(clusterName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	nsList, err := e.Client.CoreV1().Namespaces().List(c.Request.Context(), metav1.ListOptions{})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": "集群返回错误: " + err.Error()})
		return
	}
	visible, all := middleware.VisibleNamespaces(h.db, middleware.Role(c), middleware.UID(c), clusterName)
	aliases := nsAliasMap(h.db, clusterName)
	out := []map[string]any{}
	for _, ns := range nsList.Items {
		if !all && !containsStr(visible, ns.Name) {
			continue
		}
		phase := string(ns.Status.Phase)
		if phase == "" {
			phase = "Unknown"
		}
		out = append(out, map[string]any{
			"name":               ns.Name,
			"status":             phase,
			"alias":              aliases[ns.Name],
			"creation_timestamp": ns.CreationTimestamp.Time,
		})
	}
	c.JSON(http.StatusOK, out)
}

// ---------- 列表 ----------

// ListResources GET /api/v1/clusters/:cluster/resources
func (h *H) ListResources(c *gin.Context) {
	clusterName := c.Param("cluster")
	e, err := h.mgr.Get(clusterName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	var q struct {
		Group     string `form:"group"`
		Version   string `form:"version" binding:"required"`
		Resource  string `form:"resource" binding:"required"`
		Namespace string `form:"namespace"`
		Search    string `form:"search"`
		// 标签选择器（应用详情「运行实例」等场景）：下推到 K8s 服务端过滤。
		// 此前前端全量拉回再筛，命名空间 Pod 超过单页上限 500 时目标应用的 Pod 可能
		// 一页都进不来（表现为运行实例列表为空）；带选择器时也跟随 Continue 翻完
		LabelSelector string `form:"label_selector"`
	}
	if err := c.ShouldBindQuery(&q); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	// discovery 校正版本（老集群 cronjobs 等自动落到实际服务的 group/version）
	gvr := e.PreferredGVR(q.Group, q.Version, q.Resource)

	// 鉴权用的资源 key：CR 用 group/plural 全限定名（与前端授权保存一致），内置资源用 plural
	allowedResKey := resKeyForAuth(gvr.Group, gvr.Resource)

	// 资源级 list 鉴权：先确定「哪些命名空间内可 list 该资源」——
	// 无任何 list 授权直接 403（此前 cluster-scoped / ns=* 聚合路径缺此检查，可越权列举任意资源）
	listNS, listAll, listOK := middleware.ListableNamespaces(h.db, middleware.Role(c), middleware.UID(c), clusterName, allowedResKey)
	if !listOK {
		denyResource403(c, "list", allowedResKey)
		return
	}

	var namespaces []string // 待列举的 namespace；空串 = 集群级或全命名空间
	namespaced := resourceScope(e, gvr)
	switch {
	case !namespaced:
		// 集群级资源（nodes/namespaces/storageclasses/集群级 CR 等）：仅 ns=*
		// 的 list 授权可全集群列举——具体命名空间的授权不放行（与 Allowed 对
		// 集群级资源的新口径一致，外部评审核实）
		if !listAll {
			denyResource403(c, "list", allowedResKey)
			return
		}
		namespaces = []string{""}
	case q.Namespace != "":
		if !listAll && !containsStr(listNS, q.Namespace) {
			c.JSON(http.StatusForbidden, gin.H{"message": "无该命名空间的访问权限"})
			return
		}
		namespaces = []string{q.Namespace}
	case listAll:
		namespaces = []string{""}
	default:
		namespaces = listNS
	}

	items := []map[string]any{}
	truncated := false // 任一命名空间超过单次拉取上限（500 条）即置位，前端展示 "共 N 项+"
	// 搜索时翻页拉全量再过滤：500 上限只用于默认浏览，否则排在其后的资源永远搜不到
	const listLimit = 500
	const maxSearchPages = 50 // 搜索全量翻页保险丝：50×500=2.5 万条/命名空间，防止异常大列表拖垮请求
	opts := metav1.ListOptions{Limit: listLimit, LabelSelector: q.LabelSelector}
	for _, ns := range namespaces {
		for page := 0; ; page++ {
			var list *unstructured.UnstructuredList
			var err error
			if ns != "" {
				list, err = e.Dynamic.Resource(gvr).Namespace(ns).List(c.Request.Context(), opts)
			} else {
				list, err = e.Dynamic.Resource(gvr).List(c.Request.Context(), opts)
			}
			if err != nil {
				if len(namespaces) == 1 {
					c.JSON(http.StatusBadGateway, gin.H{"message": "集群返回错误: " + err.Error()})
					return
				}
				break // 多 ns 聚合时单个失败跳过
			}
			// unstructured 列表没有 RemainingItemCount 字段，从 list metadata 里取
			remaining := false
			if n := getStr(list.Object, "metadata", "remainingItemCount"); n != "" {
				if v, err := strconv.ParseInt(n, 10, 64); err == nil && v > 0 {
					remaining = true
				}
			}
			for i := range list.Items {
				m := list.Items[i].Object
				stripManagedFields(m)
				if q.Search != "" {
					if !matchSearch(q.Resource, m, q.Search) {
						continue
					}
				}
				items = append(items, m)
			}
			// 默认浏览：单页即止（保持 500 上限）；搜索 / 标签选择器：调用方要的是完整
			// 结果集，跟随 Continue 翻完所有页（maxSearchPages 保险丝防异常大列表拖垮请求）
			followPages := q.Search != "" || q.LabelSelector != ""
			cont := list.GetContinue()
			if !followPages || cont == "" || page >= maxSearchPages {
				if remaining {
					truncated = true // 单页截断，或保险丝打断
				}
				break
			}
			opts.Continue = cont
		}
		opts.Continue = ""
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items), "truncated": truncated})
}

// ---------- 单资源 ----------

// GetResource GET /api/v1/clusters/:cluster/resource
func (h *H) GetResource(c *gin.Context) {
	clusterName := c.Param("cluster")
	e, err := h.mgr.Get(clusterName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	t, gvr, ok := h.parseTarget(c, e)
	if !ok {
		return
	}
	allowedResKey := resKeyForAuth(gvr.Group, gvr.Resource)
	if !middleware.Allowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, t.Namespace, "get", allowedResKey) {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "resource_get", clusterName, t.Namespace, t.Resource, t.Name, "权限拒绝", "denied"))
		denyResource403(c, "get", allowedResKey)
		return
	}
	obj, err := dynamicRes(e, gvr, t.Namespace).Get(c.Request.Context(), t.Name, metav1.GetOptions{})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": err.Error()})
		return
	}
	stripManagedFields(obj.Object)
	c.JSON(http.StatusOK, obj.Object)
}

// UpdateResource PUT /api/v1/clusters/:cluster/resource — YAML 编辑保存
func (h *H) UpdateResource(c *gin.Context) {
	clusterName := c.Param("cluster")
	// 限制 body 大小：YAML 编辑的合理上限（避免恶意大 payload DoS）
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4*1024*1024)
	e, err := h.mgr.Get(clusterName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	t, gvr, ok := h.parseTarget(c, e)
	if !ok {
		return
	}
	// Pod 是运行实例不支持编辑（表单/YAML 两个入口一致；此前仅表单拦截，YAML 可绕过）
	if gvr.Group == "" && gvr.Resource == "pods" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Pod 是运行实例，不支持编辑；请修改所属的应用（Deployment 等）"})
		return
	}
	allowedResKey := resKeyForAuth(gvr.Group, gvr.Resource)
	if !middleware.Allowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, t.Namespace, "update", allowedResKey) {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "resource_update", clusterName, t.Namespace, t.Resource, t.Name, "权限拒绝", "denied"))
		denyResource403(c, "update", allowedResKey)
		return
	}
	var body map[string]any
	if err := c.BindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "JSON 解析失败"})
		return
	}
	stripManagedFields(body)
	// 细粒度字段授权：与线上对象 diff，越权字段拒绝（CR 用 group/plural）
	if patterns, all := middleware.AllowedUpdateFields(h.db, middleware.Role(c), middleware.UID(c), clusterName, t.Namespace, allowedResKey); !all {
		old, gerr := dynamicRes(e, gvr, t.Namespace).Get(c.Request.Context(), t.Name, metav1.GetOptions{})
		if gerr != nil {
			c.JSON(http.StatusBadGateway, gin.H{"message": "读取当前对象失败: " + gerr.Error()})
			return
		}
		if denyFieldUpdate(c, clusterName, t.Namespace, allowedResKey, t.Name,
			middleware.FieldViolations(old.Object, body, patterns), patterns) {
			return
		}
	}
	obj := &unstructured.Unstructured{Object: body}
	updated, err := dynamicRes(e, gvr, t.Namespace).Update(c.Request.Context(), obj, metav1.UpdateOptions{})
	if err != nil {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "resource_update", clusterName, t.Namespace, t.Resource, t.Name, err.Error(), "failed"))
		c.JSON(http.StatusBadGateway, gin.H{"message": "保存失败: " + err.Error()})
		return
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "resource_update", clusterName, t.Namespace, t.Resource, t.Name, "更新资源", "success"))
	stripManagedFields(updated.Object)
	c.JSON(http.StatusOK, updated.Object)
}

// DeleteResource DELETE /api/v1/clusters/:cluster/resource
func (h *H) DeleteResource(c *gin.Context) {
	clusterName := c.Param("cluster")
	e, err := h.mgr.Get(clusterName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	t, gvr, ok := h.parseTarget(c, e)
	if !ok {
		return
	}
	allowedResKey := resKeyForAuth(gvr.Group, gvr.Resource)
	if !middleware.Allowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, t.Namespace, "delete", allowedResKey) {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "resource_delete", clusterName, t.Namespace, t.Resource, t.Name, "权限拒绝", "denied"))
		denyResource403(c, "delete", allowedResKey)
		return
	}
	if err := dynamicRes(e, gvr, t.Namespace).Delete(c.Request.Context(), t.Name, metav1.DeleteOptions{}); err != nil {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "resource_delete", clusterName, t.Namespace, t.Resource, t.Name, err.Error(), "failed"))
		c.JSON(http.StatusBadGateway, gin.H{"message": "删除失败: " + err.Error()})
		return
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "resource_delete", clusterName, t.Namespace, t.Resource, t.Name, "删除资源", "success"))
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// ---------- YAML 部署 ----------

var docSplitRe = regexp.MustCompile(`(?m)^---\s*$`)

type applyResultItem struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Action    string `json:"action"` // create / update
	OK        bool   `json:"ok"`
	Message   string `json:"message"`
	DryRun    bool   `json:"dry_run"`
}

// ApplyYAML POST /api/v1/clusters/:cluster/apply  body: {yaml, dry_run, namespace}
func (h *H) ApplyYAML(c *gin.Context) {
	clusterName := c.Param("cluster")
	// 限制 body 大小（4MB YAML 已极充裕，防 DoS）
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4*1024*1024)
	e, err := h.mgr.Get(clusterName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	var req struct {
		YAML      string `json:"yaml" binding:"required"`
		DryRun    bool   `json:"dry_run"`
		Namespace string `json:"namespace"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	results := []applyResultItem{}
	for _, doc := range docSplitRe.Split(req.YAML, -1) {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		results = append(results, h.applyOne(c, e, clusterName, doc, req.Namespace, req.DryRun))
	}
	okAll := true
	for _, r := range results {
		if !r.OK {
			okAll = false
		}
	}
	action := "apply"
	if req.DryRun {
		action = "apply_dry_run"
	}
	status := "success"
	if !okAll {
		status = "failed"
	}
	// 审计明细列出每个对象的 Kind/名称与动作（create/update），避免"n 个文档"不可读
	docParts := make([]string, 0, len(results))
	for _, r := range results {
		mark := "√"
		if !r.OK {
			mark = "×"
		}
		docParts = append(docParts, fmt.Sprintf("%s%s/%s（%s）", mark, r.Kind, r.Name, r.Action))
	}
	detail := fmt.Sprintf("%d 个文档：%s", len(results), strings.Join(docParts, "、"))
	if req.DryRun {
		detail = "试运行（未实际变更集群），" + detail
	}
	// 附带部署时提交的 YAML：审计页详情含多行时显示"查看详情"入口，点击即见当时部署内容
	yamlText := req.YAML
	if len(yamlText) > 60000 {
		// 按字符边界截断：YAML 常含中文（注解/ConfigMap 内容），字节截断会切出
		// 非法 UTF-8，入库和回显时变成 U+FFFD 乱码
		yamlText = utf8Trunc(yamlText, 60000) + "\n…（内容过长已截断）"
	}
	detail += "\n\n【部署 YAML】\n" + yamlText
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), action, clusterName, req.Namespace, "yaml", "", detail, status))
	c.JSON(http.StatusOK, gin.H{"results": results, "ok": okAll})
}

func (h *H) applyOne(c *gin.Context, e *cluster.Entry, clusterName, doc, defaultNS string, dryRun bool) applyResultItem {
	var obj map[string]any
	if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
		return applyResultItem{OK: false, Message: "YAML 解析失败: " + err.Error()}
	}
	apiVersion := getStr(obj, "apiVersion")
	kind := getStr(obj, "kind")
	name := getStr(obj, "metadata", "name")
	ns := getStr(obj, "metadata", "namespace")
	if apiVersion == "" || kind == "" || name == "" {
		return applyResultItem{Kind: kind, Name: name, Namespace: ns, OK: false, Message: "缺少 apiVersion/kind/metadata.name"}
	}
	gv := strings.SplitN(apiVersion, "/", 2)
	gvk := schema.GroupVersionKind{Kind: kind, Version: gv[0]}
	if len(gv) == 2 {
		gvk.Group, gvk.Version = gv[0], gv[1]
	}
	mapping, err := e.Mapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return applyResultItem{Kind: kind, Name: name, Namespace: ns, OK: false, Message: "无法识别资源类型 " + apiVersion + "/" + kind}
	}
	// cluster-scope 资源：apiserver 要求 metadata.namespace 为空；前端表单 / YAML 误填都需清掉
	if mapping.Scope.Name() != meta.RESTScopeNameNamespace {
		if meta, ok := obj["metadata"].(map[string]any); ok {
			delete(meta, "namespace")
		}
		ns = ""
	} else {
		if ns == "" {
			ns = defaultNS
			_ = unstructured.SetNestedField(obj, ns, "metadata", "namespace")
		}
	}
	gvr := mapping.Resource
	var res dynamic.ResourceInterface
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		res = e.Dynamic.Resource(gvr).Namespace(ns)
	} else {
		res = e.Dynamic.Resource(gvr)
	}

	// verb 级鉴权：CR 用 group/plural 全限定名（与前端授权保存一致），内置资源用 plural
	// 先检查 create/update 任一权限再去读集群——避免无权限用户借 apply 探测资源是否存在
	allowedKey := resKeyForAuth(mapping.GroupVersionKind.Group, gvr.Resource)
	uid, uname, role := middleware.UID(c), middleware.Username(c), middleware.Role(c)
	canCreate := middleware.Allowed(h.db, role, uid, clusterName, ns, "create", allowedKey)
	canUpdate := middleware.Allowed(h.db, role, uid, clusterName, ns, "update", allowedKey)
	if !canCreate && !canUpdate {
		audit.Log(audit.Record(uid, uname, "apply", clusterName, ns, allowedKey, name, "权限拒绝：无 create/update 权限", "denied"))
		return applyResultItem{Kind: kind, Name: name, Namespace: ns, OK: false,
			Message: "无 " + resDisplayLabel(allowedKey) + " 的 create/update 权限"}
	}

	// 已存在 → update；否则 create
	existing, gerr := res.Get(c.Request.Context(), name, metav1.GetOptions{})
	verb := "create"
	if gerr == nil {
		verb = "update"
	}
	// Pod 是运行实例不支持编辑：apply 更新 Pod 与 YAML 编辑入口一致拦截（创建不拦，属部署行为）
	if verb == "update" && gvr.Group == "" && gvr.Resource == "pods" {
		return applyResultItem{Kind: kind, Name: name, Namespace: ns, Action: verb, OK: false,
			Message: "Pod 是运行实例，不支持更新；请修改所属的应用（Deployment 等）", DryRun: dryRun}
	}
	if (verb == "update" && !canUpdate) || (verb == "create" && !canCreate) {
		audit.Log(audit.Record(uid, uname, "apply", clusterName, ns, allowedKey, name, "权限拒绝：无 "+verb+" 权限", "denied"))
		return applyResultItem{Kind: kind, Name: name, Namespace: ns, OK: false,
			Message: "无 " + resDisplayLabel(allowedKey) + " 的 " + verb + " 权限"}
	}

	// 字段级鉴权：同上用 group/plural
	stripManagedFields(obj)
	// update 时做字段级校验（create 不涉及）
	if verb == "update" {
		if patterns, all := middleware.AllowedUpdateFields(h.db, middleware.Role(c), middleware.UID(c), clusterName, ns, allowedKey); !all {
			if bad := middleware.FieldViolations(existing.Object, obj, patterns); len(bad) > 0 {
				detail := "该授权未开放任何可修改字段"
				if len(patterns) > 0 {
					detail = "可修改: " + strings.Join(patterns, ", ")
				}
				audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "apply", clusterName, ns, allowedKey, name,
					"越权字段: "+strings.Join(bad, ", ")+"；"+detail, "denied"))
				return applyResultItem{Kind: kind, Name: name, Namespace: ns, Action: verb, OK: false,
					Message: "无权修改字段: " + strings.Join(bad, ", ") + "（" + detail + "）", DryRun: dryRun}
			}
		}
	}
	u := &unstructured.Unstructured{Object: obj}
	var dryOpts []string
	if dryRun {
		dryOpts = []string{metav1.DryRunAll}
	}
	if verb == "update" {
		_, err = res.Update(c.Request.Context(), u, metav1.UpdateOptions{DryRun: dryOpts})
	} else {
		_, err = res.Create(c.Request.Context(), u, metav1.CreateOptions{DryRun: dryOpts})
	}
	if err != nil {
		return applyResultItem{Kind: kind, Name: name, Namespace: ns, Action: verb, OK: false, Message: err.Error(), DryRun: dryRun}
	}
	msg := "创建成功"
	if verb == "update" {
		msg = "更新成功"
	}
	if dryRun {
		msg += "（试运行）"
	}
	return applyResultItem{Kind: kind, Name: name, Namespace: ns, Action: verb, OK: true, Message: msg, DryRun: dryRun}
}

// ---------- 副本数 / 重启 ----------

// Scale POST /api/v1/clusters/:cluster/scale
func (h *H) Scale(c *gin.Context) {
	clusterName := c.Param("cluster")
	var req struct {
		Group     string `json:"group"`
		Version   string `json:"version" binding:"required"`
		Resource  string `json:"resource" binding:"required"`
		Namespace string `json:"namespace"`
		Name      string `json:"name" binding:"required"`
		Replicas  int64  `json:"replicas"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	e, err := h.mgr.Get(clusterName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	allowedResKey := resKeyForAuth(req.Group, req.Resource)
	if !middleware.Allowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, req.Namespace, "update", allowedResKey) {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "scale", clusterName, req.Namespace, req.Resource, req.Name, "权限拒绝", "denied"))
		denyResource403(c, "update", allowedResKey)
		return
	}
	if !middleware.ActionAllowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, req.Namespace, allowedResKey, "replicas") {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "scale", clusterName, req.Namespace, req.Resource, req.Name, "权限拒绝：未授权副本数动作", "denied"))
		c.JSON(http.StatusForbidden, gin.H{"message": "无副本数调整权限"})
		return
	}
	gvr := schema.GroupVersionResource{Group: req.Group, Version: req.Version, Resource: req.Resource}
	ri := dynamicRes(e, gvr, req.Namespace)
	obj, err := ri.Get(c.Request.Context(), req.Name, metav1.GetOptions{})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": err.Error()})
		return
	}
	if _, ok, _ := unstructured.NestedInt64(obj.Object, "spec", "replicas"); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"message": "该资源不支持副本数调整"})
		return
	}
	_ = unstructured.SetNestedField(obj.Object, req.Replicas, "spec", "replicas")
	if _, err := ri.Update(c.Request.Context(), obj, metav1.UpdateOptions{}); err != nil {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "scale", clusterName, req.Namespace, req.Resource, req.Name, fmt.Sprintf("副本数→%d 失败: %v", req.Replicas, err), "failed"))
		c.JSON(http.StatusBadGateway, gin.H{"message": "调整失败: " + err.Error()})
		return
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "scale", clusterName, req.Namespace, req.Resource, req.Name, fmt.Sprintf("副本数调整为 %d", req.Replicas), "success"))
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// restartableResource 滚动重启（注解 spec.template 触发）仅对这三类工作负载有意义
func restartableResource(resource string) bool {
	return resource == "deployments" || resource == "statefulsets" || resource == "daemonsets"
}

// Restart POST /api/v1/clusters/:cluster/restart — merge patch 注入 restartedAt 触发滚动重启
func (h *H) Restart(c *gin.Context) {
	clusterName := c.Param("cluster")
	var req struct {
		Group     string `json:"group"`
		Version   string `json:"version" binding:"required"`
		Resource  string `json:"resource" binding:"required"`
		Namespace string `json:"namespace"`
		Name      string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	if !restartableResource(req.Resource) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "仅 Deployment / StatefulSet / DaemonSet 支持滚动重启"})
		return
	}
	e, err := h.mgr.Get(clusterName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	allowedResKey := resKeyForAuth(req.Group, req.Resource)
	if !middleware.Allowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, req.Namespace, "update", allowedResKey) {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "restart", clusterName, req.Namespace, req.Resource, req.Name, "权限拒绝", "denied"))
		denyResource403(c, "update", allowedResKey)
		return
	}
	if !middleware.ActionAllowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, req.Namespace, allowedResKey, "restart") {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "restart", clusterName, req.Namespace, req.Resource, req.Name, "权限拒绝：未授权重启动作", "denied"))
		c.JSON(http.StatusForbidden, gin.H{"message": "无重启权限"})
		return
	}
	gvr := schema.GroupVersionResource{Group: req.Group, Version: req.Version, Resource: req.Resource}
	ri := dynamicRes(e, gvr, req.Namespace)
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`, time.Now().Format(time.RFC3339))
	if _, err := ri.Patch(c.Request.Context(), req.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "restart", clusterName, req.Namespace, req.Resource, req.Name, err.Error(), "failed"))
		c.JSON(http.StatusBadGateway, gin.H{"message": "重启失败: " + err.Error()})
		return
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "restart", clusterName, req.Namespace, req.Resource, req.Name, "滚动重启", "success"))
	c.JSON(http.StatusOK, gin.H{"message": "重启指令已下发"})
}

// ---------- 白屏表单编辑 ----------

// workloadPodSpecPath 各资源 podSpec 所在路径
func workloadPodSpecPath(resource string) []string {
	switch resource {
	case "pods":
		return []string{"spec"}
	case "cronjobs":
		return []string{"spec", "jobTemplate", "spec", "template", "spec"}
	default:
		return []string{"spec", "template", "spec"}
	}
}

// formContainerEdit 表单提交的单容器变更（仅含当前 section 的字段）
type formContainerEdit struct {
	Name      string           `json:"name"`
	Image     string           `json:"image"`
	Command   []string         `json:"command"`
	Args      []string         `json:"args"`
	Ports     []map[string]any `json:"ports"`
	Resources map[string]any   `json:"resources"`
	Env       []map[string]any `json:"env"`
	Mounts    []formVolMount   `json:"mounts"`     // storage 模块：本容器的新挂载描述
	KeepMount []map[string]any `json:"keepMounts"` // storage 模块：原样保留的挂载（须与现有一致，防越权注入）
	Probes    []formProbeEdit  `json:"probes"`     // probes 模块：本容器的探针配置
}

// formProbeEdit 探针编辑：Kind = liveness/readiness/startup；Probe 为完整探针对象（nil = 删除该探针）
type formProbeEdit struct {
	Kind  string         `json:"kind"`
	Probe map[string]any `json:"probe"`
}

// formIngRoute Ingress 路由规则行（routes 模块）：host 可空（通配），port 为数字或端口名
type formIngRoute struct {
	Host     string `json:"host"`
	Path     string `json:"path"`
	PathType string `json:"pathType"`
	Svc      string `json:"svc"`
	Port     any    `json:"port"` // 前端 el-input 可能传 number，统一经 portString 归一
}

// portString 归一端口值（数字/字符串）为字符串；空返回 ""
func (r formIngRoute) portString() string {
	switch v := r.Port.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.Itoa(int(v))
	case int:
		return strconv.Itoa(v)
	}
	return ""
}

// hostOfRule 取 rule 的 host（无 host 键 = 通配 ""），用于同 host 相邻归组判断
func hostOfRule(rule map[string]any) string {
	h, _ := rule["host"].(string)
	return h
}

// formVolDesc 存储卷描述（storage 模块）：type 四选一，refName 为 PVC/CM/Secret 对象名
type formVolDesc struct {
	Name    string `json:"name"`
	Type    string `json:"type"` // pvc / configmap / secret / emptydir
	RefName string `json:"refName"`
}

// formVolMount 存储挂载描述：name 引用 formVolDesc.Name
type formVolMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
	SubPath   string `json:"subPath"`
	ReadOnly  bool   `json:"readOnly"`
}

// ---------- 调度策略（scheduling 模块）与节点污点（taints 模块）----------

// formTaint 节点污点行（taints 模块）
type formTaint struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Effect string `json:"effect"`
}

// taintObj 转为 K8s taint 对象；value 空时省略（key+effect 的污点合法存在）
func taintObj(t formTaint) map[string]any {
	m := map[string]any{"key": t.Key, "effect": t.Effect}
	if t.Value != "" {
		m["value"] = t.Value
	}
	return m
}

// validTaintEffects 污点 effect 白名单
var validTaintEffects = map[string]bool{"NoSchedule": true, "PreferNoSchedule": true, "NoExecute": true}

// systemTaintKeys 系统管理型污点：kubelet/云控制器维护，表单拒绝增删改（改了也会被立刻改回）
var systemTaintKeys = map[string]bool{
	"node.kubernetes.io/not-ready":                   true,
	"node.kubernetes.io/unreachable":                 true,
	"node.kubernetes.io/unschedulable":               true,
	"node.cloudprovider.kubernetes.io/uninitialized": true,
	"node.kubernetes.io/network-unavailable":         true,
}

// validateTaint 校验第 n 行污点（key/value 标签格式 + effect 白名单）。
// 系统污点的增删改拦截不在此处（见 resedit taints 分支的对照校验）：系统污点行必须原样提交才能保留
func validateTaint(n int, t formTaint) string {
	key, name := t.Key, t.Key
	if i := strings.Index(key, "/"); i >= 0 {
		if strings.Count(key, "/") > 1 {
			return fmt.Sprintf("第 %d 行污点键 %q 不合法（前缀只能有一段 /）", n, key)
		}
		prefix := key[:i]
		name = key[i+1:]
		if prefix == "" || len(prefix) > 253 {
			return fmt.Sprintf("第 %d 行污点键 %q 的前缀不合法（需非空且 ≤253 字符）", n, key)
		}
		for _, seg := range strings.Split(prefix, ".") {
			if !labelPrefixSegRe.MatchString(seg) {
				return fmt.Sprintf("第 %d 行污点键的前缀需为 DNS 子域（小写字母/数字/中划线/点）", n)
			}
		}
	}
	if name == "" || len(name) > 63 || !labelNameRe.MatchString(name) {
		return fmt.Sprintf("第 %d 行污点键 %q 不合法（名称 ≤63 字符，字母/数字开头结尾，可含 - _ .，可选 DNS 前缀）", n, key)
	}
	if t.Value != "" && (len(t.Value) > 63 || !labelValueRe.MatchString(t.Value)) {
		return fmt.Sprintf("第 %d 行污点值不合法（≤63 字符，字母或数字开头结尾，可含 - _ .）", n)
	}
	if !validTaintEffects[t.Effect] {
		return fmt.Sprintf("第 %d 行 effect 不合法（NoSchedule / PreferNoSchedule / NoExecute）", n)
	}
	return ""
}

// applyScheduling 把 scheduling 模块 payload 写入 podSpec 路径下的 nodeSelector/nodeName/affinity。
// 返回错误文案（空=通过）。payload 形状：
//
//	{node_selector: {k:v}, node_name: "", node_affinity_required: [{key,operator,values}],
//	 node_affinity_preferred: [{weight,key,operator,values}],
//	 pod_anti_required: [{key,operator,values,topology_key}],
//	 pod_anti_preferred: [{weight,key,operator,values,topology_key}]}
func applyScheduling(obj *unstructured.Unstructured, podSpec []string, payload map[string]any) error {
	// 前置守卫：现有 affinity 若含建模外结构，明确拒绝（不静默合并/覆盖）
	aff, found, _ := unstructured.NestedMap(obj.Object, append(podSpec, "affinity")...)
	if found && hasUnmodeledAffinity(aff) {
		return fmt.Errorf("检测到表单不支持的高级亲和配置（多 term / matchFields / Pod 亲和等），请使用 YAML 编辑")
	}

	// nodeSelector：空表删除字段
	ns, _ := payload["node_selector"].(map[string]any)
	cleanNS := map[string]any{}
	for k, v := range ns {
		s, ok := v.(string)
		if !ok || k == "" {
			return fmt.Errorf("节点标签选择器的键值需为字符串")
		}
		cleanNS[k] = s
	}
	if len(cleanNS) > 0 {
		for k, v := range cleanNS {
			if msg := validateSelectorKV(k, v.(string)); msg != "" {
				return errors.New(msg)
			}
		}
		_ = unstructured.SetNestedField(obj.Object, cleanNS, append(podSpec, "nodeSelector")...)
	} else {
		unstructured.RemoveNestedField(obj.Object, append(podSpec, "nodeSelector")...)
	}

	// nodeName：空串删除字段
	nn, _ := payload["node_name"].(string)
	nn = strings.TrimSpace(nn)
	if nn != "" {
		if len(nn) > 253 || !labelPrefixSegRe.MatchString(nn) {
			return fmt.Errorf("指定节点名不合法（DNS 名，如 node1.example.com）")
		}
		_ = unstructured.SetNestedField(obj.Object, nn, append(podSpec, "nodeName")...)
	} else {
		unstructured.RemoveNestedField(obj.Object, append(podSpec, "nodeName")...)
	}

	// 节点亲和 required / preferred + Pod 反亲和 required / preferred（四组行统一解析：
	// 表达式校验 + preferred 权重 + 反亲和 topologyKey 都在这里，行与产出一一对应）
	nar, _ := payload["node_affinity_required"].([]any)
	reqs, err := matchExprRows(nar, false)
	if err != nil {
		return err
	}
	nap, _ := payload["node_affinity_preferred"].([]any)
	prefs, err := matchExprRows(nap, true)
	if err != nil {
		return err
	}
	pareq, _ := payload["pod_anti_required"].([]any)
	antiReqs, err := matchExprRows(pareq, false)
	if err != nil {
		return err
	}
	papre, _ := payload["pod_anti_preferred"].([]any)
	antiPrefs, err := matchExprRows(papre, true)
	if err != nil {
		return err
	}
	// 反亲和行必须带拓扑键（节点亲和行不带，topologyKey 留空）
	for i, r := range antiReqs {
		if r.topologyKey == "" {
			return fmt.Errorf("Pod 反亲和第 %d 行的拓扑键不能为空（如 kubernetes.io/hostname）", i+1)
		}
	}
	for i, r := range antiPrefs {
		if r.topologyKey == "" {
			return fmt.Errorf("Pod 反亲和第 %d 行的拓扑键不能为空（如 kubernetes.io/hostname）", i+1)
		}
	}

	var nodeAff, podAnti map[string]any
	if len(reqs) > 0 || len(prefs) > 0 {
		nodeAff = map[string]any{}
		if len(reqs) > 0 {
			// 建模为单个 term：多行 matchExpressions 之间是 AND 关系（单 term 内语义）
			nodeAff["requiredDuringSchedulingIgnoredDuringExecution"] = map[string]any{
				"nodeSelectorTerms": []any{map[string]any{"matchExpressions": exprList(reqs)}},
			}
		}
		if len(prefs) > 0 {
			// preferred term 真实形状：{weight, preference: {nodeSelectorTerms: [{matchExpressions: [...]}]}}
			// 每行独立打分（行间 OR），与表单行语义一致
			terms := make([]any, 0, len(prefs))
			for _, r := range prefs {
				terms = append(terms, map[string]any{
					"weight": r.weight,
					"preference": map[string]any{
						"nodeSelectorTerms": []any{map[string]any{"matchExpressions": []any{r.expr}}},
					},
				})
			}
			nodeAff["preferredDuringSchedulingIgnoredDuringExecution"] = terms
		}
	}
	if len(antiReqs) > 0 || len(antiPrefs) > 0 {
		podAnti = map[string]any{}
		if len(antiReqs) > 0 {
			// required term 为 PodAffinityTerm 平铺：{topologyKey, labelSelector}
			terms := make([]any, 0, len(antiReqs))
			for _, r := range antiReqs {
				terms = append(terms, map[string]any{
					"topologyKey":   r.topologyKey,
					"labelSelector": map[string]any{"matchExpressions": []any{r.expr}},
				})
			}
			podAnti["requiredDuringSchedulingIgnoredDuringExecution"] = terms
		}
		if len(antiPrefs) > 0 {
			// preferred term 形状：{weight, podAffinityTerm: {topologyKey, labelSelector}}
			terms := make([]any, 0, len(antiPrefs))
			for _, r := range antiPrefs {
				terms = append(terms, map[string]any{
					"weight": r.weight,
					"podAffinityTerm": map[string]any{
						"topologyKey":   r.topologyKey,
						"labelSelector": map[string]any{"matchExpressions": []any{r.expr}},
					},
				})
			}
			podAnti["preferredDuringSchedulingIgnoredDuringExecution"] = terms
		}
	}

	if nodeAff == nil && podAnti == nil {
		if found {
			// 全清空且原有 affinity 就是建模子集 → 删除字段
			unstructured.RemoveNestedField(obj.Object, append(podSpec, "affinity")...)
		}
		return nil
	}
	aff2 := map[string]any{}
	if nodeAff != nil {
		aff2["nodeAffinity"] = nodeAff
	}
	if podAnti != nil {
		aff2["podAntiAffinity"] = podAnti
	}
	_ = unstructured.SetNestedMap(obj.Object, aff2, append(podSpec, "affinity")...)
	return nil
}

// schedExprRow 行模型解析结果：表达式 + preferred 权重 + 反亲和拓扑键（节点亲和行 topologyKey 为空）
type schedExprRow struct {
	expr        map[string]any
	weight      int64
	topologyKey string
}

// exprList 行集合 → matchExpressions 数组
func exprList(rows []schedExprRow) []any {
	out := make([]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.expr)
	}
	return out
}

// matchExprRows 行模型 → schedExprRow（key/operator/values 校验；preferred 校验权重 1-100；
// 带 topology_key 的行（Pod 反亲和）校验非空。行与产出一一对应，无索引错位）
func matchExprRows(rows []any, preferred bool) ([]schedExprRow, error) {
	out := make([]schedExprRow, 0, len(rows))
	for i, rv := range rows {
		row, ok := rv.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("第 %d 行格式错误", i+1)
		}
		key, _ := row["key"].(string)
		key = strings.TrimSpace(key)
		op, _ := row["operator"].(string)
		op = strings.TrimSpace(op)
		w, _ := row["weight"].(float64)
		values, _ := row["values"].([]any)
		var vals []string
		for _, v := range values {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("第 %d 行的值需为字符串", i+1)
			}
			if s = strings.TrimSpace(s); s != "" {
				vals = append(vals, s)
			}
		}
		if msg := validateSelectorKV(key, ""); msg != "" && key != "" {
			return nil, fmt.Errorf("第 %d 行的键 %s", i+1, msg)
		}
		if key == "" {
			return nil, fmt.Errorf("第 %d 行的键不能为空", i+1)
		}
		switch op {
		case "In", "NotIn", "Gt", "Lt":
			if len(vals) == 0 {
				return nil, fmt.Errorf("第 %d 行 %s 操作符需要至少一个值", i+1, op)
			}
		case "Exists", "DoesNotExist":
			vals = nil
		default:
			return nil, fmt.Errorf("第 %d 行操作符 %q 不合法（In/NotIn/Exists/DoesNotExist/Gt/Lt）", i+1, op)
		}
		r := schedExprRow{expr: map[string]any{"key": key, "operator": op}}
		if len(vals) > 0 {
			// 必须 []string→[]any 且元素为 string：unstructured 深拷贝只认 JSON 类型
			r.expr["values"] = strsToAny(vals)
		}
		if tk, has := row["topology_key"]; has {
			tks, _ := tk.(string)
			tks = strings.TrimSpace(tks)
			if tks == "" {
				return nil, fmt.Errorf("第 %d 行的拓扑键不能为空（如 kubernetes.io/hostname）", i+1)
			}
			r.topologyKey = tks
		}
		if preferred {
			if w < 1 || w > 100 {
				return nil, fmt.Errorf("第 %d 行权重需为 1-100", i+1)
			}
			r.weight = int64(w)
		}
		out = append(out, r)
	}
	return out, nil
}

// validateSelectorKV 节点标签键/值格式校验（值可空=只校验键）
func validateSelectorKV(k, v string) string {
	name := k
	if i := strings.LastIndex(k, "/"); i >= 0 {
		prefix, n := k[:i], k[i+1:]
		if prefix == "" || n == "" || len(prefix) > 253 {
			return fmt.Sprintf("键 %q 不合法（前缀需为 DNS 子域）", k)
		}
		for _, seg := range strings.Split(prefix, ".") {
			if !labelPrefixSegRe.MatchString(seg) {
				return fmt.Sprintf("键 %q 的前缀需为 DNS 子域（小写字母/数字/中划线/点）", k)
			}
		}
		name = n
	}
	if name == "" || len(name) > 63 || !labelNameRe.MatchString(name) {
		return fmt.Sprintf("键 %q 不合法（≤63 字符，字母或数字开头结尾，可含 - _ . 与可选域名前缀）", k)
	}
	if v != "" && (len(v) > 63 || !labelValueRe.MatchString(v)) {
		return fmt.Sprintf("键 %q 的值不合法（≤63 字符，字母或数字开头结尾，可含 - _ .）", k)
	}
	return ""
}

// hasUnmodeledAffinity 检测 affinity 中建模外结构（表单只支持单 term / matchExpressions）：
// 节点亲和多 term、matchFields、podAffinity、namespaceSelector、namespace（pod 亲和 term 上）均视为建模外
func hasUnmodeledAffinity(aff map[string]any) bool {
	// 非建模键（nodeAffinity/podAntiAffinity 之外，如 podAffinity）整体视为建模外
	for k := range aff {
		if k != "nodeAffinity" && k != "podAntiAffinity" {
			return true
		}
	}
	if na, ok := aff["nodeAffinity"].(map[string]any); ok {
		for k := range na {
			if k != "requiredDuringSchedulingIgnoredDuringExecution" && k != "preferredDuringSchedulingIgnoredDuringExecution" {
				return true
			}
		}
		if r, ok := na["requiredDuringSchedulingIgnoredDuringExecution"]; ok {
			m, ok := r.(map[string]any)
			if !ok {
				return true
			}
			terms, _ := m["nodeSelectorTerms"].([]any)
			if len(terms) > 1 {
				return true // 多 term = OR 关系，表单行间是 AND，语义不同，拒绝
			}
			if len(terms) == 1 {
				t, ok := terms[0].(map[string]any)
				if !ok {
					return true
				}
				for k := range t {
					if k != "matchExpressions" {
						return true // matchFields 等
					}
				}
				if mx, _ := t["matchExpressions"].([]any); len(mx) == 0 {
					return true // 空 matchExpressions 的 term 读回 0 行，保存会静默丢掉
				}
			}
		}
		// preferred：表单产出的形状是 {weight, preference: {nodeSelectorTerms: [{matchExpressions: [单表达式]}]}}。
		// 每个 term 只允许恰好一个表达式——多表达式是单 term 内 AND，拆成多行保存会变成多个独立
		// 打分 term（OR），静默改义，一律拒绝
		if r, ok := na["preferredDuringSchedulingIgnoredDuringExecution"].([]any); ok {
			for _, pv := range r {
				p, ok := pv.(map[string]any)
				if !ok || len(p) != 2 || p["weight"] == nil || p["preference"] == nil {
					return true
				}
				pref, ok := p["preference"].(map[string]any)
				if !ok || len(pref) != 1 {
					return true
				}
				terms, _ := pref["nodeSelectorTerms"].([]any)
				if len(terms) != 1 {
					return true
				}
				t, ok := terms[0].(map[string]any)
				if !ok {
					return true
				}
				for k := range t {
					if k != "matchExpressions" {
						return true
					}
				}
				if mx, _ := t["matchExpressions"].([]any); len(mx) != 1 {
					return true
				}
			}
		}
	}
	if pa, ok := aff["podAntiAffinity"].(map[string]any); ok {
		for _, grp := range []string{"requiredDuringSchedulingIgnoredDuringExecution", "preferredDuringSchedulingIgnoredDuringExecution"} {
			if r, ok := pa[grp]; ok {
				terms, _ := r.([]any)
				for _, tv := range terms {
					t, ok := tv.(map[string]any)
					if !ok {
						return true
					}
					if preferred := grp == "preferredDuringSchedulingIgnoredDuringExecution"; preferred {
						// preferred term 形如 {weight, podAffinityTerm}
						if _, ok := t["weight"]; !ok {
							return true
						}
						pt, ok := t["podAffinityTerm"].(map[string]any)
						if !ok {
							return singleKeyAffinityTerm(t)
						}
						if unmodeledPodTerm(pt) {
							return true
						}
					} else {
						if unmodeledPodTerm(t) {
							return true
						}
						if singleKeyAffinityTerm(t) {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// unmodeledPodTerm Pod 亲和 term 的建模外检测（表单只支持 topologyKey + labelSelector.matchExpressions）
func unmodeledPodTerm(t map[string]any) bool {
	for k := range t {
		if k != "topologyKey" && k != "podAffinityTerm" {
			// required term 允许直接平铺 topologyKey/labelSelector（K8s 两种形态等价），
			// 但表单只产出 podAffinityTerm 形态；平铺形态也接受（读回时归一）
			if k != "labelSelector" {
				return true
			}
		}
	}
	pt, ok := t["podAffinityTerm"].(map[string]any)
	if !ok {
		if _, hasLS := t["labelSelector"]; hasLS {
			pt = t // 平铺形态
		} else {
			return true
		}
	}
	return podTermExtraKeys(pt)
}

// singleKeyAffinityTerm 单键非 podAffinityTerm 形态的 term 判定
func singleKeyAffinityTerm(t map[string]any) bool {
	if len(t) == 2 {
		if _, ok := t["topologyKey"]; ok {
			if _, ok := t["labelSelector"]; ok {
				return false
			}
		}
	}
	return len(t) > 2
}

// podTermExtraKeys podAffinityTerm 内部建模外键检测
func podTermExtraKeys(pt map[string]any) bool {
	for k := range pt {
		if k != "topologyKey" && k != "labelSelector" && k != "namespaceSelector" && k != "namespaces" {
			return true
		}
	}
	if _, ok := pt["namespaceSelector"]; ok {
		return true
	}
	if _, ok := pt["namespaces"]; ok {
		return true
	}
	ls, ok := pt["labelSelector"].(map[string]any)
	if !ok {
		return pt["labelSelector"] != nil
	}
	for k := range ls {
		if k != "matchExpressions" {
			return true // matchLabels 也拒绝（表单不产出；行模型可表达为 In 单值，读回时归一）
		}
	}
	// 每个 term 只允许恰好一个表达式：多表达式（单 selector 内 AND）拆成多行保存会变成
	// 多个 term（行间 OR），静默改义；零个则保存会静默丢掉该 term
	if mx, _ := ls["matchExpressions"].([]any); len(mx) != 1 {
		return true
	}
	return false
}

// buildProbeObj 校验并重建单个探针对象（白名单形状：httpGet/tcpSocket/exec 三选一 + 数值参数范围钳制）。
// 表单不支持的键（httpHeaders 等）直接丢弃——白名单形状重建，不透传 payload 未知字段
func buildProbeObj(p map[string]any) (map[string]any, string) {
	out := map[string]any{}
	intFields := []struct {
		key   string
		label string
		min   int64
		max   int64
	}{
		{"initialDelaySeconds", "延迟秒数", 0, 3600},
		{"periodSeconds", "间隔秒数", 1, 3600},
		{"timeoutSeconds", "超时秒数", 1, 3600},
		{"successThreshold", "成功阈值", 1, 100},
		{"failureThreshold", "失败阈值", 1, 100},
	}
	for _, f := range intFields {
		v, ok := p[f.key]
		if !ok || v == nil {
			continue
		}
		n, ok := toInt64(v)
		if !ok {
			return nil, f.label + "需为整数"
		}
		if n < f.min {
			n = f.min
		}
		if n > f.max {
			n = f.max
		}
		out[f.key] = n
	}
	switch p["type"] {
	case "http":
		path, _ := p["path"].(string)
		path = strings.TrimSpace(path)
		if path == "" {
			path = "/"
		}
		if !strings.HasPrefix(path, "/") {
			return nil, "HTTP 路径需以 / 开头"
		}
		port, msg := probePort(p["port"])
		if msg != "" {
			return nil, msg
		}
		if port == nil {
			return nil, "HTTP 探针需填写端口"
		}
		scheme, _ := p["scheme"].(string)
		if scheme != "HTTPS" {
			scheme = "HTTP" // 缺省/非法值回落 HTTP（K8s 默认）
		}
		out["httpGet"] = map[string]any{"path": path, "port": port, "scheme": scheme}
	case "tcp":
		port, msg := probePort(p["port"])
		if msg != "" {
			return nil, msg
		}
		if port == nil {
			return nil, "TCP 探针需填写端口"
		}
		out["tcpSocket"] = map[string]any{"port": port}
	case "exec":
		cmdV, _ := p["command"].([]any)
		cmds := make([]string, 0, len(cmdV))
		for _, c := range cmdV {
			s, ok := c.(string)
			if !ok || strings.TrimSpace(s) == "" {
				continue
			}
			cmds = append(cmds, s)
		}
		if len(cmds) == 0 {
			return nil, "执行命令探针需填写命令"
		}
		out["exec"] = map[string]any{"command": strsToAny(cmds)}
	default:
		return nil, "探针类型需为 http/tcp/exec"
	}
	return out, ""
}

// probePort 探针端口：整数（1-65535）或端口名（DNS 名）；空返回 nil
func probePort(v any) (any, string) {
	switch t := v.(type) {
	case nil:
		return nil, ""
	case float64:
		n := int64(t)
		if n < 1 || n > 65535 {
			return nil, "探针端口需为 1-65535"
		}
		return n, ""
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return nil, ""
		}
		if n, err := strconv.Atoi(s); err == nil {
			if n < 1 || n > 65535 {
				return nil, "探针端口需为 1-65535"
			}
			// 必须 int64：unstructured 深拷贝只认 JSON 类型，原生 int 会 panic（cannot deep copy int）
			return int64(n), ""
		}
		if len(s) > 15 || !dnsLabelRe.MatchString(s) {
			return nil, "探针端口名不合法（小写字母/数字/中划线，≤15 字符）"
		}
		return s, ""
	default:
		return nil, "探针端口格式不对"
	}
}

// toInt64 宽松整数转换（JSON 解码后可能是 float64 / int64 / string）
func toInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case float64:
		return int64(t), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		return int64(n), err == nil
	}
	return 0, false
}

var dnsLabelRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// ConfigMap / Secret 数据键（K8s 语义：字母数字、中划线、点、下划线）
var dataKeyRe = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)

// buildVolume 白名单形状重建卷对象；类型外的卷（hostPath/projected 等）不允许经表单新增
func buildVolume(v formVolDesc) map[string]any {
	switch v.Type {
	case "pvc":
		return map[string]any{"name": v.Name, "persistentVolumeClaim": map[string]any{"claimName": v.RefName}}
	case "configmap":
		return map[string]any{"name": v.Name, "configMap": map[string]any{"name": v.RefName}}
	case "secret":
		return map[string]any{"name": v.Name, "secret": map[string]any{"secretName": v.RefName}}
	default:
		return map[string]any{"name": v.Name, "emptyDir": map[string]any{}}
	}
}

// volMountObj 挂载描述 → K8s volumeMounts 条目（空子字段省略）
func volMountObj(m formVolMount) map[string]any {
	out := map[string]any{"name": m.Name, "mountPath": m.MountPath}
	if m.SubPath != "" {
		out["subPath"] = m.SubPath
	}
	if m.ReadOnly {
		out["readOnly"] = true
	}
	return out
}

// sameAsExisting 归一化深度相等（数字类型归一，与字段 diff 同规则）
func sameAsExisting(v any, existing []any) bool {
	nv := normAny(v)
	for _, e := range existing {
		if reflect.DeepEqual(nv, normAny(e)) {
			return true
		}
	}
	return false
}

func normAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, vv := range t {
			m[k] = normAny(vv)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, vv := range t {
			s[i] = normAny(vv)
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

func mapsToAny(ms []map[string]any) []any {
	out := make([]any, len(ms))
	for i, m := range ms {
		out[i] = m
	}
	return out
}

// strsToAny []string → []any（unstructured 只接受 JSON 类型，带类型切片深拷贝会 panic）
func strsToAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// isBuiltInWorkload 工作负载资源（CR/其他不属于）
func isBuiltInWorkload(resource string) bool {
	switch resource {
	case "deployments", "statefulsets", "daemonsets", "jobs", "cronjobs":
		return true
	}
	return false
}

var workloadFormSections = map[string]bool{"image": true, "command": true, "ports": true, "resources": true, "env": true, "probes": true, "storage": true, "scheduling": true}
var serviceFormSections = map[string]bool{"ports": true, "selector": true}
var ingressFormSections = map[string]bool{"routes": true}

// probeRe 探针字段名与合法 kind
var probeKinds = map[string]bool{"liveness": true, "readiness": true, "startup": true}

// metaFormSections 通用资源 section：labels / annotations（metadata 层，与 diffIgnore 一致不纳入字段授权，仅需 update 权限）
var metaFormSections = map[string]bool{"labels": true, "annotations": true}

// 标签/注解键值的 K8s 格式约束（防呆，前端同款规则）
var (
	labelPrefixSegRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
	labelNameRe      = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
	labelValueRe     = regexp.MustCompile(`^([A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)?$`)
)

// validateMetaKV 校验 labels/annotations 表单载荷；返回错误文案（空=通过）
func validateMetaKV(section string, m map[string]any) string {
	for k, v := range m {
		name := k
		if i := strings.LastIndex(k, "/"); i >= 0 {
			prefix, n := k[:i], k[i+1:]
			if prefix == "" || n == "" {
				return fmt.Sprintf("键 %q 的前缀或名称为空", k)
			}
			if len(prefix) > 253 {
				return fmt.Sprintf("键 %q 的前缀超过 253 字符", k)
			}
			for _, seg := range strings.Split(prefix, ".") {
				if !labelPrefixSegRe.MatchString(seg) {
					return fmt.Sprintf("键 %q 的前缀需为 DNS 子域（小写字母/数字/中划线/点）", k)
				}
			}
			name = n
		}
		if name == "" || len(name) > 63 || !labelNameRe.MatchString(name) {
			return fmt.Sprintf("键 %q 不合法（≤63 字符，字母或数字开头结尾，可含 - _ . 与可选域名前缀）", k)
		}
		s, ok := v.(string)
		if !ok {
			return fmt.Sprintf("键 %q 的值必须是字符串", k)
		}
		if section == "labels" {
			if len(s) > 63 || !labelValueRe.MatchString(s) {
				return fmt.Sprintf("标签 %q 的值不合法（≤63 字符，字母或数字开头结尾，可含 - _ .）", k)
			}
		} else if hasControlChars(s) {
			return fmt.Sprintf("注解 %q 的值含控制字符，无法保存", k)
		}
	}
	return ""
}

func hasControlChars(s string) bool {
	for _, r := range s {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return true
		}
	}
	return false
}

// ResourceFormEdit PUT /api/v1/clusters/:cluster/resedit — 表单编辑：按 section 精确更新一个维度
// section 与授权动作一一对应：保存「环境变量」要求 env 动作授权，与 YAML diff 校验同一套字段模式
func (h *H) ResourceFormEdit(c *gin.Context) {
	clusterName := c.Param("cluster")
	// 限制 body 大小（labels/annotations 表单理论上很小；4MB 充裕）
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4*1024*1024)
	var req struct {
		Group     string         `json:"group"`
		Version   string         `json:"version" binding:"required"`
		Resource  string         `json:"resource" binding:"required"`
		Namespace string         `json:"namespace"`
		Name      string         `json:"name" binding:"required"`
		Section   string         `json:"section" binding:"required"`
		Payload   map[string]any `json:"payload"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Payload == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	isService := req.Resource == "services"
	if req.Resource == "pods" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Pod 是运行实例，不支持编辑；请修改所属的应用（Deployment 等）"})
		return
	}
	isWorkload := isBuiltInWorkload(req.Resource)
	isServiceSection := isService && serviceFormSections[req.Section]
	// Ingress 路由规则编辑（spec.rules 整段替换，与目录 routes 动作对应）
	ingressSection := req.Resource == "ingresses" && ingressFormSections[req.Section]
	// ConfigMap / Secret 的数据编辑（data 为顶层字段，与目录 data 动作对应）
	isCMSecret := req.Resource == "configmaps" || req.Resource == "secrets"
	cmSection := isCMSecret && req.Section == "data"
	// PVC 容量扩容（spec.resources.requests.storage，仅能扩大，由 apiserver 校验存储类是否允许）
	pvcSection := req.Resource == "persistentvolumeclaims" && req.Section == "storage"
	// 节点污点编辑（spec.taints 全量覆盖）：权限=nodes 的 update（与 cordon 同口径），不进动作目录
	// ——避免改变「update 令牌=完整更新」的既有授权语义；跳过 ActionAllowed 门（字段 diff 仍兜底）
	isTaints := req.Resource == "nodes" && req.Section == "taints"
	// labels/annotations 任何可 update 资源都允许（CR 走此分支；service/workload 也支持元数据）
	allowMeta := metaFormSections[req.Section]
	if !isServiceSection && !isWorkload && !allowMeta && !cmSection && !pvcSection && !ingressSection && !isTaints {
		c.JSON(http.StatusBadRequest, gin.H{"message": "该资源类型不支持所选编辑模块；CR 请使用 YAML 编辑"})
		return
	}
	if isService && !serviceFormSections[req.Section] && !metaFormSections[req.Section] {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Service 仅支持 ports/selector/labels/annotations"})
		return
	}
	if req.Resource == "ingresses" && !ingressFormSections[req.Section] && !metaFormSections[req.Section] {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Ingress 仅支持 routes/labels/annotations；TLS 与默认后端请使用 YAML 编辑"})
		return
	}
	if isWorkload && !workloadFormSections[req.Section] && !metaFormSections[req.Section] {
		c.JSON(http.StatusBadRequest, gin.H{"message": "工作负载仅支持 image/command/ports/resources/env/probes/storage/scheduling/labels/annotations"})
		return
	}
	e, err := h.mgr.Get(clusterName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	// 鉴权资源 key：CR 用 group/plural 全限定名
	allowedResKey := resKeyForAuth(req.Group, req.Resource)
	if !middleware.Allowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, req.Namespace, "update", allowedResKey) {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "form_edit", clusterName, req.Namespace, req.Resource, req.Name, "权限拒绝", "denied"))
		denyResource403(c, "update", allowedResKey)
		return
	}
	// labels/annotations 属 metadata 层，按平台设计走 update 通道（不映射细粒度动作），
	// update 权限即可；字段级 diff 中它们由 serverManagedMetaFields 忽略（metadata 不再
	// 整体忽略，finalizers 等其他 metadata 字段仍受字段授权管控）。其余 section 与授权动作一一对应。
	// taints 同样跳过端点级动作门：nodes 不进动作目录（目录外资源走 CR 语义），update 已在上方检查，
	// 修改范围由下方 case 的形状校验 + 字段 diff（spec.taints 路径）兜底
	if !metaFormSections[req.Section] && !isTaints {
		if !middleware.ActionAllowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, req.Namespace, allowedResKey, req.Section) {
			audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "form_edit", clusterName, req.Namespace, req.Resource, req.Name,
				"权限拒绝：未授权 "+req.Section+" 动作", "denied"))
			c.JSON(http.StatusForbidden, gin.H{"message": "无「" + middleware.ActionLabel(h.db, req.Resource, req.Section) + "」模块权限"})
			return
		}
	}
	gvr := schema.GroupVersionResource{Group: req.Group, Version: req.Version, Resource: req.Resource}
	ri := dynamicRes(e, gvr, req.Namespace)
	obj, err := ri.Get(c.Request.Context(), req.Name, metav1.GetOptions{})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": "读取对象失败: " + err.Error()})
		return
	}
	before := obj.DeepCopy()

	switch {
	case metaFormSections[req.Section]:
		// labels / annotations：全量覆盖 metadata 对应子表（前端行编辑后提交的就是完整表）
		m, ok := req.Payload[req.Section].(map[string]any)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"message": "payload." + req.Section + " 需为对象"})
			return
		}
		if msg := validateMetaKV(req.Section, m); msg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"message": msg})
			return
		}
		if len(m) == 0 {
			// 全删空表：移除字段本身，避免 metadata 下残留 {}
			if meta, ok := obj.Object["metadata"].(map[string]any); ok {
				delete(meta, req.Section)
			}
		} else {
			_ = unstructured.SetNestedField(obj.Object, m, "metadata", req.Section)
		}
	case isService:
		switch req.Section {
		case "ports":
			_ = unstructured.SetNestedField(obj.Object, req.Payload["ports"], "spec", "ports")
		case "selector":
			_ = unstructured.SetNestedField(obj.Object, req.Payload["selector"], "spec", "selector")
		}
	case cmSection:
		// ConfigMap / Secret 数据：data 为顶层字段，全量覆盖；secret 保存后清掉 stringData 避免旧残留覆盖 data
		m, ok := req.Payload["data"].(map[string]any)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"message": "payload.data 需为对象"})
			return
		}
		for k, v := range m {
			if k == "" || len(k) > 253 || !dataKeyRe.MatchString(k) {
				c.JSON(http.StatusBadRequest, gin.H{"message": "数据键 " + k + " 不合法（字母/数字/中划线/点/下划线）"})
				return
			}
			if _, ok := v.(string); !ok {
				c.JSON(http.StatusBadRequest, gin.H{"message": "数据键 " + k + " 的值需为字符串"})
				return
			}
		}
		if len(m) == 0 {
			delete(obj.Object, "data")
		} else {
			obj.Object["data"] = m
		}
		if req.Resource == "secrets" {
			delete(obj.Object, "stringData")
		}
	case pvcSection:
		// PVC 容量扩容：只能扩大由 apiserver / 存储类（allowVolumeExpansion）校验，这里只管写入
		size, ok := req.Payload["storage"].(string)
		size = strings.TrimSpace(size)
		if !ok || size == "" {
			c.JSON(http.StatusBadRequest, gin.H{"message": "payload.storage 需为容量字符串，如 20Gi"})
			return
		}
		_ = unstructured.SetNestedField(obj.Object, size, "spec", "resources", "requests", "storage")
	case req.Section == "storage":
		// 存储挂载：前端算好最终形态（新卷描述 + 原样保留卷 + 各容器挂载），后端校验后整段替换。
		// 白名单外的卷类型（hostPath/projected 等）只能「保留现有」，不能经表单新增——防越权挂载宿主路径
		podSpec := workloadPodSpecPath(req.Resource)
		rawV, _ := json.Marshal(req.Payload["volumes"])
		var vols []formVolDesc
		if err := json.Unmarshal(rawV, &vols); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": "payload.volumes 解析失败"})
			return
		}
		rawKV, _ := json.Marshal(req.Payload["keepVolumes"])
		var keepVols []map[string]any
		_ = json.Unmarshal(rawKV, &keepVols)
		rawC, _ := json.Marshal(req.Payload["containers"])
		var edits []formContainerEdit
		if err := json.Unmarshal(rawC, &edits); err != nil || len(edits) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"message": "payload.containers 解析失败"})
			return
		}
		existingVols, _, _ := unstructured.NestedSlice(obj.Object, append(podSpec, "volumes")...)

		// 卷描述校验与重建（白名单形状：pvc / configmap / secret / emptyDir）
		nameSet := map[string]bool{}
		newVols := make([]any, 0, len(vols)+len(keepVols))
		for _, v := range vols {
			v.Name = strings.TrimSpace(v.Name)
			v.RefName = strings.TrimSpace(v.RefName)
			if v.Name == "" || len(v.Name) > 63 || !dnsLabelRe.MatchString(v.Name) {
				c.JSON(http.StatusBadRequest, gin.H{"message": "卷名 " + v.Name + " 需为 DNS 名（小写字母/数字/中划线，≤63 字符）"})
				return
			}
			if nameSet[v.Name] {
				c.JSON(http.StatusBadRequest, gin.H{"message": "卷名 " + v.Name + " 重复"})
				return
			}
			if v.Type != "emptydir" && (v.RefName == "" || len(v.RefName) > 253 || !dnsLabelRe.MatchString(v.RefName)) {
				c.JSON(http.StatusBadRequest, gin.H{"message": "卷 " + v.Name + " 的引用对象名不合法（需 DNS 名，如 my-pvc）"})
				return
			}
			nameSet[v.Name] = true
			newVols = append(newVols, buildVolume(v))
		}
		// 保留卷：必须与现有卷完全一致（前端回传的 hostPath/projected 等），防止借道注入
		for _, kv := range keepVols {
			if !sameAsExisting(kv, existingVols) {
				c.JSON(http.StatusBadRequest, gin.H{"message": "保留卷与现有配置不一致，疑似越权注入；请使用 YAML 编辑"})
				return
			}
			if n, _ := kv["name"].(string); n != "" {
				if nameSet[n] {
					c.JSON(http.StatusBadRequest, gin.H{"message": "卷名 " + n + " 重复"})
					return
				}
				nameSet[n] = true
			}
			newVols = append(newVols, kv)
		}

		containers, _, _ := unstructured.NestedSlice(obj.Object, append(podSpec, "containers")...)
		for _, ce := range edits {
			var target map[string]any
			for _, cm := range containers {
				if m, ok := cm.(map[string]any); ok && m["name"] == ce.Name {
					target = m
					break
				}
			}
			if target == nil {
				c.JSON(http.StatusBadRequest, gin.H{"message": "容器 " + ce.Name + " 不存在"})
				return
			}
			existingMounts, _, _ := unstructured.NestedSlice(target, "volumeMounts")
			mounts := make([]any, 0, len(ce.Mounts)+len(ce.KeepMount))
			paths := map[string]bool{}
			for _, m := range ce.Mounts {
				if m.MountPath == "" || !strings.HasPrefix(m.MountPath, "/") {
					c.JSON(http.StatusBadRequest, gin.H{"message": "容器 " + ce.Name + " 的挂载路径需以 / 开头"})
					return
				}
				if paths[m.MountPath] {
					c.JSON(http.StatusBadRequest, gin.H{"message": "容器 " + ce.Name + " 挂载路径 " + m.MountPath + " 重复"})
					return
				}
				if !nameSet[m.Name] {
					c.JSON(http.StatusBadRequest, gin.H{"message": "容器 " + ce.Name + " 的挂载引用了不存在的卷 " + m.Name})
					return
				}
				if m.SubPath != "" && (strings.HasPrefix(m.SubPath, "/") || strings.Contains(m.SubPath, "..")) {
					c.JSON(http.StatusBadRequest, gin.H{"message": "容器 " + ce.Name + " 的子路径不能以 / 开头或包含 .."})
					return
				}
				paths[m.MountPath] = true
				mounts = append(mounts, volMountObj(m))
			}
			for _, km := range ce.KeepMount {
				if !sameAsExisting(km, existingMounts) {
					c.JSON(http.StatusBadRequest, gin.H{"message": "容器 " + ce.Name + " 的保留挂载与现有配置不一致，疑似越权注入；请使用 YAML 编辑"})
					return
				}
				if mp, _ := km["mountPath"].(string); mp != "" {
					if paths[mp] {
						c.JSON(http.StatusBadRequest, gin.H{"message": "容器 " + ce.Name + " 挂载路径 " + mp + " 重复"})
						return
					}
					paths[mp] = true
				}
				mounts = append(mounts, km)
			}
			if len(mounts) == 0 {
				delete(target, "volumeMounts")
			} else {
				target["volumeMounts"] = mounts
			}
		}
		_ = unstructured.SetNestedSlice(obj.Object, containers, append(podSpec, "containers")...)
		if len(newVols) == 0 {
			unstructured.RemoveNestedField(obj.Object, append(podSpec, "volumes")...)
		} else {
			_ = unstructured.SetNestedSlice(obj.Object, newVols, append(podSpec, "volumes")...)
		}
	case ingressSection:
		// Ingress 路由规则：校验行后整段重建 spec.rules（同 host 相邻归组为一条 rule）。
		// 只管 rules——spec.tls / defaultBackend / ingressClassName 不经表单（防越权面扩大），走 YAML 编辑
		rawV, hasRoutes := req.Payload["routes"]
		if !hasRoutes || rawV == nil {
			// routes 键缺失/为 null 视为非法请求拒绝——空数组（清空全部路由）才是合法的显式意图
			c.JSON(http.StatusBadRequest, gin.H{"message": "payload.routes 不能为空（清空全部路由请提交空数组）"})
			return
		}
		rawR, _ := json.Marshal(rawV)
		var rows []formIngRoute
		if err := json.Unmarshal(rawR, &rows); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": "payload.routes 解析失败"})
			return
		}
		var rules []any
		var curRule map[string]any
		seen := map[string]bool{}
		for i, r := range rows {
			r.Host = strings.TrimSpace(r.Host)
			r.Path = strings.TrimSpace(r.Path)
			r.PathType = strings.TrimSpace(r.PathType)
			r.Svc = strings.TrimSpace(r.Svc)
			portStr := r.portString()
			if r.Path == "" {
				c.JSON(http.StatusBadRequest, gin.H{"message": fmt.Sprintf("第 %d 行的路径不能为空", i+1)})
				return
			}
			if !strings.HasPrefix(r.Path, "/") {
				c.JSON(http.StatusBadRequest, gin.H{"message": fmt.Sprintf("第 %d 行的路径 %s 需以 / 开头", i+1, r.Path)})
				return
			}
			if r.PathType == "" {
				r.PathType = "Prefix"
			}
			if r.PathType != "Prefix" && r.PathType != "Exact" && r.PathType != "ImplementationSpecific" {
				c.JSON(http.StatusBadRequest, gin.H{"message": fmt.Sprintf("第 %d 行的匹配类型 %s 不合法（Prefix/Exact/ImplementationSpecific）", i+1, r.PathType)})
				return
			}
			if r.Svc == "" || len(r.Svc) > 63 || !dnsLabelRe.MatchString(r.Svc) {
				c.JSON(http.StatusBadRequest, gin.H{"message": fmt.Sprintf("第 %d 行的后端服务名不合法（需 DNS 名，如 my-svc）", i+1)})
				return
			}
			var portObj map[string]any
			if portStr == "" {
				c.JSON(http.StatusBadRequest, gin.H{"message": fmt.Sprintf("第 %d 行的端口不能为空", i+1)})
				return
			}
			if n, err := strconv.Atoi(portStr); err == nil {
				if n < 1 || n > 65535 {
					c.JSON(http.StatusBadRequest, gin.H{"message": fmt.Sprintf("第 %d 行的端口需为 1-65535", i+1)})
					return
				}
				// 必须 int64：unstructured 深拷贝只认 JSON 类型，原生 int 会 panic（cannot deep copy int）
				portObj = map[string]any{"number": int64(n)}
			} else {
				if len(portStr) > 63 || !dnsLabelRe.MatchString(portStr) {
					c.JSON(http.StatusBadRequest, gin.H{"message": fmt.Sprintf("第 %d 行的端口需为 1-65535 数字或端口名", i+1)})
					return
				}
				portObj = map[string]any{"name": portStr}
			}
			key := r.Host + "|" + r.Path
			if seen[key] {
				c.JSON(http.StatusBadRequest, gin.H{"message": fmt.Sprintf("路由 %s%s 重复", r.Host, r.Path)})
				return
			}
			seen[key] = true
			// host 与上一行不同 → 开新 rule；同 host 相邻行归入同一条 rule 的 paths
			if curRule == nil || hostOfRule(curRule) != r.Host {
				curRule = map[string]any{}
				if r.Host != "" {
					curRule["host"] = r.Host
				}
				curRule["http"] = map[string]any{"paths": []any{}}
				rules = append(rules, curRule)
			}
			httpMap, _ := curRule["http"].(map[string]any)
			paths, _ := httpMap["paths"].([]any)
			paths = append(paths, map[string]any{
				"path":     r.Path,
				"pathType": r.PathType,
				"backend": map[string]any{
					"service": map[string]any{
						"name": r.Svc,
						"port": portObj,
					},
				},
			})
			httpMap["paths"] = paths
		}
		if len(rules) == 0 {
			unstructured.RemoveNestedField(obj.Object, "spec", "rules")
		} else {
			_ = unstructured.SetNestedSlice(obj.Object, rules, "spec", "rules")
		}
	case isTaints:
		// 节点污点：前端行编辑后提交完整污点表，全量覆盖 spec.taints（空 = 清除全部污点）。
		// 系统管理型污点（kubelet/云控制器维护）不允许经表单增删改——动了会被控制器立刻改回甚至打乱就绪判定
		raw, _ := json.Marshal(req.Payload["taints"])
		var rows []formTaint
		if err := json.Unmarshal(raw, &rows); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": "payload.taints 解析失败"})
			return
		}
		seen := map[string]bool{}
		taints := make([]any, 0, len(rows))
		for i, t := range rows {
			t.Key = strings.TrimSpace(t.Key)
			t.Value = strings.TrimSpace(t.Value)
			t.Effect = strings.TrimSpace(t.Effect)
			if msg := validateTaint(i+1, t); msg != "" {
				c.JSON(http.StatusBadRequest, gin.H{"message": msg})
				return
			}
			if seen[t.Key+"/"+t.Effect] {
				c.JSON(http.StatusBadRequest, gin.H{"message": fmt.Sprintf("第 %d 行污点 %s/%s 重复", i+1, t.Key, t.Effect)})
				return
			}
			seen[t.Key+"/"+t.Effect] = true
			taints = append(taints, taintObj(t))
		}
		// 系统管理型污点只能原样保留（保存为全量覆盖，必须对照现有值防删改）：
		// 提交行里的系统污点必须与节点现有完全一致（防伪造/篡改），现有系统污点必须出现在提交中（防删除）
		curTaints, _, _ := unstructured.NestedSlice(obj.Object, "spec", "taints")
		type sysSig struct{ key, value, effect string }
		var curSys []sysSig
		for _, cv := range curTaints {
			ct, ok := cv.(map[string]any)
			if !ok {
				continue
			}
			k, _ := ct["key"].(string)
			if !systemTaintKeys[k] {
				continue
			}
			v, _ := ct["value"].(string)
			e, _ := ct["effect"].(string)
			curSys = append(curSys, sysSig{k, v, e})
		}
		sigOf := func(s sysSig) string { return s.key + "|" + s.value + "|" + s.effect }
		submittedSys := map[string]bool{}
		for i, t := range rows {
			if !systemTaintKeys[t.Key] {
				continue
			}
			match := false
			for _, s := range curSys {
				if sigOf(s) == sigOf(sysSig{t.Key, t.Value, t.Effect}) {
					match = true
					break
				}
			}
			if !match {
				c.JSON(http.StatusBadRequest, gin.H{"message": fmt.Sprintf("第 %d 行污点 %s 为系统管理型（kubelet/控制器维护），不能新增或修改，只能原样保留", i+1, t.Key)})
				return
			}
			submittedSys[sigOf(sysSig{t.Key, t.Value, t.Effect})] = true
		}
		for _, s := range curSys {
			if !submittedSys[sigOf(s)] {
				c.JSON(http.StatusBadRequest, gin.H{"message": fmt.Sprintf("系统管理型污点 %s（%s）由 kubelet/控制器维护，不能删除，请保留该行", s.key, s.effect)})
				return
			}
		}
		if len(taints) == 0 {
			unstructured.RemoveNestedField(obj.Object, "spec", "taints")
		} else {
			_ = unstructured.SetNestedSlice(obj.Object, taints, "spec", "taints")
		}
	case req.Section == "scheduling":
		// 调度策略：建模实用子集（nodeSelector / nodeName / 节点亲和 / Pod 反亲和），
		// 遇到建模不了的现有亲和配置（多 term / matchFields / podAffinity 等）明确拒绝——
		// 比静默合并安全：全有全无，用户转 YAML 编辑
		podSpec := workloadPodSpecPath(req.Resource)
		if err := applyScheduling(obj, podSpec, req.Payload); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}
	default:
		raw, _ := json.Marshal(req.Payload["containers"])
		var edits []formContainerEdit
		if err := json.Unmarshal(raw, &edits); err != nil || len(edits) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"message": "payload.containers 解析失败"})
			return
		}
		podSpec := workloadPodSpecPath(req.Resource)
		containers, _, _ := unstructured.NestedSlice(obj.Object, append(podSpec, "containers")...)
		for _, ce := range edits {
			if req.Section == "image" && ce.Image == "" {
				c.JSON(http.StatusBadRequest, gin.H{"message": "容器 " + ce.Name + " 镜像不能为空"})
				return
			}
			var target map[string]any
			for _, cm := range containers {
				if m, ok := cm.(map[string]any); ok && m["name"] == ce.Name {
					target = m
					break
				}
			}
			if target == nil {
				c.JSON(http.StatusBadRequest, gin.H{"message": "容器 " + ce.Name + " 不存在"})
				return
			}
			switch req.Section {
			case "image":
				target["image"] = ce.Image
			case "command":
				// 空数组 = 清除该字段（用镜像默认），与 K8s JSON 语义一致；
				// 须转 []any：unstructured 深拷贝只认 JSON 类型，带类型的 []string 会 panic
				if len(ce.Command) == 0 {
					delete(target, "command")
				} else {
					target["command"] = strsToAny(ce.Command)
				}
				if len(ce.Args) == 0 {
					delete(target, "args")
				} else {
					target["args"] = strsToAny(ce.Args)
				}
			case "ports":
				target["ports"] = mapsToAny(ce.Ports)
			case "resources":
				target["resources"] = ce.Resources
			case "env":
				target["env"] = mapsToAny(ce.Env)
			case "probes":
				// 健康检查：白名单重建探针对象；probe=null = 删除该探针；
				// 未提及的 kind 保持原样（前端每次提交完整三探针意图，缺省即不动——但删除语义由显式 null 表达）
				for _, pe := range ce.Probes {
					if !probeKinds[pe.Kind] {
						c.JSON(http.StatusBadRequest, gin.H{"message": "探针类型 " + pe.Kind + " 不合法"})
						return
					}
					key := pe.Kind + "Probe"
					if pe.Probe == nil {
						delete(target, key)
						continue
					}
					np, msg := buildProbeObj(pe.Probe)
					if msg != "" {
						c.JSON(http.StatusBadRequest, gin.H{"message": "容器 " + ce.Name + " 的" + msg})
						return
					}
					target[key] = np
				}
			}
		}
		_ = unstructured.SetNestedSlice(obj.Object, containers, append(podSpec, "containers")...)
	}

	// 纵深防御：section 实现若有漏洞，仍由字段 diff 兜底
	if patterns, all := middleware.AllowedUpdateFields(h.db, middleware.Role(c), middleware.UID(c), clusterName, req.Namespace, allowedResKey); !all {
		if bad := middleware.FieldViolations(before.Object, obj.Object, patterns); len(bad) > 0 {
			audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "form_edit", clusterName, req.Namespace, req.Resource, req.Name,
				"越权字段: "+strings.Join(bad, ", "), "denied"))
			c.JSON(http.StatusForbidden, gin.H{"message": "无权修改字段: " + strings.Join(bad, ", ")})
			return
		}
	}
	updated, err := ri.Update(c.Request.Context(), obj, metav1.UpdateOptions{})
	if err != nil {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "form_edit", clusterName, req.Namespace, req.Resource, req.Name,
			"section="+req.Section+" 失败: "+err.Error(), "failed"))
		c.JSON(http.StatusBadGateway, gin.H{"message": "保存失败: " + err.Error()})
		return
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "form_edit", clusterName, req.Namespace, req.Resource, req.Name,
		"表单编辑 "+formEditDiff(req.Resource, isService, req.Section, before.Object, updated.Object), "success"))
	stripManagedFields(updated.Object)
	c.JSON(http.StatusOK, updated.Object)
}

// ---------- 启动 / 停止（平台侧实现：K8s 无原生启停，注解保存原副本数）----------

const stopAnnotation = "kubeyard.io/original-replicas"

func scalableResource(resource string) bool {
	return resource == "deployments" || resource == "statefulsets"
}

// currentReplicas 读 spec.replicas，缺省按 1（API Server 默认值）
func currentReplicas(obj *unstructured.Unstructured) int64 {
	n, ok, _ := unstructured.NestedInt64(obj.Object, "spec", "replicas")
	if !ok || n < 0 {
		return 1
	}
	return n
}

// Stop POST /api/v1/clusters/:cluster/stop — 停止：注解记录原副本数并缩容为 0；启动时原样恢复
func (h *H) Stop(c *gin.Context) {
	clusterName := c.Param("cluster")
	var req struct {
		Group     string `json:"group"`
		Version   string `json:"version" binding:"required"`
		Resource  string `json:"resource" binding:"required"`
		Namespace string `json:"namespace"`
		Name      string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	if !scalableResource(req.Resource) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "仅 Deployment / StatefulSet 支持启停"})
		return
	}
	e, err := h.mgr.Get(clusterName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	allowedResKey := resKeyForAuth(req.Group, req.Resource)
	if !middleware.Allowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, req.Namespace, "update", allowedResKey) {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "stop", clusterName, req.Namespace, req.Resource, req.Name, "权限拒绝", "denied"))
		denyResource403(c, "update", allowedResKey)
		return
	}
	if !middleware.ActionAllowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, req.Namespace, allowedResKey, "stop") {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "stop", clusterName, req.Namespace, req.Resource, req.Name, "权限拒绝：未授权停止动作", "denied"))
		c.JSON(http.StatusForbidden, gin.H{"message": "无停止权限"})
		return
	}
	gvr := schema.GroupVersionResource{Group: req.Group, Version: req.Version, Resource: req.Resource}
	ri := dynamicRes(e, gvr, req.Namespace)
	obj, err := ri.Get(c.Request.Context(), req.Name, metav1.GetOptions{})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": err.Error()})
		return
	}
	ann, _, _ := unstructured.NestedString(obj.Object, "metadata", "annotations", stopAnnotation)
	if ann != "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "当前已处于停止状态"})
		return
	}
	replicas := currentReplicas(obj)
	if replicas <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "当前副本数为 0，无需停止"})
		return
	}
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"%s":"%d"}},"spec":{"replicas":0}}`, stopAnnotation, replicas)
	if _, err := ri.Patch(c.Request.Context(), req.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "stop", clusterName, req.Namespace, req.Resource, req.Name, err.Error(), "failed"))
		c.JSON(http.StatusBadGateway, gin.H{"message": "停止失败: " + err.Error()})
		return
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "stop", clusterName, req.Namespace, req.Resource, req.Name,
		fmt.Sprintf("停止（原副本数 %d，启动时恢复）", replicas), "success"))
	c.JSON(http.StatusOK, gin.H{"message": "已停止，副本数已记录，启动时恢复"})
}

// Start POST /api/v1/clusters/:cluster/start — 启动：恢复注解中保存的副本数
func (h *H) Start(c *gin.Context) {
	clusterName := c.Param("cluster")
	var req struct {
		Group     string `json:"group"`
		Version   string `json:"version" binding:"required"`
		Resource  string `json:"resource" binding:"required"`
		Namespace string `json:"namespace"`
		Name      string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	if !scalableResource(req.Resource) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "仅 Deployment / StatefulSet 支持启停"})
		return
	}
	e, err := h.mgr.Get(clusterName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	allowedResKey := resKeyForAuth(req.Group, req.Resource)
	if !middleware.Allowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, req.Namespace, "update", allowedResKey) {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "start", clusterName, req.Namespace, req.Resource, req.Name, "权限拒绝", "denied"))
		denyResource403(c, "update", allowedResKey)
		return
	}
	if !middleware.ActionAllowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, req.Namespace, allowedResKey, "start") {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "start", clusterName, req.Namespace, req.Resource, req.Name, "权限拒绝：未授权启动动作", "denied"))
		c.JSON(http.StatusForbidden, gin.H{"message": "无启动权限"})
		return
	}
	gvr := schema.GroupVersionResource{Group: req.Group, Version: req.Version, Resource: req.Resource}
	ri := dynamicRes(e, gvr, req.Namespace)
	obj, err := ri.Get(c.Request.Context(), req.Name, metav1.GetOptions{})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": err.Error()})
		return
	}
	ann, _, _ := unstructured.NestedString(obj.Object, "metadata", "annotations", stopAnnotation)
	if ann == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "当前未处于停止状态"})
		return
	}
	n, err := strconv.ParseInt(ann, 10, 64)
	if err != nil || n < 0 {
		n = 1
	}
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"%s":null}},"spec":{"replicas":%d}}`, stopAnnotation, n)
	if _, err := ri.Patch(c.Request.Context(), req.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "start", clusterName, req.Namespace, req.Resource, req.Name, err.Error(), "failed"))
		c.JSON(http.StatusBadGateway, gin.H{"message": "启动失败: " + err.Error()})
		return
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "start", clusterName, req.Namespace, req.Resource, req.Name,
		fmt.Sprintf("启动（恢复副本数 %d）", n), "success"))
	c.JSON(http.StatusOK, gin.H{"message": "已启动"})
}

// ---------- 事件 ----------

// ListEvents GET /api/v1/clusters/:cluster/events — 读数据库（30s 轮询采集，保留 7 天），按时间倒序
func (h *H) ListEvents(c *gin.Context) {
	clusterName := c.Param("cluster")
	if _, err := h.mgr.Get(clusterName); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	nsParam := c.Query("namespace")
	search := c.Query("search")
	q := h.db.Model(&model.ClusterEvent{}).Where("cluster = ?", clusterName)
	if nsParam != "" {
		q = q.Where("namespace = ?", nsParam)
	}
	if search != "" {
		like := "%" + search + "%"
		q = q.Where("object_name LIKE ? OR message LIKE ? OR reason LIKE ?", like, like, like)
	}
	// 可见命名空间过滤压进查询（在 Limit 之前）：否则先全局取 500 条再逐行过滤，
	// 范围受限用户会莫名少数据；集群级对象（namespace 为空）保留待逐行按 list 授权判定
	visible, all := middleware.VisibleNamespaces(h.db, middleware.Role(c), middleware.UID(c), clusterName)
	if !all {
		q = q.Where("namespace IN ? OR namespace = ''", visible)
	}
	var rows []model.ClusterEvent
	q.Order("last_time DESC").Limit(500).Find(&rows)
	role, uid := middleware.Role(c), middleware.UID(c)
	out := []map[string]any{}
	for _, r := range rows {
		if !all && !containsStr(visible, r.Namespace) {
			// 集群级对象（Node/Namespace/PV 等，namespace 为空）的事件：按该资源的 list 授权放行
			// （Kind→plural 简单加 s：Node→nodes、PersistentVolume→persistentvolumes 均成立）
			if r.Namespace != "" || !middleware.Allowed(h.db, role, uid, clusterName, "", "list", strings.ToLower(r.ObjectType)+"s") {
				continue
			}
		}
		out = append(out, map[string]any{
			"cluster":     r.Cluster,
			"namespace":   r.Namespace,
			"type":        r.Type,
			"reason":      r.Reason,
			"object":      r.ObjectType + "/" + r.ObjectName,
			"object_type": r.ObjectType, // AI 诊断等需要 Kind 与名称分开传参
			"object_name": r.ObjectName,
			"message":     r.Message,
			"count":       r.Count,
			"last_time":   r.LastTime,
		})
	}
	c.JSON(http.StatusOK, out)
}

// ---------- 工具 ----------

func getStr(m map[string]any, path ...string) string {
	s, _, _ := unstructured.NestedString(m, path...)
	return s
}

// matchSearch 资源名 + 资源特定字段（Pod IP/节点、Node IP/命名空间/状态）做大小写不敏感子串匹配
func matchSearch(resource string, m map[string]any, q string) bool {
	if strings.Contains(strings.ToLower(getStr(m, "metadata", "name")), strings.ToLower(q)) {
		return true
	}
	if resource == "ingresses" {
		// 路由字段：host（主要诉求）/ path / 后端服务名 / TLS 域名——子串、忽略大小写
		lq := strings.ToLower(q)
		if rules, _, _ := unstructured.NestedSlice(m, "spec", "rules"); rules != nil {
			for _, r := range rules {
				rm, ok := r.(map[string]any)
				if !ok {
					continue
				}
				if h, _ := rm["host"].(string); h != "" && strings.Contains(strings.ToLower(h), lq) {
					return true
				}
				paths, _, _ := unstructured.NestedSlice(rm, "http", "paths")
				for _, p := range paths {
					pm, ok := p.(map[string]any)
					if !ok {
						continue
					}
					if pt, _ := pm["path"].(string); pt != "" && strings.Contains(strings.ToLower(pt), lq) {
						return true
					}
					if svc, _, _ := unstructured.NestedString(pm, "backend", "service", "name"); svc != "" && strings.Contains(strings.ToLower(svc), lq) {
						return true
					}
				}
			}
		}
		if tls, _, _ := unstructured.NestedSlice(m, "spec", "tls"); tls != nil {
			for _, t := range tls {
				tm, ok := t.(map[string]any)
				if !ok {
					continue
				}
				hosts, _, _ := unstructured.NestedStringSlice(tm, "hosts")
				for _, h := range hosts {
					if strings.Contains(strings.ToLower(h), lq) {
						return true
					}
				}
			}
		}
	}
	if resource == "nodes" {
		// 节点 IP：status.addresses[].InternalIP（列表主显示名为 IP，用户按 IP 搜索的诉求最自然）
		if addrs, _, _ := unstructured.NestedSlice(m, "status", "addresses"); addrs != nil {
			lq := strings.ToLower(q)
			for _, a := range addrs {
				am, ok := a.(map[string]any)
				if !ok {
					continue
				}
				if t, _ := am["type"].(string); t != "InternalIP" {
					continue
				}
				if ip, _ := am["address"].(string); ip != "" && strings.Contains(strings.ToLower(ip), lq) {
					return true
				}
			}
		}
	}
	if resource == "pods" {
		lq := strings.ToLower(q)
		// Pod IP：status.podIP（主 IP）+ status.podIPs（多网络，含 ipv4/ipv6）；用户输入 "10.0.1" 也能命中
		// IP 字段内可能有逗号分隔多 IP，逐个判
		if ip := getStr(m, "status", "podIP"); ip != "" {
			for _, s := range strings.Split(ip, ",") {
				if strings.Contains(strings.ToLower(strings.TrimSpace(s)), lq) {
					return true
				}
			}
		}
		// 各容器 status：已分配的 IP + waiting 原因（CrashLoopBackOff / ImagePullBackOff…——
		// 容器异常等待时 phase 仍 Running，waiting 原因是搜异常词的唯一入口）
		for _, key := range []string{"containerStatuses", "initContainerStatuses"} {
			if cs, _, _ := unstructured.NestedSlice(m, "status", key); cs != nil {
				for _, c := range cs {
					if cm, ok := c.(map[string]any); ok {
						if ip, _ := cm["ip"].(string); ip != "" && strings.Contains(strings.ToLower(ip), lq) {
							return true
						}
						if w, _, _ := unstructured.NestedString(cm, "state", "waiting", "reason"); w != "" && strings.Contains(strings.ToLower(w), lq) {
							return true
						}
					}
				}
			}
		}
		// status.reason：Evicted 等调度失败标记
		if strings.Contains(strings.ToLower(getStr(m, "status", "reason")), lq) {
			return true
		}
		// 节点名 / 节点 IP / 命名空间 / 状态 兜底
		if strings.Contains(strings.ToLower(getStr(m, "spec", "nodeName")), lq) {
			return true
		}
		if ip := getStr(m, "status", "hostIP"); ip != "" && strings.Contains(strings.ToLower(ip), lq) {
			return true
		}
		if strings.Contains(strings.ToLower(getStr(m, "metadata", "namespace")), lq) {
			return true
		}
		if strings.Contains(strings.ToLower(getStr(m, "status", "phase")), lq) {
			return true
		}
		// Pod IP 含点号时用户可能搜 "10.0.1.5" 或 "10.0.1"（前缀）——都按子串已能命中
	}
	return false
}

func firstNonZero(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// stripManagedFields 去除 metadata.managedFields（体积大且含内部信息）
func stripManagedFields(m map[string]any) {
	if meta, ok := m["metadata"].(map[string]any); ok {
		delete(meta, "managedFields")
	}
}

// formEditDiff 表单编辑前后差异（审计详情用）：按 section 提取关心的字段，拼成紧凑摘要
func formEditDiff(resource string, isService bool, section string, before, after map[string]any) string {
	var lines []string
	if section == "labels" || section == "annotations" {
		if section == "annotations" {
			lines = append(lines, metadataKeyDiff(nested(before, "metadata", section), nested(after, "metadata", section))...)
		} else {
			bj, _ := json.Marshal(nested(before, "metadata", section))
			aj, _ := json.Marshal(nested(after, "metadata", section))
			if string(bj) != string(aj) {
				lines = append(lines, fmt.Sprintf("标签: %s → %s", string(bj), string(aj)))
			}
		}
		return metaSectionDiff(section, lines)
	}
	if section == "taints" {
		return auditValueDiff("污点", nested(before, "spec", "taints"), nested(after, "spec", "taints"))
	}
	if section == "scheduling" {
		path := workloadPodSpecPath(resource)
		return auditValueDiff("调度策略", schedulingAuditFields(before, path), schedulingAuditFields(after, path))
	}
	if section == "storage" {
		if resource == "persistentvolumeclaims" {
			return auditValueDiff("PVC 容量", nested(before, "spec", "resources", "requests", "storage"),
				nested(after, "spec", "resources", "requests", "storage"))
		}
		path := workloadPodSpecPath(resource)
		return auditValueDiff("存储挂载", storageAuditFields(before, path), storageAuditFields(after, path))
	}
	if section == "data" {
		return dataKeyDiff(nested(before, "data"), nested(after, "data"))
	}
	if section == "routes" {
		// Ingress 路由：spec.rules 整段对比（规则数可能变化，前后全量展开）
		bj, _ := json.Marshal(nested(before, "spec", "rules"))
		aj, _ := json.Marshal(nested(after, "spec", "rules"))
		if string(bj) != string(aj) {
			lines = append(lines, fmt.Sprintf("路由规则: %s → %s", string(bj), string(aj)))
		}
		return metaSectionDiff(section, lines)
	}
	if isService {
		// Service：ports / selector 都在 spec 下
		var field string
		if section == "ports" {
			field = "服务端口"
		} else {
			field = "选择器"
		}
		bj, _ := json.Marshal(nested(before, "spec", section))
		aj, _ := json.Marshal(nested(after, "spec", section))
		if string(bj) != string(aj) {
			lines = append(lines, fmt.Sprintf("%s: %s → %s", field, string(bj), string(aj)))
		}
	} else {
		cBefore, _ := toContainerMap(before)
		cAfter, _ := toContainerMap(after)
		for name, ac := range cAfter {
			bc := cBefore[name]
			if bc == nil {
				continue
			}
			switch section {
			case "image":
				if bi, _ := bc["image"].(string); bi != ac["image"] {
					lines = append(lines, fmt.Sprintf("容器 %s 镜像: %s → %s", name, bi, ac["image"]))
				}
			case "command":
				if jsonString(bc["command"]) != jsonString(ac["command"]) || jsonString(bc["args"]) != jsonString(ac["args"]) {
					lines = append(lines, fmt.Sprintf("容器 %s 入口/命令: entrypoint=%s cmd=%s → entrypoint=%s cmd=%s",
						name, jsonString(bc["command"]), jsonString(bc["args"]), jsonString(ac["command"]), jsonString(ac["args"])))
				}
			case "ports":
				bj, _ := json.Marshal(bc["ports"])
				aj, _ := json.Marshal(ac["ports"])
				if string(bj) != string(aj) {
					lines = append(lines, fmt.Sprintf("容器 %s 端口: %s → %s", name, string(bj), string(aj)))
				}
			case "resources":
				bj, _ := json.Marshal(bc["resources"])
				aj, _ := json.Marshal(ac["resources"])
				if string(bj) != string(aj) {
					lines = append(lines, fmt.Sprintf("容器 %s 资源: %s → %s", name, string(bj), string(aj)))
				}
			case "env":
				bj, _ := json.Marshal(bc["env"])
				aj, _ := json.Marshal(ac["env"])
				if string(bj) != string(aj) {
					lines = append(lines, fmt.Sprintf("容器 %s 环境变量: %s → %s", name, string(bj), string(aj)))
				}
			case "probes":
				for _, pk := range []string{"livenessProbe", "readinessProbe", "startupProbe"} {
					bj, _ := json.Marshal(bc[pk])
					aj, _ := json.Marshal(ac[pk])
					if string(bj) != string(aj) {
						lines = append(lines, fmt.Sprintf("容器 %s 探针 %s: %s → %s", name, pk, string(bj), string(aj)))
					}
				}
			}
		}
	}
	if len(lines) == 0 {
		return section + "（前后无差异）"
	}
	return section + ":\n - " + strings.Join(lines, "\n - ")
}

func auditValueDiff(label string, before, after any) string {
	bj, _ := json.Marshal(before)
	aj, _ := json.Marshal(after)
	if string(bj) == string(aj) {
		return label + "（前后无差异）"
	}
	return fmt.Sprintf("%s: %s → %s", label, string(bj), string(aj))
}

func schedulingAuditFields(obj map[string]any, podSpec []string) map[string]any {
	fields := map[string]any{}
	for _, field := range []string{"nodeName", "nodeSelector", "affinity"} {
		path := append(append([]string(nil), podSpec...), field)
		if value := nested(obj, path...); value != nil {
			fields[field] = value
		}
	}
	return fields
}

func storageAuditFields(obj map[string]any, podSpec []string) map[string]any {
	fields := map[string]any{}
	if volumes := nested(obj, append(append([]string(nil), podSpec...), "volumes")...); volumes != nil {
		fields["volumes"] = volumes
	}
	if containers, ok := nested(obj, append(append([]string(nil), podSpec...), "containers")...).([]any); ok {
		mounts := make([]map[string]any, 0, len(containers))
		for _, container := range containers {
			c, ok := container.(map[string]any)
			if !ok {
				continue
			}
			name, _ := c["name"].(string)
			mounts = append(mounts, map[string]any{"name": name, "volumeMounts": c["volumeMounts"]})
		}
		fields["containers"] = mounts
	}
	return fields
}

func metadataKeyDiff(before, after any) []string {
	b, _ := before.(map[string]any)
	a, _ := after.(map[string]any)
	keys := map[string]bool{}
	for key := range b {
		keys[key] = true
	}
	for key := range a {
		keys[key] = true
	}
	var changed []string
	for key := range keys {
		bv, bExists := b[key]
		av, aExists := a[key]
		bj, _ := json.Marshal(bv)
		aj, _ := json.Marshal(av)
		if bExists != aExists || string(bj) != string(aj) {
			changed = append(changed, key)
		}
	}
	sort.Strings(changed)
	if len(changed) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("注解变更键: %s（值不写入审计，避免记录敏感内容）", strings.Join(changed, ", "))}
}

func dataKeyDiff(before, after any) string {
	b, _ := before.(map[string]any)
	a, _ := after.(map[string]any)
	keys := map[string]bool{}
	for key := range b {
		keys[key] = true
	}
	for key := range a {
		keys[key] = true
	}
	var added, removed, changed []string
	for key := range keys {
		bv, bExists := b[key]
		av, aExists := a[key]
		bj, _ := json.Marshal(bv)
		aj, _ := json.Marshal(av)
		switch {
		case !bExists:
			added = append(added, key)
		case !aExists:
			removed = append(removed, key)
		case string(bj) != string(aj):
			changed = append(changed, key)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	if len(added)+len(removed)+len(changed) == 0 {
		return "数据（前后无差异）"
	}
	return fmt.Sprintf("数据键变更（不记录值）: 新增 [%s]，删除 [%s]，修改 [%s]",
		strings.Join(added, ", "), strings.Join(removed, ", "), strings.Join(changed, ", "))
}

// metaSectionDiff labels/annotations 审计摘要收尾（空差异 → 无差异文案）
func metaSectionDiff(section string, lines []string) string {
	if len(lines) == 0 {
		return section + "（前后无差异）"
	}
	return section + ":\n - " + strings.Join(lines, "\n - ")
}

// toContainerMap 从 workload 对象提取 容器名 → 容器对象（deployment/statefulset/job/cronjob 通用）
func toContainerMap(obj map[string]any) (map[string]map[string]any, bool) {
	out := map[string]map[string]any{}
	s, found, _ := unstructured.NestedSlice(obj, "spec", "template", "spec", "containers")
	if !found {
		s, found, _ = unstructured.NestedSlice(obj, "spec", "jobTemplate", "spec", "template", "spec", "containers")
	}
	if !found {
		s, _, _ = unstructured.NestedSlice(obj, "spec", "containers")
	}
	for _, item := range s {
		if m, ok := item.(map[string]any); ok {
			if n, _ := m["name"].(string); n != "" {
				out[n] = m
			}
		}
	}
	return out, len(out) > 0
}

func nested(obj map[string]any, fields ...string) any {
	v, found, _ := unstructured.NestedFieldNoCopy(obj, fields...)
	if !found {
		return nil
	}
	return v
}

// jsonString 稳定的字符串形式：nil 输出 []（与空数组视觉一致，审计摘要可读性）
func jsonString(v any) string {
	if v == nil {
		return "[]"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// denyFieldUpdate 字段越权：审计 + 响应；返回 false 表示拒绝
func denyFieldUpdate(c *gin.Context, clusterName, namespace, resource, name string, bad, patterns []string) bool {
	if len(bad) == 0 {
		return false
	}
	detail := "该授权未开放任何可修改字段"
	if len(patterns) > 0 {
		detail = "可修改: " + strings.Join(patterns, ", ")
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "resource_update", clusterName, namespace, resource, name,
		"越权字段: "+strings.Join(bad, ", ")+"；"+detail, "denied"))
	c.JSON(http.StatusForbidden, gin.H{"message": "无权修改字段: " + strings.Join(bad, ", ") + "（" + detail + "）"})
	return true
}
