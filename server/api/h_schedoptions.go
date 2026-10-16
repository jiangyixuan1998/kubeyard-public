package api

import (
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"kubeyard/internal/middleware"
)

// ---------- 调度候选数据（SchedEditor 下拉：节点名 / 标签键 / 标签值）----------
//
// 一般用户不分配 nodes 的 get/list——列表接口拿不到节点数据，调度策略编辑器的节点与标签
// 下拉会恒为空。本端点只暴露调度相关的最小字段（节点名、是否封锁、标签键/值），权限不挂
// nodes 的 get/list，而是「该用户在本集群任意命名空间对 5 类工作负载持有 update + scheduling
// 动作」——语义：有调度策略编辑权，就需要看到调度候选数据；数据本身（节点名/标签）不属敏感面。
// 无权限返回 200 + 空列表（不是 403）：前端静默降级为手输，不弹错误。

// schedOptionsWorkloads 有 scheduling 动作的资源清单（与 workloadFormSections / builtinActions 同步）
var schedOptionsWorkloads = []string{"deployments", "statefulsets", "daemonsets", "jobs", "cronjobs"}

// schedOptionsCanView 用户是否可在本集群看到调度候选：任一工作负载规则的 update + scheduling（actions=* 视为含）
func schedOptionsCanView(db *gorm.DB, role string, userID uint, clusterName string) bool {
	for _, res := range schedOptionsWorkloads {
		if middleware.Allowed(db, role, userID, clusterName, "", "update", res) &&
			middleware.ActionAllowed(db, role, userID, clusterName, "", res, "scheduling") {
			return true
		}
	}
	return false
}

// SchedOptions GET /api/v1/clusters/:cluster/sched-options
// 返回 {nodes: [{name, unschedulable}], label_keys: [k...], label_values: {k: [v...]}}
func (h *H) SchedOptions(c *gin.Context) {
	clusterName := c.Param("cluster")
	empty := gin.H{"nodes": []gin.H{}, "label_keys": []string{}, "label_values": map[string][]string{}, "taint_keys": []string{}, "taint_values": map[string][]string{}}
	if middleware.Role(c) != "SuperAdmin" && !schedOptionsCanView(h.db, middleware.Role(c), middleware.UID(c), clusterName) {
		c.JSON(http.StatusOK, empty) // 静默降级：前端下拉为空、允许手输
		return
	}
	e, err := h.mgr.Get(clusterName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	list, err := e.Dynamic.Resource(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "nodes"}).
		List(c.Request.Context(), metav1.ListOptions{})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": "获取节点失败: " + err.Error()})
		return
	}
	aggKeys := map[string]map[string]bool{}
	taintAgg := map[string]map[string]bool{}
	agg := func(m map[string]string) {
		for k, v := range m {
			if k == "" || v == "" {
				continue
			}
			if aggKeys[k] == nil {
				aggKeys[k] = map[string]bool{}
			}
			aggKeys[k][v] = true
		}
	}
	nodes := make([]gin.H, 0, len(list.Items))
	for i := range list.Items {
		n := &list.Items[i]
		name, _, _ := unstructured.NestedString(n.Object, "metadata", "name")
		unsched, _, _ := unstructured.NestedBool(n.Object, "spec", "unschedulable")
		nodes = append(nodes, gin.H{"name": name, "unschedulable": unsched})
		labels, _, _ := unstructured.NestedStringMap(n.Object, "metadata", "labels")
		agg(labels)
		// 污点聚合（污点编辑行的键/值下拉）：系统管理型污点排除——表单禁止增删改，不该出现在候选里
		taints, _, _ := unstructured.NestedSlice(n.Object, "spec", "taints")
		for _, tv := range taints {
			t, ok := tv.(map[string]any)
			if !ok {
				continue
			}
			tk, _ := t["key"].(string)
			tvv, _ := t["value"].(string)
			if tk == "" || systemTaintKeys[tk] {
				continue
			}
			if taintAgg[tk] == nil {
				taintAgg[tk] = map[string]bool{}
			}
			if tvv != "" {
				taintAgg[tk][tvv] = true
			}
		}
	}
	sort.Slice(nodes, func(a, b int) bool { return nodes[a]["name"].(string) < nodes[b]["name"].(string) })
	labelKeys := make([]string, 0, len(aggKeys))
	labelValues := map[string][]string{}
	for k, vs := range aggKeys {
		labelKeys = append(labelKeys, k)
		arr := make([]string, 0, len(vs))
		for v := range vs {
			arr = append(arr, v)
		}
		sort.Strings(arr)
		labelValues[k] = arr
	}
	sort.Strings(labelKeys)
	taintKeysArr := make([]string, 0, len(taintAgg))
	taintValues := map[string][]string{}
	for k, vs := range taintAgg {
		taintKeysArr = append(taintKeysArr, k)
		arr := make([]string, 0, len(vs))
		for v := range vs {
			arr = append(arr, v)
		}
		sort.Strings(arr)
		taintValues[k] = arr
	}
	sort.Strings(taintKeysArr)
	c.JSON(http.StatusOK, gin.H{"nodes": nodes, "label_keys": labelKeys, "label_values": labelValues,
		"taint_keys": taintKeysArr, "taint_values": taintValues})
}
