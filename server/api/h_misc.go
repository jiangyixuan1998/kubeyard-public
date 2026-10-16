package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kubeyard/internal/middleware"
	"kubeyard/internal/model"
)

// ---------- Dashboard ----------

// 异常 Pod（Pending / CrashLoopBackOff / ImagePullBackOff / Evicted 等运行异常）
type abnormalPod struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Reason    string    `json:"reason"`
	Ready     string    `json:"ready"` // "1/2"
	Restarts  int32     `json:"restarts"`
	StartTime time.Time `json:"start_time"`
}

// 命名空间指标（Pod 计数 + 资源请求）
type nsMetric struct {
	Namespace       string `json:"namespace"`
	PodCount        int    `json:"pod_count"`
	CPURequestMilli int64  `json:"cpu_request_milli"`
	MemRequestBytes int64  `json:"mem_request_bytes"`
}

// 未达副本的应用
type deployHealth struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Ready     int32  `json:"ready"`
	Desired   int32  `json:"desired"`
}

// clusterWarm 预热缓存里的单集群完整指标（全量数据；响应组装时再按用户可见命名空间过滤）
type clusterWarm struct {
	Name            string
	Status          string
	Version         string
	NodeCount       int
	NodeReady       int
	NodeNotReady    []string
	NSCount         int
	PodCount        int
	DeployCount     int
	CPUAllocMilli   int64
	MemAllocBytes   int64
	CPUReqMilli     int64
	MemReqBytes     int64
	NSMetrics       []nsMetric          // 按 Pod 数降序
	NSMap           map[string]nsMetric // 快速按 ns 取指标
	Abnormal        []abnormalPod
	DeploysNotReady []deployHealth
	CollectErrs     []string // 采集失败的项（nodes/pods/deployments），响应透出供前端提示
}

// dashCluster 响应里的单集群。监控口径（cpu/mem_req_milli 对全集群，描述集群本身负载，全员一致）；
// 清单口径（Pod/应用/TopNS 计数与明细）按用户可见命名空间过滤；NotReady 节点名需 nodes list 权限。
// scope_all=false 时 my_* 为用户可见命名空间的 requests 之和（前端展示「我的可见范围」）。
type dashCluster struct {
	Name                string         `json:"name"`
	Description         string         `json:"description"` // 纳管描述（显示别名用，与集群下拉同口径）
	Status              string         `json:"status"`
	Version             string         `json:"version"`
	NodeCount           int            `json:"node_count"`
	NodeReady           int            `json:"node_ready"`
	NodeNotReady        []string       `json:"node_not_ready"`
	NSCount             int            `json:"ns_count"`
	PodCount            int            `json:"pod_count"`
	PodAbnormalTotal    int            `json:"pod_abnormal_total"`
	PodAbnormal         []abnormalPod  `json:"pod_abnormal"`
	DeployCount         int            `json:"deploy_count"`
	DeployNotReadyTotal int            `json:"deploy_not_ready_total"`
	DeploysNotReady     []deployHealth `json:"deploys_not_ready"`
	CPUAllocMilli       int64          `json:"cpu_alloc_milli"`
	CPUReqMilli         int64          `json:"cpu_req_milli"`
	MemAllocBytes       int64          `json:"mem_alloc_bytes"`
	MemReqBytes         int64          `json:"mem_req_bytes"`
	ScopeAll            bool           `json:"scope_all"`
	MyCPUReqMilli       int64          `json:"my_cpu_req_milli"`
	MyMemReqBytes       int64          `json:"my_mem_req_bytes"`
	TopNamespaces       []nsMetric     `json:"top_namespaces"`
	CollectErrs         []string       `json:"collect_errors"` // 采集失败的项：nodes / pods / deployments
}

type dashWarning struct {
	Cluster   string    `json:"cluster"`
	Namespace string    `json:"namespace"`
	Object    string    `json:"object"`
	Reason    string    `json:"reason"`
	Message   string    `json:"message"`
	Count     int64     `json:"count"`
	LastTime  time.Time `json:"last_time"`
}

type dashData struct {
	GeneratedAt         time.Time     `json:"generated_at"`
	Clusters            []dashCluster `json:"clusters"`
	TotalClusters       int           `json:"total_clusters"`
	TotalNodes          int           `json:"total_nodes"`
	TotalPods           int           `json:"total_pods"`
	TotalAbnormal       int           `json:"total_abnormal"`
	TotalNotReadyDeploy int           `json:"total_not_ready_deploys"`
	HealthyCount        int           `json:"healthy_count"`
	DegradedCount       int           `json:"degraded_count"`
	OfflineCount        int           `json:"offline_count"`
	Warnings            struct {
		Total int           `json:"total"`
		More  bool          `json:"more"` // 超过扫描上限，总数展示为 "N+"
		Items []dashWarning `json:"items"`
	} `json:"warnings"`
}

// 预热/响应各列表的条数上限（防大集群撑爆内存与响应体）
const (
	maxAbnormalWarm = 500 // 单集群异常 Pod 预热上限
	maxAbnormalResp = 50  // 响应单集群异常 Pod 明细上限
	maxNotReadyWarm = 200 // 单集群未达副本应用预热上限
	maxNotReadyResp = 50
	maxTopNS        = 10  // 命名空间 Top N
	maxWarnScan     = 300 // Warning 事件扫描上限（近 24h，按 last_time 倒序）
	maxWarnItems    = 12
)

var (
	dashMu     sync.Mutex
	dashWarm   map[string]clusterWarm // 后台定时预热的全集群指标（name → 指标）
	dashWarmAt time.Time              // 预热数据生成时间
)

// podRequests 容器 requests 汇总。K8s 有效请求 = max(常规容器之和, 最大 init 容器)，照此近似
func podRequests(p *corev1.Pod) (cpuMilli, memBytes int64) {
	for i := range p.Spec.Containers {
		r := p.Spec.Containers[i].Resources.Requests
		if q, ok := r[corev1.ResourceCPU]; ok {
			cpuMilli += q.MilliValue()
		}
		if q, ok := r[corev1.ResourceMemory]; ok {
			memBytes += q.Value()
		}
	}
	var icCPU, icMem int64
	for i := range p.Spec.InitContainers {
		r := p.Spec.InitContainers[i].Resources.Requests
		if q, ok := r[corev1.ResourceCPU]; ok {
			if v := q.MilliValue(); v > icCPU {
				icCPU = v
			}
		}
		if q, ok := r[corev1.ResourceMemory]; ok {
			if v := q.Value(); v > icMem {
				icMem = v
			}
		}
	}
	if icCPU > cpuMilli {
		cpuMilli = icCPU
	}
	if icMem > memBytes {
		memBytes = icMem
	}
	return cpuMilli, memBytes
}

// podAbnormalReason 异常原因（空串 = 正常）。Succeeded（跑完的 Job）视为正常；
// 容器等待态上报原生 Reason（CrashLoopBackOff / ImagePullBackOff / CreateContainerConfigError 等），
// 调度等待统一报 Pending；ContainerCreating / PodInitializing 属正常启动过程不计
func podAbnormalReason(p *corev1.Pod) string {
	if p.Status.Phase == corev1.PodSucceeded {
		return ""
	}
	if p.Status.Phase == corev1.PodFailed || p.Status.Reason == "Evicted" {
		if p.Status.Reason != "" {
			return p.Status.Reason
		}
		return "Failed"
	}
	checkWaiting := func(statuses []corev1.ContainerStatus) string {
		for i := range statuses {
			if w := statuses[i].State.Waiting; w != nil && w.Reason != "" &&
				w.Reason != "ContainerCreating" && w.Reason != "PodInitializing" {
				return w.Reason
			}
		}
		return ""
	}
	if r := checkWaiting(p.Status.ContainerStatuses); r != "" {
		return r
	}
	if r := checkWaiting(p.Status.InitContainerStatuses); r != "" {
		return r
	}
	if p.Status.Phase == corev1.PodPending {
		return "Pending"
	}
	return ""
}

// computeClusterMetric 拉取单集群全量指标（后台预热用，可较重；四路并发）：
// 命名空间计数（Limit=1 + remainingItemCount 不取对象体）、节点 Ready/NotReady + allocatable、
// Pod 计数/异常/资源请求（按命名空间聚合）、Deployment 计数/未达副本名单
func (h *H) computeClusterMetric(cl model.Cluster) clusterWarm {
	w := clusterWarm{Name: cl.Name, Status: cl.Status, Version: cl.Version, NodeCount: cl.NodeCount}
	e, err := h.mgr.Get(cl.Name)
	if err != nil {
		return w
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	nsCh := make(chan int, 1)
	nodeCh := make(chan string, 1) // 采集失败项标记（空串 = 成功）
	podCh := make(chan string, 1)
	depCh := make(chan string, 1)

	go func() { // 命名空间计数
		nss, err := e.Client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1})
		if err != nil {
			nsCh <- 0
			return
		}
		if nss.RemainingItemCount == nil {
			// 退回全量（apiserver 未回填计数）
			all, err := e.Client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
			if err != nil {
				nsCh <- 0
				return
			}
			nsCh <- len(all.Items)
			return
		}
		nsCh <- len(nss.Items) + int(*nss.RemainingItemCount)
	}()
	go func() { // 节点：Ready 计数、NotReady 名单、allocatable 汇总
		nodes, err := e.Client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			nodeCh <- "nodes"
			return
		}
		w.NodeCount = len(nodes.Items)
		for i := range nodes.Items {
			n := &nodes.Items[i]
			ready := false
			for _, cond := range n.Status.Conditions {
				if cond.Type == corev1.NodeReady {
					ready = cond.Status == corev1.ConditionTrue
				}
			}
			if ready {
				w.NodeReady++
			} else {
				w.NodeNotReady = append(w.NodeNotReady, n.Name)
			}
			if q, ok := n.Status.Allocatable[corev1.ResourceCPU]; ok {
				w.CPUAllocMilli += q.MilliValue()
			}
			if q, ok := n.Status.Allocatable[corev1.ResourceMemory]; ok {
				w.MemAllocBytes += q.Value()
			}
		}
		nodeCh <- ""
	}()
	go func() { // Pod：计数（先 Limit=1 快速拿，全量失败时兜底）+ 异常/请求按命名空间聚合
		if pl, err := e.Client.CoreV1().Pods("").List(ctx, metav1.ListOptions{Limit: 1}); err == nil && pl.RemainingItemCount != nil {
			w.PodCount = len(pl.Items) + int(*pl.RemainingItemCount)
		}
		pods, err := e.Client.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
		if err != nil {
			podCh <- "pods"
			return
		}
		nsMap := map[string]*nsMetric{}
		abnormal := []abnormalPod{}
		for i := range pods.Items {
			p := &pods.Items[i]
			m := nsMap[p.Namespace]
			if m == nil {
				m = &nsMetric{Namespace: p.Namespace}
				nsMap[p.Namespace] = m
			}
			m.PodCount++
			cpu, mem := podRequests(p)
			m.CPURequestMilli += cpu
			m.MemRequestBytes += mem
			w.CPUReqMilli += cpu
			w.MemReqBytes += mem
			if reason := podAbnormalReason(p); reason != "" && len(abnormal) < maxAbnormalWarm {
				ready, restarts := 0, int32(0)
				for j := range p.Status.ContainerStatuses {
					cs := &p.Status.ContainerStatuses[j]
					if cs.Ready {
						ready++
					}
					restarts += cs.RestartCount
				}
				var st time.Time
				if p.Status.StartTime != nil {
					st = p.Status.StartTime.Time
				}
				abnormal = append(abnormal, abnormalPod{
					Namespace: p.Namespace, Name: p.Name, Reason: reason,
					Ready:    strconv.Itoa(ready) + "/" + strconv.Itoa(len(p.Status.ContainerStatuses)),
					Restarts: restarts, StartTime: st,
				})
			}
		}
		w.PodCount = len(pods.Items) // 全量更准，覆盖 Limit=1 的近似值
		w.Abnormal = abnormal
		w.NSMetrics = make([]nsMetric, 0, len(nsMap))
		for _, m := range nsMap {
			w.NSMetrics = append(w.NSMetrics, *m)
		}
		sort.Slice(w.NSMetrics, func(i, j int) bool { return w.NSMetrics[i].PodCount > w.NSMetrics[j].PodCount })
		w.NSMap = make(map[string]nsMetric, len(nsMap))
		for _, m := range w.NSMetrics {
			w.NSMap[m.Namespace] = m
		}
		podCh <- ""
	}()
	go func() { // Deployment：计数 + 未达副本名单（replicas=0 的缩容状态不算异常）
		deps, err := e.Client.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
		if err != nil {
			depCh <- "deployments"
			return
		}
		w.DeployCount = len(deps.Items)
		nr := []deployHealth{}
		for i := range deps.Items {
			d := &deps.Items[i]
			desired := int32(1)
			if d.Spec.Replicas != nil {
				desired = *d.Spec.Replicas
			}
			if desired > 0 && d.Status.ReadyReplicas < desired && len(nr) < maxNotReadyWarm {
				nr = append(nr, deployHealth{Namespace: d.Namespace, Name: d.Name, Ready: d.Status.ReadyReplicas, Desired: desired})
			}
		}
		w.DeploysNotReady = nr
		depCh <- ""
	}()
	w.NSCount = <-nsCh
	for _, tag := range []string{<-nodeCh, <-podCh, <-depCh} {
		if tag != "" {
			w.CollectErrs = append(w.CollectErrs, tag)
		}
	}
	return w
}

// refreshDashboard 全量刷新预热数据（集群间并发 fan-out）
func (h *H) refreshDashboard() {
	var clusters []model.Cluster
	h.db.Order("name ASC").Find(&clusters)
	out := make(map[string]clusterWarm, len(clusters))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := range clusters {
		wg.Add(1)
		go func(cl model.Cluster) {
			defer wg.Done()
			d := h.computeClusterMetric(cl)
			mu.Lock()
			out[cl.Name] = d
			mu.Unlock()
		}(clusters[i])
	}
	wg.Wait()
	dashMu.Lock()
	dashWarm, dashWarmAt = out, time.Now()
	dashMu.Unlock()
}

// dashboardLoop 后台每 5 分钟预热一次（异常 Pod / 用度等需要可用的时效性；
// 全量 Pod List 对超大集群有压力，必要时可调大间隔），接口只读缓存秒回
func (h *H) dashboardLoop() {
	h.refreshDashboard()
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		h.refreshDashboard()
	}
}

func (h *H) warmSnapshot() (map[string]clusterWarm, time.Time) {
	dashMu.Lock()
	defer dashMu.Unlock()
	if dashWarm == nil {
		return map[string]clusterWarm{}, time.Time{}
	}
	return dashWarm, dashWarmAt
}

// Dashboard GET /api/v1/dashboard?refresh=1 — 数据由后台每 5 分钟预热，正常点击秒回；refresh=1 立即重算。
// 响应按用户权限过滤：Pod/应用/命名空间维度只含可见命名空间（pods / deployments 的 list 授权），
// NotReady 节点名需 nodes 的 list 权限，Warning 事件按可见命名空间 + 集群级对象的 list 授权过滤。
func (h *H) Dashboard(c *gin.Context) {
	c.JSON(http.StatusOK, h.dashboardData(c))
}

// dashboardData 总览数据组装（Dashboard 接口与 AI 巡检共用同一份权限过滤后的数据源，
// 保证「AI 看到的 = 用户在总览页看到的」）
func (h *H) dashboardData(c *gin.Context) dashData {
	role, uid := middleware.Role(c), middleware.UID(c)
	clusters := middleware.AllowedClusters(h.db, role, uid)
	super := role == model.RoleSuperAdmin
	var grants []model.UserGrant
	if !super {
		grants = middleware.EffectiveGrants(h.db, uid)
	}

	warm, at := h.warmSnapshot()
	cold := len(warm) == 0
	for i := range clusters {
		if _, ok := warm[clusters[i].Name]; !ok {
			cold = true
			break
		}
	}
	if c.Query("refresh") == "1" || cold {
		h.refreshDashboard()
		warm, at = h.warmSnapshot()
	}

	out := dashData{GeneratedAt: at, Clusters: []dashCluster{}}
	for i := range clusters {
		cl := &clusters[i]
		w, ok := warm[cl.Name]
		if !ok {
			// 极端情况（刚纳管且预热失败）：现场补算，避免空行
			w = h.computeClusterMetric(*cl)
		}
		dc := dashCluster{
			Name: w.Name, Description: cl.Description, Status: w.Status, Version: w.Version,
			NodeCount: w.NodeCount, NodeReady: w.NodeReady,
			NSCount:       w.NSCount,
			CPUAllocMilli: w.CPUAllocMilli, MemAllocBytes: w.MemAllocBytes,
			PodAbnormal:     []abnormalPod{},
			DeploysNotReady: []deployHealth{},
			TopNamespaces:   []nsMetric{},
			CollectErrs:     w.CollectErrs,
		}
		switch w.Status {
		case "Healthy":
			out.HealthyCount++
		case "Degraded":
			out.DegradedCount++
		default:
			out.OfflineCount++
		}

		// 监控口径：requests / 可分配量始终为整个集群（描述集群本身负载，与用户权限无关，
		// 管理员与受限用户看到的百分比一致；受限用户的可见范围用量单独走 my_* 字段）
		dc.CPUReqMilli, dc.MemReqBytes = w.CPUReqMilli, w.MemReqBytes

		// Pod 维度（计数 / 异常 / Top 命名空间 / 可见范围用量）按 pods list 授权的命名空间过滤
		podAll, podOK := super, super
		var podNS []string
		if !super {
			podNS, podAll, podOK = middleware.ListableNSFromGrants(grants, cl.ID, "pods")
		}
		if podOK {
			dc.ScopeAll = podAll
			if podAll {
				dc.PodCount = w.PodCount
				dc.MyCPUReqMilli, dc.MyMemReqBytes = w.CPUReqMilli, w.MemReqBytes
				dc.PodAbnormalTotal = len(w.Abnormal)
				if len(w.Abnormal) > maxAbnormalResp {
					dc.PodAbnormal = append(dc.PodAbnormal, w.Abnormal[:maxAbnormalResp]...)
				} else {
					dc.PodAbnormal = append(dc.PodAbnormal, w.Abnormal...)
				}
				if len(w.NSMetrics) > maxTopNS {
					dc.TopNamespaces = append(dc.TopNamespaces, w.NSMetrics[:maxTopNS]...)
				} else {
					dc.TopNamespaces = append(dc.TopNamespaces, w.NSMetrics...)
				}
			} else {
				nsSet := map[string]bool{}
				for _, ns := range podNS {
					nsSet[ns] = true
				}
				for _, ns := range podNS {
					if m, ok := w.NSMap[ns]; ok {
						dc.PodCount += m.PodCount
						dc.MyCPUReqMilli += m.CPURequestMilli
						dc.MyMemReqBytes += m.MemRequestBytes
						dc.TopNamespaces = append(dc.TopNamespaces, m)
					}
				}
				sort.Slice(dc.TopNamespaces, func(i, j int) bool { return dc.TopNamespaces[i].PodCount > dc.TopNamespaces[j].PodCount })
				if len(dc.TopNamespaces) > maxTopNS {
					dc.TopNamespaces = dc.TopNamespaces[:maxTopNS]
				}
				for _, p := range w.Abnormal {
					if nsSet[p.Namespace] {
						dc.PodAbnormal = append(dc.PodAbnormal, p)
					}
				}
				dc.PodAbnormalTotal = len(dc.PodAbnormal)
			}
		}

		// 应用维度按 deployments list 授权过滤
		depAll, depOK := super, super
		var depNS []string
		if !super {
			depNS, depAll, depOK = middleware.ListableNSFromGrants(grants, cl.ID, "deployments")
		}
		if depOK {
			dc.DeployCount = w.DeployCount
			if depAll {
				dc.DeployNotReadyTotal = len(w.DeploysNotReady)
				n := len(w.DeploysNotReady)
				if n > maxNotReadyResp {
					n = maxNotReadyResp
				}
				dc.DeploysNotReady = append(dc.DeploysNotReady, w.DeploysNotReady[:n]...)
			} else {
				for _, d := range w.DeploysNotReady {
					for _, ns := range depNS {
						if d.Namespace == ns {
							dc.DeploysNotReady = append(dc.DeploysNotReady, d)
							break
						}
					}
				}
				dc.DeployNotReadyTotal = len(dc.DeploysNotReady)
			}
		}

		// NotReady 节点名：需 nodes 的 list 权限（计数本身不限制，与原总览口径一致）
		if super || middleware.HasListGrantInCluster(grants, cl.ID, "nodes") {
			dc.NodeNotReady = w.NodeNotReady
		}

		out.TotalNodes += dc.NodeCount
		out.TotalPods += dc.PodCount
		out.TotalAbnormal += dc.PodAbnormalTotal
		out.TotalNotReadyDeploy += dc.DeployNotReadyTotal
		out.Clusters = append(out.Clusters, dc)
	}
	out.TotalClusters = len(clusters)

	// 近 24h Warning 事件（读事件采集库，30s 粒度；按用户可见命名空间过滤，
	// 集群级对象的事件需对应资源的 list 权限——与事件页口径一致）
	var rows []model.ClusterEvent
	h.db.Where("type = ? AND last_time > ?", "Warning", time.Now().Add(-24*time.Hour)).
		Order("last_time DESC").Limit(maxWarnScan).Find(&rows)
	type nsVis struct {
		list []string
		all  bool
	}
	vis := map[string]nsVis{}
	byName := map[string]model.Cluster{}
	for i := range clusters {
		byName[clusters[i].Name] = clusters[i]
		if super {
			vis[clusters[i].Name] = nsVis{all: true}
		} else {
			l, all := middleware.VisibleNSFromGrants(grants, clusters[i].ID)
			vis[clusters[i].Name] = nsVis{list: l, all: all}
		}
	}
	out.Warnings.Items = []dashWarning{}
	for i := range rows {
		r := &rows[i]
		cl, ok := byName[r.Cluster]
		if !ok {
			continue
		}
		if r.Namespace != "" {
			v := vis[r.Cluster]
			if !v.all && !containsStr(v.list, r.Namespace) {
				continue
			}
		} else if !(super || middleware.HasListGrantInCluster(grants, cl.ID, strings.ToLower(r.ObjectType)+"s")) {
			continue
		}
		if len(out.Warnings.Items) < maxWarnItems {
			out.Warnings.Items = append(out.Warnings.Items, dashWarning{
				Cluster: r.Cluster, Namespace: r.Namespace,
				Object: r.ObjectType + "/" + r.ObjectName,
				Reason: r.Reason, Message: r.Message, Count: r.Count, LastTime: r.LastTime,
			})
		}
		out.Warnings.Total++
	}
	out.Warnings.More = out.Warnings.Total >= maxWarnScan

	return out
}

// ---------- 命名空间别名 ----------

func nsAliasKey(clusterName string) string { return "ns_alias:" + clusterName }

// nsAliasMap 读取某集群的命名空间别名表（Setting 存 JSON，空表返回空 map）
func nsAliasMap(db *gorm.DB, clusterName string) map[string]string {
	var s model.Setting
	if err := db.Where("`key` = ?", nsAliasKey(clusterName)).First(&s).Error; err != nil {
		return map[string]string{}
	}
	m := map[string]string{}
	_ = json.Unmarshal([]byte(s.Value), &m)
	return m
}

// GetNSAliases GET /api/v1/admin/clusters/:cluster/ns-aliases
func (h *H) GetNSAliases(c *gin.Context) {
	c.JSON(http.StatusOK, nsAliasMap(h.db, c.Param("cluster")))
}

// PutNSAliases PUT /api/v1/admin/clusters/:cluster/ns-aliases body: {"命名空间":"显示别名", ...}
func (h *H) PutNSAliases(c *gin.Context) {
	clusterName := c.Param("cluster")
	var cl model.Cluster
	if err := h.db.Where("name = ?", clusterName).First(&cl).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "集群不存在"})
		return
	}
	var m map[string]string
	if err := c.BindJSON(&m); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	clean := map[string]string{}
	for ns, alias := range m {
		alias = strings.TrimSpace(alias)
		if alias == "" || len(alias) > 64 {
			continue // 空别名=不显示；超长跳过
		}
		clean[ns] = alias
	}
	raw, _ := json.Marshal(clean)
	var s model.Setting
	if err := h.db.Where("`key` = ?", nsAliasKey(clusterName)).First(&s).Error; err == nil {
		s.Value = string(raw)
		h.db.Save(&s)
	} else {
		h.db.Create(&model.Setting{Key: nsAliasKey(clusterName), Value: string(raw)})
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// ---------- 审计 ----------

// ListAudits GET /api/v1/admin/audits?user=&q=&cluster=&action=&status=&page=&page_size=
// LEFT JOIN users：审计表只存账号，姓名/邮箱在用户表——LEFT 兜底用户被删后记录仍显示。
// user/q 均为模糊匹配；JOIN 后 username 等列名有歧义，筛选条件统一加 audit_logs. 前缀。
func (h *H) ListAudits(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	q := h.db.Model(&model.AuditLog{}).
		Select("audit_logs.*, users.display_name AS display_name, users.email AS email").
		Joins("LEFT JOIN users ON users.id = audit_logs.user_id")
	if v := c.Query("user"); v != "" {
		like := "%" + v + "%"
		q = q.Where("(audit_logs.username LIKE ? OR users.display_name LIKE ? OR users.email LIKE ?)", like, like, like)
	}
	if v := c.Query("cluster"); v != "" {
		q = q.Where("audit_logs.cluster = ?", v)
	}
	if v := c.Query("action"); v != "" {
		q = q.Where("audit_logs.action = ?", v)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("audit_logs.status = ?", v)
	}
	// 全局关键词：账号/姓名/邮箱/动作码/对象/集群/命名空间/详情 一次模糊命中
	if kw := c.Query("q"); kw != "" {
		like := "%" + kw + "%"
		cols := []string{
			"audit_logs.username", "audit_logs.action", "audit_logs.resource_type", "audit_logs.resource_name",
			"audit_logs.cluster", "audit_logs.namespace", "audit_logs.detail", "users.display_name", "users.email",
		}
		cond := make([]string, len(cols))
		args := make([]any, 0, len(cols))
		for i, col := range cols {
			cond[i] = col + " LIKE ?"
			args = append(args, like)
		}
		q = q.Where("("+strings.Join(cond, " OR ")+")", args...)
	}
	// 管理员管辖范围过滤：SuperAdmin 全量；集群管理员看整集群；命名空间管理员看授权命名空间
	all, clusterAll, nsOf := middleware.AdminScope(h.db, middleware.Role(c), middleware.UID(c))
	if !all {
		cond, args := middleware.ScopeSQL(clusterAll, nsOf)
		q = q.Where(cond, args...)
	}
	var total int64
	q.Count(&total)
	var logs []auditRow
	q.Order("audit_logs.created_at DESC").Offset((page - 1) * size).Limit(size).Find(&logs)
	c.JSON(http.StatusOK, gin.H{"items": logs, "total": total})
}

// auditRow 审计列表行：AuditLog + JOIN 出来的用户姓名/邮箱（用户已删则为空）
type auditRow struct {
	model.AuditLog
	DisplayName string `json:"display_name,omitempty"`
	Email       string `json:"email,omitempty"`
}

// ---------- 设置 ----------

// GetSettings GET /api/v1/admin/settings
func (h *H) GetSettings(c *gin.Context) {
	var settings []model.Setting
	h.db.Find(&settings)
	m := map[string]string{}
	for _, s := range settings {
		if strings.HasPrefix(s.Key, "security.") || s.Key == "ai.api_key" {
			continue // 内部安全密钥与 AI 模型 Key 不通过通用设置接口暴露（后者走 /admin/ai 专用接口）
		}
		m[s.Key] = s.Value
	}
	c.JSON(http.StatusOK, m)
}

// PutSettings PUT /api/v1/admin/settings body: {key: value, ...}
func (h *H) PutSettings(c *gin.Context) {
	var m map[string]string
	if err := c.BindJSON(&m); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	for k, v := range m {
		if strings.HasPrefix(k, "security.") || k == "ai.api_key" {
			continue // 禁止通过通用设置接口改写安全密钥与 AI 模型 Key（后者走 /admin/ai 专用接口）
		}
		var s model.Setting
		if err := h.db.Where("`key` = ?", k).First(&s).Error; err == nil {
			s.Value = v
			h.db.Save(&s)
		} else {
			h.db.Create(&model.Setting{Key: k, Value: v})
		}
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}
