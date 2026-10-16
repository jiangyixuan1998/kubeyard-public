package api

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kubeyard/internal/audit"
	"kubeyard/internal/middleware"
)

// ---------- 节点概要：角色 + CPU/内存/异构资源（GPU/NPU/DCU…）----------

// nodeSumResource 资源表单行：容量 / 可分配 / 已分配 / 分配率由前端按 unit 格式化计算。
// 扩展资源不硬编码厂商清单——capacity/allocatable 里除基础资源外的键（nvidia.com/gpu、
// huawei.com/npu、enflame DCU 等）一律原样透出，新增设备无需改代码。
type nodeSumResource struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"` // cpu | memory | pods | storage | extended
	Capacity    string `json:"capacity"`
	Allocatable string `json:"allocatable"`
	Allocated   int64  `json:"allocated"`
	Unit        string `json:"unit"` // milli | bytes | count
}

type nodeSumPod struct {
	Namespace string           `json:"namespace"`
	Name      string           `json:"name"`
	Phase     string           `json:"phase"`
	CPUMilli  int64            `json:"cpu_milli"`
	MemBytes  int64            `json:"mem_bytes"`
	Ext       map[string]int64 `json:"ext,omitempty"` // 扩展资源分配（nvidia.com/gpu 等）
}

type nodeSummary struct {
	Roles          []string               `json:"roles"`
	InternalIP     string                 `json:"internal_ip"`
	Hostname       string                 `json:"hostname"`
	KubeletVersion string                 `json:"kubelet_version"`
	OSImage        string                 `json:"os_image"`
	KernelVersion  string                 `json:"kernel_version"`
	RuntimeVersion string                 `json:"container_runtime_version"`
	Architecture   string                 `json:"architecture"`
	Taints         []corev1.Taint         `json:"taints"`
	Conditions     []corev1.NodeCondition `json:"conditions"`
	Resources      []nodeSumResource      `json:"resources"`
	PodCount       int                    `json:"pod_count"`
	Pods           []nodeSumPod           `json:"pods"`
	PodsFiltered   bool                   `json:"pods_filtered"` // 明细行按可见命名空间过滤过（汇总数字仍为全量）
}

// isBaseNodeResource 基础资源（非扩展/加速器）。其余键一律按扩展资源透出。
func isBaseNodeResource(name corev1.ResourceName) bool {
	switch name {
	case corev1.ResourceCPU, corev1.ResourceMemory, corev1.ResourcePods, corev1.ResourceEphemeralStorage:
		return true
	}
	s := string(name)
	return strings.HasPrefix(s, "hugepages-") || strings.HasPrefix(s, "attachable-volumes-")
}

// reqValue 资源量的统一数值口径：cpu 毫核、memory 字节、其余整数个
func reqValue(name corev1.ResourceName, q resource.Quantity) int64 {
	if name == corev1.ResourceCPU {
		return q.MilliValue()
	}
	return q.Value()
}

// podRequestsExt pod 有效 requests（K8s 口径 = max(常规容器之和, 最大 init 容器)，按资源独立取 max），
// 返回 键→数值（cpu 毫核 / memory 字节 / 扩展资源个数）。dashboard 的 podRequests 只算 cpu/memory，
// 这里是它对所有资源维度的推广（原函数不动，避免影响总览口径）。
func podRequestsExt(p *corev1.Pod) map[string]int64 {
	main := map[string]int64{}
	for i := range p.Spec.Containers {
		for name, q := range p.Spec.Containers[i].Resources.Requests {
			main[string(name)] += reqValue(name, q)
		}
	}
	icMax := map[string]int64{}
	for i := range p.Spec.InitContainers {
		for name, q := range p.Spec.InitContainers[i].Resources.Requests {
			if v := reqValue(name, q); v > icMax[string(name)] {
				icMax[string(name)] = v
			}
		}
	}
	out := make(map[string]int64, len(main)+len(icMax))
	for k, v := range main {
		out[k] = v
	}
	for k, v := range icMax {
		if v > out[k] {
			out[k] = v
		}
	}
	return out
}

// nodeRoles 角色标签（node-role.kubernetes.io/<role>）；无任何角色标签 = worker（kubectl 口径）
func nodeRoles(n *corev1.Node) []string {
	const prefix = "node-role.kubernetes.io/"
	roles := []string{}
	for k := range n.Labels {
		if strings.HasPrefix(k, prefix) {
			roles = append(roles, strings.TrimPrefix(k, prefix))
		}
	}
	if len(roles) == 0 {
		roles = append(roles, "worker")
	}
	sort.Strings(roles)
	return roles
}

// NodeSummary GET /api/v1/clusters/:cluster/nodes/:name/summary
// 权限：nodes 的 get（集群级，忽略 ns 维度）。汇总数字（容量/可分配/已分配）视为节点自身属性，
// 有 nodes get 即放行（与总览「监控口径全员一致」同原则）；Pod 分配明细行按用户对 pods 的
// 可见命名空间过滤（与总览「清单口径按可见 ns 过滤」同原则），发生过滤时 pods_filtered=true。
func (h *H) NodeSummary(c *gin.Context) {
	name := c.Param("name")
	if !middleware.Allowed(h.db, middleware.Role(c), middleware.UID(c), c.Param("cluster"), "", "get", "nodes") {
		c.JSON(http.StatusForbidden, gin.H{"message": "无该节点的查看权限"})
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "summary", c.Param("cluster"), "", "nodes", name, "权限拒绝", "denied"))
		return
	}
	e, err := h.mgr.Get(c.Param("cluster"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	ctx := c.Request.Context()
	n, err := e.Client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": "获取节点失败: " + err.Error()})
		return
	}
	// 该节点上的 Pod（fieldSelector 只命中已调度 Pod，天然排除 Pending）
	podList, err := e.Client.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + name})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": "获取节点 Pod 失败: " + err.Error()})
		return
	}

	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "summary", c.Param("cluster"), "", "nodes", name, "查看节点概要", "success"))

	sum := nodeSummary{
		Roles:          nodeRoles(n),
		Taints:         n.Spec.Taints,
		Conditions:     n.Status.Conditions,
		Pods:           []nodeSumPod{},
		PodsFiltered:   false,
		KubeletVersion: n.Status.NodeInfo.KubeletVersion,
		OSImage:        n.Status.NodeInfo.OSImage,
		KernelVersion:  n.Status.NodeInfo.KernelVersion,
		RuntimeVersion: n.Status.NodeInfo.ContainerRuntimeVersion,
		Architecture:   n.Status.NodeInfo.Architecture,
	}
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP && sum.InternalIP == "" {
			sum.InternalIP = a.Address
		}
		if a.Type == corev1.NodeHostName && sum.Hostname == "" {
			sum.Hostname = a.Address
		}
	}

	// Pod 聚合：已分配按全量（节点属性）；明细行按可见 ns 过滤
	podNS, podAll, _ := middleware.ListableNamespaces(h.db, middleware.Role(c), middleware.UID(c), c.Param("cluster"), "pods")
	visible := func(ns string) bool {
		if podAll {
			return true
		}
		for _, v := range podNS {
			if v == ns {
				return true
			}
		}
		return false
	}
	allocated := map[string]int64{} // 资源名 → 已分配（全量口径）
	for i := range podList.Items {
		p := &podList.Items[i]
		req := podRequestsExt(p)
		for k, v := range req {
			allocated[k] += v
		}
		if !visible(p.Namespace) {
			sum.PodsFiltered = true
			continue
		}
		row := nodeSumPod{Namespace: p.Namespace, Name: p.Name, Phase: string(p.Status.Phase), CPUMilli: req[string(corev1.ResourceCPU)], MemBytes: req[string(corev1.ResourceMemory)]}
		for k, v := range req {
			if !isBaseNodeResource(corev1.ResourceName(k)) {
				if row.Ext == nil {
					row.Ext = map[string]int64{}
				}
				row.Ext[k] = v
			}
		}
		sum.Pods = append(sum.Pods, row)
	}
	sum.PodCount = len(podList.Items)
	sort.SliceStable(sum.Pods, func(i, j int) bool { return sum.Pods[i].CPUMilli > sum.Pods[j].CPUMilli })
	if len(sum.Pods) > 500 {
		sum.Pods = sum.Pods[:500]
	}

	// 资源表：cpu / memory / pods / ephemeral-storage 固定顺序 + 扩展资源按名排序
	kindOf := map[corev1.ResourceName]struct{ kind, unit string }{
		corev1.ResourceCPU:              {"cpu", "milli"},
		corev1.ResourceMemory:           {"memory", "bytes"},
		corev1.ResourcePods:             {"pods", "count"},
		corev1.ResourceEphemeralStorage: {"storage", "bytes"},
	}
	resRow := func(name corev1.ResourceName, kind, unit string, cap, alloc corev1.ResourceList) nodeSumResource {
		row := nodeSumResource{Name: string(name), Kind: kind, Unit: unit, Allocated: allocated[string(name)]}
		if q, ok := cap[name]; ok {
			row.Capacity = q.String()
		}
		if q, ok := alloc[name]; ok {
			row.Allocatable = q.String()
		}
		return row
	}
	baseOrder := []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory, corev1.ResourcePods, corev1.ResourceEphemeralStorage}
	for _, rn := range baseOrder {
		sum.Resources = append(sum.Resources, resRow(rn, kindOf[rn].kind, kindOf[rn].unit, n.Status.Capacity, n.Status.Allocatable))
	}
	extNames := []string{}
	for rn := range n.Status.Capacity {
		if !isBaseNodeResource(rn) {
			extNames = append(extNames, string(rn))
		}
	}
	for rn := range n.Status.Allocatable {
		if !isBaseNodeResource(rn) && !containsStr(extNames, string(rn)) {
			extNames = append(extNames, string(rn))
		}
	}
	sort.Strings(extNames)
	for _, rn := range extNames {
		sum.Resources = append(sum.Resources, resRow(corev1.ResourceName(rn), "extended", "count", n.Status.Capacity, n.Status.Allocatable))
	}
	// pods 行的「已分配」= 该节点 Pod 数（requests 里不会出现 pods 键）
	for i := range sum.Resources {
		if sum.Resources[i].Kind == "pods" {
			sum.Resources[i].Allocated = int64(sum.PodCount)
		}
	}

	c.JSON(http.StatusOK, sum)
}
