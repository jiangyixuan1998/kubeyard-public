package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kubeyard/internal/audit"
	"kubeyard/internal/middleware"
)

// DescribePod GET /api/v1/clusters/:cluster/pods/:namespace/:name/describe
// 输出 kubectl describe pod 风格的纯文本（对象信息 + 关联事件），前端弹窗等宽字体展示。
// 权限沿用 pods 的 get（与详情一致，只读）。
func (h *H) DescribePod(c *gin.Context) {
	ns, pod := c.Param("namespace"), c.Param("name")
	if !middleware.Allowed(h.db, middleware.Role(c), middleware.UID(c), c.Param("cluster"), ns, "get", "pods") {
		c.JSON(http.StatusForbidden, gin.H{"message": "无该 Pod 的查看权限"})
		audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "describe", c.Param("cluster"), ns, "pods", pod, "权限拒绝", "denied"))
		return
	}
	e, err := h.mgr.Get(c.Param("cluster"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	ctx := c.Request.Context()
	p, err := e.Client.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": "获取 Pod 失败: " + err.Error()})
		return
	}
	evs, err := e.Client.CoreV1().Events(ns).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.name=" + pod})
	if err != nil {
		evs = &corev1.EventList{} // 事件拉取失败不阻塞对象信息展示
	}

	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "describe", c.Param("cluster"), ns, "pods", pod, "查看 describe", "success"))

	var b strings.Builder
	ts := func(t *metav1.Time) string {
		if t == nil || t.IsZero() {
			return "<unset>"
		}
		return t.Time.Local().Format("2006-01-02 15:04:05")
	}

	fmt.Fprintf(&b, "Name:             %s\n", p.Name)
	fmt.Fprintf(&b, "Namespace:        %s\n", p.Namespace)
	if p.Spec.Priority != nil {
		fmt.Fprintf(&b, "Priority:         %d\n", *p.Spec.Priority)
	}
	if p.Spec.PriorityClassName != "" {
		fmt.Fprintf(&b, "Priority Class:   %s\n", p.Spec.PriorityClassName)
	}
	if p.Spec.NodeName != "" {
		fmt.Fprintf(&b, "Node:             %s\n", p.Spec.NodeName)
	} else {
		b.WriteString("Node:             <none>\n")
	}
	fmt.Fprintf(&b, "Start Time:       %s\n", ts(p.Status.StartTime))
	if len(p.Labels) > 0 {
		fmt.Fprintf(&b, "Labels:           %s\n", mapLine(p.Labels))
	}
	if len(p.Annotations) > 0 {
		fmt.Fprintf(&b, "Annotations:      %s\n", mapLine(p.Annotations))
	}
	fmt.Fprintf(&b, "Status:           %s\n", p.Status.Phase)
	if p.Status.Reason != "" {
		fmt.Fprintf(&b, "Reason:           %s\n", p.Status.Reason)
	}
	if p.Status.Message != "" {
		fmt.Fprintf(&b, "Message:          %s\n", p.Status.Message)
	}
	fmt.Fprintf(&b, "IP:               %s\n", orUnset(p.Status.PodIP))
	if len(p.Status.PodIPs) > 0 {
		ips := make([]string, 0, len(p.Status.PodIPs))
		for _, ip := range p.Status.PodIPs {
			ips = append(ips, ip.IP)
		}
		fmt.Fprintf(&b, "IPs:              %s\n", strings.Join(ips, ", "))
	}
	if len(p.OwnerReferences) > 0 {
		ref := p.OwnerReferences[0]
		fmt.Fprintf(&b, "Controlled By:    %s/%s\n", ref.Kind, ref.Name)
	}

	// ---------- 容器 ----------
	b.WriteString("\nContainers:\n")
	for i := range p.Spec.Containers {
		ct := &p.Spec.Containers[i]
		var st *corev1.ContainerStatus
		for j := range p.Status.ContainerStatuses {
			if p.Status.ContainerStatuses[j].Name == ct.Name {
				st = &p.Status.ContainerStatuses[j]
				break
			}
		}
		fmt.Fprintf(&b, "  %s:\n", ct.Name)
		fmt.Fprintf(&b, "    Image:      %s\n", ct.Image)
		if len(ct.Ports) > 0 {
			var parts []string
			for _, port := range ct.Ports {
				s := fmt.Sprintf("%d/%s", port.ContainerPort, port.Protocol)
				if port.Name != "" {
					s = fmt.Sprintf("%s:%d/%s", port.Name, port.ContainerPort, port.Protocol)
				}
				parts = append(parts, s)
			}
			fmt.Fprintf(&b, "    Port:       %s\n", strings.Join(parts, ", "))
		}
		if st != nil {
			fmt.Fprintf(&b, "    State:      %s\n", describeContainerState(&st.State))
			if last := describeContainerState(&st.LastTerminationState); last != "" {
				fmt.Fprintf(&b, "    Last State: %s\n", last)
			}
			fmt.Fprintf(&b, "    Ready:      %v\n", st.Ready)
			fmt.Fprintf(&b, "    Restart Count: %d\n", st.RestartCount)
		}
	}
	if len(p.Spec.InitContainers) > 0 {
		b.WriteString("\nInit Containers:\n")
		for i := range p.Spec.InitContainers {
			ct := &p.Spec.InitContainers[i]
			var st *corev1.ContainerStatus
			for j := range p.Status.InitContainerStatuses {
				if p.Status.InitContainerStatuses[j].Name == ct.Name {
					st = &p.Status.InitContainerStatuses[j]
					break
				}
			}
			fmt.Fprintf(&b, "  %s:\n", ct.Name)
			fmt.Fprintf(&b, "    Image:      %s\n", ct.Image)
			if st != nil {
				fmt.Fprintf(&b, "    State:      %s\n", describeContainerState(&st.State))
				fmt.Fprintf(&b, "    Ready:      %v\n", st.Ready)
				fmt.Fprintf(&b, "    Restart Count: %d\n", st.RestartCount)
			}
		}
	}

	// ---------- Conditions ----------
	if len(p.Status.Conditions) > 0 {
		b.WriteString("\nConditions:\n")
		for _, cond := range p.Status.Conditions {
			fmt.Fprintf(&b, "  %-24s %-8s %s\n", cond.Type, cond.Status, ts(&cond.LastTransitionTime))
		}
	}

	// ---------- Volumes ----------
	if len(p.Spec.Volumes) > 0 {
		b.WriteString("\nVolumes:\n")
		for _, v := range p.Spec.Volumes {
			fmt.Fprintf(&b, "  %s (%s):\n", v.Name, volumeKind(&v.VolumeSource))
		}
	}

	// ---------- Events ----------
	b.WriteString("\nEvents:\n")
	if len(evs.Items) == 0 {
		b.WriteString("  <none>\n")
	} else {
		sort.SliceStable(evs.Items, func(i, j int) bool {
			return evs.Items[i].LastTimestamp.After(evs.Items[j].LastTimestamp.Time)
		})
		for i := range evs.Items {
			ev := &evs.Items[i]
			fmt.Fprintf(&b, "  %-8s %-24s %s  %s\n",
				ev.Type, ev.Reason,
				fmt.Sprintf("×%d %s前", ev.Count, ageOf(ev.LastTimestamp.Time)),
				strings.TrimSpace(ev.Message))
		}
	}

	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.String(http.StatusOK, "%s", b.String())
}

func describeContainerState(st *corev1.ContainerState) string {
	if st == nil {
		return "<unset>"
	}
	if r := st.Running; r != nil {
		return "Running since " + r.StartedAt.Time.Local().Format("2006-01-02 15:04:05")
	}
	if w := st.Waiting; w != nil {
		if w.Message != "" {
			return fmt.Sprintf("Waiting (%s: %s)", w.Reason, w.Message)
		}
		return "Waiting (" + w.Reason + ")"
	}
	if t := st.Terminated; t != nil {
		return fmt.Sprintf("Terminated (%d, %s) 于 %s",
			t.ExitCode, t.Reason, t.FinishedAt.Time.Local().Format("2006-01-02 15:04:05"))
	}
	return "<unset>"
}

func volumeKind(v *corev1.VolumeSource) string {
	switch {
	case v.ConfigMap != nil:
		return "configMap " + v.ConfigMap.Name
	case v.Secret != nil:
		return "secret " + v.Secret.SecretName
	case v.PersistentVolumeClaim != nil:
		return "pvc " + v.PersistentVolumeClaim.ClaimName
	case v.EmptyDir != nil:
		return "emptyDir"
	case v.HostPath != nil:
		return "hostPath " + v.HostPath.Path
	case v.DownwardAPI != nil:
		return "downwardAPI"
	case v.Projected != nil:
		return "projected"
	case v.NFS != nil:
		return "nfs " + v.NFS.Server + ":" + v.NFS.Path
	default:
		return "other"
	}
}

func mapLine(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}

func orUnset(s string) string {
	if s == "" {
		return "<unset>"
	}
	return s
}

func ageOf(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时", int(d.Hours()))
	default:
		return fmt.Sprintf("%d 天", int(d.Hours()/24))
	}
}
