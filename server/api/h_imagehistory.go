package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kubeyard/internal/audit"
	"kubeyard/internal/middleware"
)

// ---------- 镜像历史（只读；回滚由前端把历史镜像回填到「镜像」表单，走既有 resedit 鉴权）----------

// 各控制器的当前版本号注解（ControllerRevision/ReplicaSet 的 revision 与之对应）
var currentRevisionAnnotations = map[string]string{
	"deployments":  "deployment.kubernetes.io/revision",
	"statefulsets": "statefulset.kubernetes.io/revision",
	"daemonsets":   "daemonset.kubernetes.io/revision",
}

type imageHistoryContainer struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

type imageHistoryItem struct {
	Revision       int64                   `json:"revision"`
	Current        bool                    `json:"current"`
	Time           string                  `json:"time"`
	Containers     []imageHistoryContainer `json:"containers"`
	InitContainers []imageHistoryContainer `json:"initContainers"`
}

// ImageHistory GET /api/v1/clusters/:cluster/image-history
// Deployment → 历史 ReplicaSet（deployment.kubernetes.io/revision 注解排序）；
// StatefulSet/DaemonSet → ControllerRevision（data 为 patch 或完整对象，双形态解析）。
// Job/CronJob 无原生模板历史，不支持。权限 = 父资源 get。
func (h *H) ImageHistory(c *gin.Context) {
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
	if _, supported := currentRevisionAnnotations[t.Resource]; !supported {
		c.JSON(http.StatusBadRequest, gin.H{"message": "该资源无镜像历史（仅 Deployment / StatefulSet / DaemonSet；Job/CronJob 模板无原生版本记录）"})
		return
	}
	allowedResKey := resKeyForAuth(gvr.Group, gvr.Resource)
	if !middleware.Allowed(h.db, middleware.Role(c), middleware.UID(c), clusterName, t.Namespace, "get", allowedResKey) {
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "image_history", clusterName, t.Namespace, t.Resource, t.Name, "权限拒绝", "denied"))
		denyResource403(c, "get", allowedResKey)
		return
	}
	obj, err := dynamicRes(e, gvr, t.Namespace).Get(c.Request.Context(), t.Name, metav1.GetOptions{})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": err.Error()})
		return
	}
	uid := string(obj.GetUID())
	annKey := currentRevisionAnnotations[t.Resource]
	currentRev := int64(0)
	if s := obj.GetAnnotations()[annKey]; s != "" {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			currentRev = v
		}
	}

	var histGVR = e.PreferredGVR("apps", "v1", "replicasets")
	if t.Resource != "deployments" {
		histGVR = e.PreferredGVR("apps", "v1", "controllerrevisions")
	}
	list, err := dynamicRes(e, histGVR, t.Namespace).List(c.Request.Context(), metav1.ListOptions{})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": "读取历史失败: " + err.Error()})
		return
	}

	items := []imageHistoryItem{}
	for i := range list.Items {
		it := &list.Items[i]
		if !ownedByUID(it, uid) {
			continue
		}
		item := imageHistoryItem{Time: it.GetCreationTimestamp().Format(time.RFC3339)}
		switch t.Resource {
		case "deployments":
			// 历史 ReplicaSet：revision 在注解里，podTemplate 在 spec.template.spec
			if s := it.GetAnnotations()[annKey]; s != "" {
				item.Revision, _ = strconv.ParseInt(s, 10, 64)
			}
			ts, _, _ := unstructured.NestedMap(it.Object, "spec", "template", "spec")
			item.Containers, item.InitContainers = historyContainersOf(ts)
		default:
			// ControllerRevision：revision 是自身字段；data 双形态（完整对象或 RFC6902 patch）
			item.Revision, _, _ = unstructured.NestedInt64(it.Object, "revision")
			spec, _ := it.Object["data"].(map[string]any)
			ts := templateSpecFromSpec(spec)
			if ts == nil {
				// patch 形态：应用到空对象重建完整 spec
				if raw, ok := it.Object["data"]; ok {
					if m := specFromPatch(raw); m != nil {
						ts = templateSpecFromSpec(m)
					}
				}
			}
			item.Containers, item.InitContainers = historyContainersOf(ts)
		}
		if item.Revision > 0 || len(item.Containers) > 0 {
			item.Current = currentRev > 0 && item.Revision == currentRev
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Revision > items[j].Revision })
	c.JSON(http.StatusOK, gin.H{"items": items, "current_revision": currentRev})
}

// ownedByUID ownerReferences 是否指向指定对象（按 UID 精确过滤）
func ownedByUID(obj *unstructured.Unstructured, uid string) bool {
	if uid == "" {
		return false
	}
	for _, or := range obj.GetOwnerReferences() {
		if string(or.UID) == uid {
			return true
		}
	}
	return false
}

// historyContainersOf 从 podSpec map 提取主/初始化容器镜像
func historyContainersOf(ts map[string]any) ([]imageHistoryContainer, []imageHistoryContainer) {
	if ts == nil {
		return nil, nil
	}
	conv := func(list []any) []imageHistoryContainer {
		out := make([]imageHistoryContainer, 0, len(list))
		for _, c := range list {
			m, ok := c.(map[string]any)
			if !ok {
				continue
			}
			name, _ := m["name"].(string)
			image, _ := m["image"].(string)
			out = append(out, imageHistoryContainer{Name: name, Image: image})
		}
		return out
	}
	cs, _, _ := unstructured.NestedSlice(ts, "containers")
	ics, _, _ := unstructured.NestedSlice(ts, "initContainers")
	return conv(cs), conv(ics)
}

// templateSpecFromSpec 从对象 spec 取 podTemplate 的 podSpec
func templateSpecFromSpec(spec map[string]any) map[string]any {
	if spec == nil {
		return nil
	}
	tpl, _ := spec["template"].(map[string]any)
	if tpl == nil {
		return nil
	}
	ts, _ := tpl["spec"].(map[string]any)
	return ts
}

// specFromPatch ControllerRevision.data 为 RFC6902 patch 时：应用到空对象重建完整对象。
// K8s 控制器生成的 patch 基底为 {}，补丁应用后即完整 StatefulSet/DaemonSet 对象
func specFromPatch(raw any) map[string]any {
	ops, ok := raw.([]any)
	if !ok || len(ops) == 0 {
		return nil
	}
	b, err := json.Marshal(ops)
	if err != nil {
		return nil
	}
	p, err := jsonpatch.DecodePatch(b)
	if err != nil {
		return nil
	}
	out, err := p.Apply([]byte(`{}`))
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		return nil
	}
	spec, _ := m["spec"].(map[string]any)
	return spec
}
