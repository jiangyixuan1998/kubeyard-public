package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	"kubeyard/internal/audit"
	"kubeyard/internal/cluster"
	"kubeyard/internal/middleware"
)

// podFilePerm 校验 pods 的文件类动词（download/upload，五元组），denied 时审计并返回 false。
// verbs 传多个时任一命中即放行（目录浏览对下载/上传权限均开放——只授了 upload 的用户也要能进文件浏览器）。
func (h *H) podFilePerm(c *gin.Context, ns, pod, detail string, verbs ...string) bool {
	for _, v := range verbs {
		if middleware.Allowed(h.db, middleware.Role(c), middleware.UID(c), c.Param("cluster"), ns, v, "pods") {
			return true
		}
	}
	label := "下载/上传"
	if len(verbs) == 1 {
		label = map[string]string{"download": "下载", "upload": "上传"}[verbs[0]]
	}
	c.JSON(http.StatusForbidden, gin.H{"message": "无该容器的文件" + label + "权限"})
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), verbs[0], c.Param("cluster"), ns, "pods", pod, "权限拒绝: "+detail, "denied"))
	return false
}

// resolvePodContainer 容器缺省：注解指定的默认容器，否则第一个。
func resolvePodContainer(ctx context.Context, e *cluster.Entry, ns, pod, container string) (string, error) {
	if container != "" {
		return container, nil
	}
	p, err := e.Client.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if len(p.Spec.Containers) == 0 {
		return "", errors.New("Pod 没有容器")
	}
	container = p.Spec.Containers[0].Name
	if v, ok := p.Annotations["kubectl.kubernetes.io/default-container"]; ok && v != "" {
		container = v
	}
	return container, nil
}

// downloadCap 下载输出上限（与上传 200MB 对称）：下载是全量收进内存再返回，
// 不设限会被容器内大文件（日志/tar 包）撑爆服务内存，并发下载更危险
const downloadCap = 200 << 20

// cappedBuffer 限容缓冲：写入累计超过 limit 直接报错中止 exec 流，防大文件全量进内存
type cappedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Buffer.Len()+len(p) > b.limit {
		b.overflow = true
		return 0, errors.New("输出超过下载上限")
	}
	return b.Buffer.Write(p)
}

func (b *cappedBuffer) reset() {
	b.Buffer.Reset()
	b.overflow = false
}

// podExecIO 在容器里执行命令并收集 stdout/stderr，可选 stdin 写入（上传用）。
// 与终端一致：WebSocket 优先，失败（旧集群/代理拒绝升级）退回 SPDY。TTY 关闭保证二进制安全。
// stdout 限容 200MB（下载防撑爆内存；列目录/上传回显远小于此）。
func podExecIO(ctx context.Context, e *cluster.Entry, ns, pod, container string, cmd []string, stdin io.Reader) (*cappedBuffer, *bytes.Buffer, error) {
	req := e.Client.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(ns).Name(pod).SubResource("exec")
	req.VersionedParams(&corev1.PodExecOptions{
		Command:   cmd,
		Container: container,
		Stdin:     stdin != nil,
		Stdout:    true,
		Stderr:    true,
		TTY:       false,
	}, scheme.ParameterCodec)
	url := req.URL()
	opts := remotecommand.StreamOptions{Tty: false, Stdin: stdin}

	stdout := &cappedBuffer{limit: downloadCap}
	var stderr bytes.Buffer
	opts.Stdout, opts.Stderr = stdout, &stderr
	if exec, err := remotecommand.NewWebSocketExecutor(e.StreamConfig(), "GET", url.String()); err == nil {
		if err := exec.StreamWithContext(ctx, opts); err == nil {
			return stdout, &stderr, nil
		}
	}

	stdout.reset()
	stderr.Reset()
	exec, err := remotecommand.NewSPDYExecutor(e.StreamConfig(), "POST", url)
	if err != nil {
		// 不返回 nil 缓冲：调用方在判 err 前就可能读 overflow / stderr，nil 会 panic
		return stdout, &stderr, err
	}
	err = exec.StreamWithContext(ctx, opts)
	return stdout, &stderr, err
}

// podExecCollect 在容器里执行一条命令并收集 stdout/stderr（无 stdin）。
func podExecCollect(ctx context.Context, e *cluster.Entry, ns, pod, container string, cmd []string) (*cappedBuffer, *bytes.Buffer, error) {
	return podExecIO(ctx, e, ns, pod, container, cmd, nil)
}

// ListPodFiles GET /api/v1/clusters/:cluster/pods/:namespace/:name/files?path=&container=
// 容器内 ls -1pa 列目录，供前端文件浏览器（勾选下载）使用。dir=true 为子目录。
func (h *H) ListPodFiles(c *gin.Context) {
	ns, pod := c.Param("namespace"), c.Param("name")
	if !h.podFilePerm(c, ns, pod, c.Query("path"), "download", "upload") {
		return
	}
	e, err := h.mgr.Get(c.Param("cluster"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	container, err := resolvePodContainer(c.Request.Context(), e, ns, pod, c.Query("container"))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": "获取容器失败: " + err.Error()})
		return
	}
	dir := path.Clean("/" + c.Query("path"))

	stdout, stderr, err := podExecCollect(c.Request.Context(), e, ns, pod, container,
		[]string{"ls", "-1pa", dir})
	if err != nil || stderr.Len() > 0 {
		msg := stderr.String()
		if msg == "" {
			msg = err.Error()
		}
		c.JSON(http.StatusBadGateway, gin.H{"message": "列目录失败: " + strings.TrimSpace(msg)})
		return
	}

	entries := []gin.H{}
	for _, line := range strings.Split(stdout.String(), "\n") {
		name := strings.TrimSpace(line)
		if name == "" || name == "." || name == ".." {
			continue
		}
		isDir := strings.HasSuffix(name, "/")
		entries = append(entries, gin.H{"name": strings.TrimSuffix(name, "/"), "dir": isDir})
	}
	// 目录在前、名称字典序
	sort.SliceStable(entries, func(i, j int) bool {
		ei, ej := entries[i], entries[j]
		if ei["dir"] != ej["dir"] {
			return ei["dir"] == true
		}
		return ei["name"].(string) < ej["name"].(string)
	})
	c.JSON(http.StatusOK, gin.H{"path": dir, "entries": entries})
}

// DownloadPodFile GET /api/v1/clusters/:cluster/pods/:namespace/:name/download?path=a&path=b&container=
// 单文件：容器内 cat 后以附件流返回；多文件（同一目录）：tar cf - 打包为 .tar 下载。
func (h *H) DownloadPodFile(c *gin.Context) {
	ns, pod := c.Param("namespace"), c.Param("name")
	paths := c.QueryArray("path")
	if len(paths) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "缺少文件路径"})
		return
	}
	if !h.podFilePerm(c, ns, pod, strings.Join(paths, ","), "download") {
		return
	}
	e, err := h.mgr.Get(c.Param("cluster"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	container, err := resolvePodContainer(c.Request.Context(), e, ns, pod, c.Query("container"))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": "获取容器失败: " + err.Error()})
		return
	}
	// 容器内路径是 POSIX 路径，清洗防穿越
	for i := range paths {
		paths[i] = path.Clean("/" + paths[i])
	}

	var cmd []string
	var filename string
	if len(paths) == 1 {
		cmd = []string{"cat", paths[0]}
		filename = path.Base(paths[0])
	} else {
		// 多文件必须同目录（前端文件浏览器保证），tar 在此目录下打包
		dir := path.Dir(paths[0])
		names := make([]string, 0, len(paths))
		for _, p := range paths {
			if path.Dir(p) != dir {
				c.JSON(http.StatusBadRequest, gin.H{"message": "多个文件必须在同一目录下"})
				return
			}
			names = append(names, path.Base(p))
		}
		// "--" 终止选项解析：文件名（path.Base）可能以 "-" 开头（如恰有名为
		// --to-command=... 的文件），不加会被 tar 当选项解析（外部评审核实）
		cmd = append([]string{"tar", "cf", "-", "-C", dir, "--"}, names...)
		filename = pod + "-files.tar"
	}

	stdout, stderr, err := podExecCollect(c.Request.Context(), e, ns, pod, container, cmd)
	if stdout.overflow {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"message": "文件过大（下载上限 200MB），请拆分文件或压缩后再下载"})
		return
	}
	if err != nil || stderr.Len() > 0 {
		msg := stderr.String()
		if msg == "" && err != nil {
			msg = err.Error()
		}
		if len(paths) > 1 && strings.Contains(msg, "not found") {
			msg += "（容器内没有 tar，请逐个单文件下载）"
		}
		c.JSON(http.StatusBadGateway, gin.H{"message": "读取文件失败: " + strings.TrimSpace(msg)})
		return
	}

	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Data(http.StatusOK, "application/octet-stream", stdout.Bytes())
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "download", c.Param("cluster"), ns, "pods", pod,
		"容器 "+container+" 文件 "+strings.Join(paths, ","), "success"))
}

// UploadPodFile POST /api/v1/clusters/:cluster/pods/:namespace/:name/upload?path=&container=
// multipart 字段 file（支持多选，前端逐个提交）。文件内容经 exec stdin 流入容器：
// cat > "$1"，目标路径走 argv 传入（避免引号/空格拼接问题）。上传到容器临时文件系统，Pod 重启即丢。
func (h *H) UploadPodFile(c *gin.Context) {
	ns, pod := c.Param("namespace"), c.Param("name")
	fh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "缺少上传文件"})
		return
	}
	if fh.Size > 200<<20 {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"message": "文件过大（上限 200MB）"})
		return
	}
	// 目标目录清洗防穿越；文件名取 Base 剥掉任何路径成分（浏览器给的 name 可能带路径）
	dir := path.Clean("/" + c.Query("path"))
	name := path.Base(strings.ReplaceAll(fh.Filename, "\\", "/"))
	if name == "." || name == ".." || name == "/" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "文件名不合法"})
		return
	}
	target := path.Join(dir, name)
	if !h.podFilePerm(c, ns, pod, target, "upload") {
		return
	}
	e, err := h.mgr.Get(c.Param("cluster"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}
	container, err := resolvePodContainer(c.Request.Context(), e, ns, pod, c.Query("container"))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": "获取容器失败: " + err.Error()})
		return
	}
	f, err := fh.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取上传文件失败: " + err.Error()})
		return
	}
	defer f.Close()
	// 内存释放机制：multipart 解析受 r.MaxMultipartMemory（32MB）约束，超限自动落临时盘、
	// 随请求结束由 Gin 清理；文件内容经 LimitReader 流式转入 exec stdin，全程只持有 64KB 级
	// 拷贝缓冲，不把文件整体载入内存。双重保险：即便客户端绕过 Content-Length，也最多流入 200MB
	limited := io.LimitReader(f, 200<<20)

	_, stderr, err := podExecIO(c.Request.Context(), e, ns, pod, container,
		[]string{"sh", "-c", `cat > "$1"`, "sh", target}, limited)
	if err != nil || stderr.Len() > 0 {
		msg := stderr.String()
		if msg == "" && err != nil {
			msg = err.Error()
		}
		c.JSON(http.StatusBadGateway, gin.H{"message": "上传失败: " + strings.TrimSpace(msg)})
		return
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "upload", c.Param("cluster"), ns, "pods", pod,
		fmt.Sprintf("容器 %s 上传 %s（%d 字节）", container, target, fh.Size), "success"))
	c.JSON(http.StatusOK, gin.H{"message": "已上传 " + name})
}
