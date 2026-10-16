package cluster

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	discoveryCached "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	"kubeyard/internal/config"
	"kubeyard/internal/cryptox"
	"kubeyard/internal/model"

	"gorm.io/gorm"
)

// Entry 单集群连接缓存：RESTConfig + 三类客户端 + 多 master 地址
type Entry struct {
	Name       string
	Raw        []byte // 解密后的 kubeconfig（用于地址切换重建）
	Context    string
	Servers    []string
	Current    int
	ClusterUID string // kube-system ns UID，集群身份指纹（故障切换防错连）
	RestConfig *rest.Config
	Client     *kubernetes.Clientset
	Dynamic    dynamic.Interface
	Disco      discovery.CachedDiscoveryInterface

	fails int
}

// Mapper 返回该集群的 RESTMapper（CRD 变更后由健康检查协程定期 Invalidate）
func (e *Entry) Mapper() meta.RESTMapper {
	return restmapper.NewDeferredDiscoveryRESTMapper(e.Disco)
}

// ResolveGVR GVK → GVR
func (e *Entry) ResolveGVR(gvk schema.GroupVersionKind) (*meta.RESTMapping, error) {
	return e.Mapper().RESTMapping(gvk.GroupKind(), gvk.Version)
}

// PreferredGVR 按集群 discovery 把前端请求的 (group, version, resource) 校正为集群实际服务的版本。
// 兼容新老集群：老集群 CronJob 只在 batch/v1beta1、新集群只在 batch/v1 等；discovery 不可用时原样返回
func (e *Entry) PreferredGVR(group, version, resource string) schema.GroupVersionResource {
	gvr := schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
	if group == "" { // core(v1) 版本稳定，无需解析
		return gvr
	}
	lists, _ := e.Disco.ServerPreferredResources()
	for _, l := range lists {
		gv, perr := schema.ParseGroupVersion(l.GroupVersion)
		if perr != nil || gv.Group != group {
			continue
		}
		for _, r := range l.APIResources {
			if r.Name == resource && !strings.Contains(r.Name, "/") {
				return gv.WithResource(resource)
			}
		}
	}
	return gvr
}

// StreamConfig 流式长连接用配置：去掉 30s Timeout，否则 exec/日志流会被定时器掐断
func (e *Entry) StreamConfig() *rest.Config {
	cfg := rest.CopyConfig(e.RestConfig)
	cfg.Timeout = 0
	return cfg
}

// Manager 多集群客户端管理器
type Manager struct {
	mu      sync.RWMutex
	entries map[string]*Entry
	db      *gorm.DB
	cfg     *config.Config
}

func NewManager(db *gorm.DB, cfg *config.Config) *Manager {
	return &Manager{entries: map[string]*Entry{}, db: db, cfg: cfg}
}

func (m *Manager) DataDir() string { return m.cfg.DataDir }

// LoadFromDB 启动时加载全部集群：解密凭据 → 构建 client → 失败标 Offline
func LoadFromDB(db *gorm.DB, cfg *config.Config) (*Manager, error) {
	m := NewManager(db, cfg)
	var clusters []model.Cluster
	if err := db.Find(&clusters).Error; err != nil {
		return nil, err
	}
	for i := range clusters {
		cl := &clusters[i]
		var cred model.ClusterCredential
		if err := db.Where("cluster_id = ?", cl.ID).First(&cred).Error; err != nil {
			m.setStatus(cl.Name, "Offline")
			log.Printf("[cluster] %s 无凭据记录，标记 Offline", cl.Name)
			continue
		}
		raw, err := cryptox.Decrypt(cred.CipherText)
		if err != nil {
			m.setStatus(cl.Name, "Offline")
			log.Printf("[cluster] %s 凭据读取失败，标记 Offline", cl.Name)
			continue
		}
		// 历史密文读取成功后自动转存明文（一次性迁移，之后不再依赖密钥）
		if string(raw) != cred.CipherText {
			db.Model(&cred).Update("cipher_text", string(raw))
		}
		e, err := buildEntry(cl.Name, raw, cred.ContextName)
		if err != nil {
			m.setStatus(cl.Name, "Offline")
			log.Printf("[cluster] %s 构建客户端失败: %v，标记 Offline", cl.Name, err)
			continue
		}
		m.mu.Lock()
		m.entries[cl.Name] = e
		m.mu.Unlock()
		m.setStatus(cl.Name, "Healthy")
		go m.RefreshMeta(cl.Name)
	}
	return m, nil
}

// Add 纳管/更新集群（热加载）
func (m *Manager) Add(name string, raw []byte, contextName string) (*Entry, error) {
	e, err := buildEntry(name, raw, contextName)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.entries[name] = e
	m.mu.Unlock()
	return e, nil
}

func (m *Manager) Remove(name string) {
	m.mu.Lock()
	delete(m.entries, name)
	m.mu.Unlock()
}

func (m *Manager) Get(name string) (*Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[name]
	if !ok {
		return nil, fmt.Errorf("集群 %s 不存在或不可用", name)
	}
	return e, nil
}

func (m *Manager) Names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.entries))
	for n := range m.entries {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (m *Manager) setStatus(name, status string) {
	m.db.Model(&model.Cluster{}).Where("name = ?", name).Update("status", status)
}

// RefreshMeta 拉取集群版本与节点数入库
func (m *Manager) RefreshMeta(name string) {
	e, err := m.Get(name)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	v, err := e.Client.Discovery().ServerVersion()
	if err != nil {
		return
	}
	nodes, err := e.Client.CoreV1().Nodes().List(ctx, metav1ListOptions())
	if err != nil {
		return
	}
	m.db.Model(&model.Cluster{}).Where("name = ?", name).
		Updates(map[string]any{"version": v.GitVersion, "node_count": len(nodes.Items)})
}

// StartHealthCheck 30s 健康探测 + 状态机 + 多 master 切换；10min 失效 discovery 缓存
func (m *Manager) StartHealthCheck() {
	tick := time.NewTicker(30 * time.Second)
	reload := time.NewTicker(10 * time.Minute)
	for {
		select {
		case <-tick.C:
			m.checkOnce()
		case <-reload.C:
			m.mu.RLock()
			for _, e := range m.entries {
				e.Disco.Invalidate()
			}
			m.mu.RUnlock()
		}
	}
}

func (m *Manager) checkOnce() {
	m.mu.RLock()
	snapshot := make([]*Entry, 0, len(m.entries))
	for _, e := range m.entries {
		snapshot = append(snapshot, e)
	}
	m.mu.RUnlock()

	for _, e := range snapshot {
		status := "Healthy"
		_, err := e.Client.Discovery().ServerVersion()
		if err != nil {
			e.fails++
			switch {
			case e.fails >= 5:
				status = "Offline"
			case e.fails >= 2:
				status = "Degraded"
			default:
				status = "Healthy"
			}
			// 尝试切换到其他 master 地址
			if ne, ok := m.tryFailover(e); ok {
				e = ne
				status = "Degraded"
			}
		} else {
			e.fails = 0
		}
		m.setStatus(e.Name, status)
	}
}

// sameClusterIdentity 故障切换身份判定：当前与候选的集群指纹均非空且相同才切换。
// 任一侧为空（读不到 kube-system）即放弃——当前侧为空时无法确认候选是否同一集群，
// 仍可能切到 kubeconfig 里顺带登记的其他集群（外部评审核实的空指纹漏洞），fail-closed
func sameClusterIdentity(cur, cand string) bool {
	return cur != "" && cand != "" && cur == cand
}

// tryFailover 多 master 故障切换：用保存的 kubeconfig 换地址重建。
// 评审核实：kubeconfigServers 会把同文件里其他 cluster 的地址也列为候选，
// 凭据/CA 恰好可复用时会连到别的集群——切换前核验目标集群身份指纹
// （kube-system UID 与当前集群一致才接受；候选不可达或拿不到指纹则跳过，
// 全部失败保持原地址，fail-closed）
func (m *Manager) tryFailover(e *Entry) (*Entry, bool) {
	for i, srv := range e.Servers {
		if i == e.Current {
			continue
		}
		ne, err := rebuildEntry(e.Name, e.Raw, e.Context, i)
		if err != nil {
			continue
		}
		if !sameClusterIdentity(e.ClusterUID, ne.ClusterUID) {
			// 候选或当前拿不到指纹（不可达/无权限），或指纹不符（kubeconfig 里的
			// 其他集群）：不切换。当前集群健康时 kube-system 拿不到的可能性极低，
			// 此时宁可放弃切换
			continue
		}
		if _, verr := ne.Client.Discovery().ServerVersion(); verr == nil {
			m.mu.Lock()
			m.entries[e.Name] = ne
			m.mu.Unlock()
			log.Printf("[cluster] %s 已切换 APIServer → %s（身份核验通过）", e.Name, srv)
			return ne, true
		}
	}
	return e, false
}

// ---------- 构建逻辑 ----------

func buildEntry(name string, raw []byte, contextName string) (*Entry, error) {
	servers, ctxName, err := kubeconfigServers(raw, contextName)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for i := range servers {
		e, err := buildAt(name, raw, ctxName, i)
		if err != nil {
			lastErr = err
			continue
		}
		if _, verr := e.Client.Discovery().ServerVersion(); verr == nil {
			return e, nil
		} else {
			lastErr = verr
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("kubeconfig 中无可用 server 地址")
	}
	return nil, lastErr
}

func buildAt(name string, raw []byte, contextName string, serverIdx int) (*Entry, error) {
	servers, ctxName, err := kubeconfigServers(raw, contextName)
	if err != nil {
		return nil, err
	}
	restCfg, err := restConfigFor(raw, ctxName, servers[serverIdx])
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	dc, err := discovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	return &Entry{
		Name:       name,
		Raw:        raw,
		Context:    ctxName,
		Servers:    servers,
		Current:    serverIdx,
		ClusterUID: clusterUID(restCfg),
		RestConfig: restCfg,
		Client:     cs,
		Dynamic:    dyn,
		Disco:      discoveryCached.NewMemCacheClient(dc),
	}, nil
}

// clusterUID 集群身份指纹：kube-system namespace 的 UID（集群生命周期内稳定，
// kubectl 生态通用的集群标识方式）。拿不到返回空串——故障切换侧对空指纹
// fail-closed（不切换），避免错连到 kubeconfig 里顺带登记的其他集群
func clusterUID(cfg *rest.Config) string {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ns, err := cs.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return ""
	}
	return string(ns.UID)
}

func rebuildEntry(name string, raw []byte, contextName string, serverIdx int) (*Entry, error) {
	return buildAt(name, raw, contextName, serverIdx)
}

// restConfigFor 复制 kubeconfig 并替换目标 server（保留 CA/凭据），QPS/Burst/Timeout 统一设置
func restConfigFor(raw []byte, contextName, server string) (*rest.Config, error) {
	apiCfg, err := clientcmd.Load(raw)
	if err != nil {
		return nil, fmt.Errorf("解析 kubeconfig: %w", err)
	}
	ctx := apiCfg.Contexts[contextName]
	if ctx == nil {
		return nil, fmt.Errorf("context %q 在 kubeconfig 中不存在", contextName)
	}
	if cl := apiCfg.Clusters[ctx.Cluster]; cl != nil && server != "" {
		cl.Server = server
	}
	buf, err := clientcmd.Write(*apiCfg)
	if err != nil {
		return nil, err
	}
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(buf)
	if err != nil {
		return nil, err
	}
	restCfg.QPS = 50
	restCfg.Burst = 100
	restCfg.Timeout = 30 * time.Second
	return restCfg, nil
}

// kubeconfigServers 提取 server 列表：当前 context 的优先，其余按名排序兜底
func kubeconfigServers(raw []byte, contextName string) ([]string, string, error) {
	apiCfg, err := clientcmd.Load(raw)
	if err != nil {
		return nil, "", fmt.Errorf("解析 kubeconfig: %w", err)
	}
	if contextName == "" {
		contextName = apiCfg.CurrentContext
	}
	ctx := apiCfg.Contexts[contextName]
	if ctx == nil {
		return nil, "", fmt.Errorf("context %q 在 kubeconfig 中不存在", contextName)
	}
	var out []string
	if cl, ok := apiCfg.Clusters[ctx.Cluster]; ok && cl.Server != "" {
		out = append(out, cl.Server)
	}
	names := make([]string, 0, len(apiCfg.Clusters))
	for n := range apiCfg.Clusters {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s := apiCfg.Clusters[n].Server
		if s == "" || containsStr(out, s) {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, "", fmt.Errorf("kubeconfig 未包含 server 地址")
	}
	return out, contextName, nil
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
