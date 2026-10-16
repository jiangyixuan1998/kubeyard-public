package ws

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	"kubeyard/internal/audit"
	"kubeyard/internal/auth"
	"kubeyard/internal/cluster"
	"kubeyard/internal/middleware"
	"kubeyard/internal/model"
)

// defaultExecCommand 容器内优先 bash，无则退回 sh。
// 进入 shell 前注入 UTF-8 locale：业务镜像多为 LANG=POSIX（如 nginx 官方镜像），
// bash 在 POSIX locale 下把中文等非 ASCII 字节渲染成八进制转义（ä¸­）且光标定位错位，
// 表现为「输入中文变成其他内容」。注入只影响这个终端会话的 shell 进程，不改容器应用的环境。
var defaultExecCommand = []string{"sh", "-c", "export LANG=C.UTF-8 LC_ALL=C.UTF-8; command -v bash >/dev/null 2>&1 && exec bash || exec sh"}

// ---------- 终端会话 ----------

// TerminalSession 实现 io.ReadWriter + remotecommand.TerminalSizeQueue
// 协议：二进制帧 = 终端输入/输出；文本帧 = 控制消息（resize/ping）
type TerminalSession struct {
	conn     *websocket.Conn
	writeMu  sync.Mutex
	inputCh  chan []byte
	resizeCh chan remotecommand.TerminalSize
	closed   chan struct{}
	closeOne sync.Once
	pending  []byte // 单帧大于 Read 缓冲时的剩余字节（大块粘贴），下次 Read 续传
}

func NewTerminalSession(conn *websocket.Conn) *TerminalSession {
	return &TerminalSession{
		conn:     conn,
		inputCh:  make(chan []byte, 16),
		resizeCh: make(chan remotecommand.TerminalSize, 4),
		closed:   make(chan struct{}),
	}
}

func (s *TerminalSession) Read(p []byte) (int, error) {
	if len(s.pending) > 0 {
		n := copy(p, s.pending)
		s.pending = s.pending[n:]
		return n, nil
	}
	select {
	case data := <-s.inputCh:
		n := copy(p, data)
		// 帧大于调用方缓冲（大块粘贴）时不能丢尾巴——丢了会截断在
		// 多字节字符中间，终端回显直接乱码；剩余字节续传
		if n < len(data) {
			s.pending = append(s.pending[:0], data[n:]...)
		}
		return n, nil
	case <-s.closed:
		return 0, io.EOF
	}
}

func (s *TerminalSession) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *TerminalSession) Next() *remotecommand.TerminalSize {
	select {
	case sz := <-s.resizeCh:
		return &sz
	case <-s.closed:
		return nil
	}
}

func (s *TerminalSession) writeControl(v any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.conn.WriteJSON(v)
}

func (s *TerminalSession) Close() {
	s.closeOne.Do(func() { close(s.closed) })
}

// pump 持续读取客户端消息分发到输入/调整尺寸通道
func (s *TerminalSession) pump() {
	defer s.Close()
	for {
		mt, data, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		switch mt {
		case websocket.BinaryMessage:
			select {
			case s.inputCh <- data:
			case <-s.closed:
				return
			}
		case websocket.TextMessage:
			var msg struct {
				Type string `json:"type"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			switch msg.Type {
			case "resize":
				select {
				case s.resizeCh <- remotecommand.TerminalSize{Width: msg.Cols, Height: msg.Rows}:
				case <-s.closed:
					return
				}
			case "ping":
				_ = s.writeControl(map[string]string{"type": "pong"})
			}
		}
	}
}

// ---------- asciinema v2 录像 ----------

// ---------- 高危命令拦截 ----------

// dangerCmdRes 高危命令规则（始终开启，无开关）：匹配整行里任一位置的命令
// （`;` `&` `|` 后的子命令也算）。刻意保持窄匹配——只拦明确不可逆的破坏性操作，
// 宁可漏过可疑命令交给录像审计，不可误拦正常操作。
var dangerCmdRes = []*regexp.Regexp{
	// rm 递归/强制删，且目标整体是危险路径：根（裸 / 或 /*）、家目录（~ ~/ ~/*）、
	// 裸通配符 *、越级 ..、一级系统目录（/etc /usr /var 等，容器内删掉即容器报废）。
	// 目标后必须到行尾或分隔符，否则 rm -rf /tmp/x 会被误拦
	regexp.MustCompile(`(^|[;&|]\s*)rm\s+(-\w*[rRfF]\w*\s+)+(/\*|/\s*$|~/?(?:\*|\s|$)|\*(?:\s|$)|\.\.?(?:/)?(?:\s|$)|/(?:etc|usr|var|bin|sbin|lib(?:32|64)?|boot|dev|proc|sys|opt)(?:/\*|\s|$))`),
	// 格式化磁盘 / 建交换区
	regexp.MustCompile(`(^|[;&|]\s*)(sudo\s+)?mkfs(\.\w+)?\s`),
	regexp.MustCompile(`(^|[;&|]\s*)(sudo\s+)?mkswap\s`),
	// dd 直写块设备
	regexp.MustCompile(`(^|[;&|]\s*)dd\s[^;|&]*of=/dev/(sd|nvme|vd|hd|xvd|mapper)`),
	// 关机 / 重启
	regexp.MustCompile(`(^|[;&|]\s*)(sudo\s+)?(shutdown|reboot|halt|poweroff|init\s+[06])\b`),
	// 删命名空间 / 全量删除
	regexp.MustCompile(`(^|[;&|]\s*)kubectl\s+delete\s+(ns|namespace)s?\s+\S`),
	regexp.MustCompile(`(^|[;&|]\s*)kubectl\s+delete\s+[^;|&]*--all\b`),
	// 递归 chmod 777 整目录（根或任何路径整体放开写权限）
	regexp.MustCompile(`(^|[;&|]\s*)chmod\s+(-\w*R\w*\s+)+777\s+/(?:\s|$)`),
	// 下载内容直接进 shell
	regexp.MustCompile(`(curl|wget)[^;|&]*\|\s*(sudo\s+)?(ba)?sh\b`),
	// fork 炸弹
	regexp.MustCompile(`:\(\)\s*\{[^}]*\}\s*;\s*:`),
}

func dangerCmd(cmd string) bool {
	for _, re := range dangerCmdRes {
		if re.MatchString(cmd) {
			return true
		}
	}
	return false
}

// guardedSession 高危命令拦截层：包在录像层之外（录像先记录原始输入，再由本层过滤）。
// 拦截方式 = 只吞掉回车键：命令仍留在命令行上（用户看得到刚才敲了什么），
// 但 shell 永远收不到 \r，命令不会执行；随后写一条红色警告（经录像层 → 既入库又被回放）
// 并记审计。已知局限：多行粘贴时每行独立判定，前一行回车已放行——这类操作
// 由录像审计兜底。
type guardedSession struct {
	inner            io.ReadWriter
	claims           *auth.Claims
	cluster, ns, pod string
	line             []byte // 当前命令行缓冲（可打印字节）
	esc              int    // 转义序列状态机：0=正常 1=刚见ESC 2=CSI/SS3 参数段 3=OSC 文本段
}

func (g *guardedSession) Read(p []byte) (int, error) {
	n, err := g.inner.Read(p)
	if n > 0 {
		n = g.filter(p[:n])
	}
	return n, err
}

func (g *guardedSession) Write(p []byte) (int, error) {
	return g.inner.Write(p)
}

// filter 逐字节过滤一个输入块，原地压缩返回新长度：
// 可打印字节进行缓冲；回车触发高危判定（命中则吞掉该字节）；退格弹出缓冲；
// 转义序列与 Tab 等控制字节原样放行（readline 需要它们工作），但行缓冲作废——
// 翻历史/补全后屏幕上的实际命令与缓冲不一致，用旧缓冲判定会误伤或漏判
func (g *guardedSession) filter(chunk []byte) int {
	out := chunk[:0]
	for _, b := range chunk {
		// 转义序列中（与 h_recording.go castCommands 同一套状态机）：
		// ESC [ ... 终止字节(0x40-0x7e) / ESC O x / ESC ] ...(BEL 或 ST)。
		// 序列字节照常放行但不进行缓冲
		if g.esc > 0 {
			switch g.esc {
			case 1:
				if b == '[' || b == 'O' {
					g.esc = 2
				} else if b == ']' {
					g.esc = 3
				} else {
					g.esc = 0
				}
			case 2:
				if b >= 0x40 && b <= 0x7e {
					g.esc = 0
				}
			case 3:
				if b == 0x07 || b == 0x1b {
					g.esc = 0
				}
			}
			out = append(out, b)
			continue
		}
		switch {
		case b == '\r' || b == '\n':
			cmd := strings.TrimSpace(string(g.line))
			danger := cmd != "" && dangerCmd(cmd)
			g.line = g.line[:0]
			if danger {
				g.warn(cmd)
				continue // 吞掉回车：命令留在屏幕上但不执行
			}
			out = append(out, b)
		case b == 0x7f || b == '\b':
			// 退格按字符边界弹（中文等多字节字符）：先把续字节弹掉再弹起始字节，
			// 否则行缓冲残留半个字符——不影响字节放行，但高危判定的命令文本会错
			for len(g.line) > 0 && g.line[len(g.line)-1]&0xC0 == 0x80 {
				g.line = g.line[:len(g.line)-1]
			}
			if len(g.line) > 0 {
				g.line = g.line[:len(g.line)-1]
			}
			out = append(out, b)
		case b == 0x1b: // 转义序列开始（方向键/光标控制）：进入序列态，行缓冲作废
			g.esc = 1
			g.line = g.line[:0]
			out = append(out, b)
		case b < 0x20: // 其余控制字节（Tab 补全/Ctrl-C/Ctrl-U 等）→ 行缓冲不可信，照常放行
			g.line = g.line[:0]
			out = append(out, b)
		default:
			g.line = append(g.line, b)
			out = append(out, b)
		}
	}
	return len(out)
}

// warn 拦截提示：红色警告写入终端（经录像层记录），并落审计
func (g *guardedSession) warn(cmd string) {
	msg := "\r\n\x1b[1;31m⚠ 已拦截高危命令（未执行）: " + cmd + "\x1b[0m\r\n" +
		"（该命令已被平台策略拦截；如需重新输入请按 Ctrl-C 丢弃本行）\r\n"
	_, _ = g.inner.Write([]byte(msg))
	audit.Log(audit.Record(g.claims.UserID, g.claims.Username, "exec", g.cluster, g.ns, "pods", g.pod,
		"拦截高危命令: "+cmd, "denied"))
}

// ---------- exec handler ----------

// RiskEvaluator 终端会话结束后的 AI 风险评估回调（api 层注入，避免 ws→api 循环依赖）。
// 录像行落库完成后异步调用：实现方读取录像内容、调平台配置的模型评估风险等级并回写该行。
// 评估失败静默（风险列留空 = 未评估），不影响会话收尾。
type RiskEvaluator func(rec *model.ExecRecording)

// HandleExec GET /api/v1/ws/exec?cluster=&namespace=&pod=&container=
// riskEval 可为 nil（未配置 AI 时）：nil 则会话结束不做风险评估
func HandleExec(mgr *cluster.Manager, db *gorm.DB, riskEval RiskEvaluator) gin.HandlerFunc {
	return func(c *gin.Context) {
		clusterName := c.Query("cluster")
		ns := c.Query("namespace")
		pod := c.Query("pod")
		container := c.Query("container")
		if clusterName == "" || ns == "" || pod == "" {
			c.JSON(http.StatusBadRequest, gin.H{"message": "缺少参数"})
			return
		}
		conn, err := Upgrade(c)
		if err != nil {
			return
		}
		defer conn.Close()

		claims, err := AuthFirstFrame(conn)
		if err != nil {
			_ = conn.WriteJSON(map[string]string{"type": "auth_failed", "message": err.Error()})
			return
		}
		if !middleware.Allowed(db, claims.Role, claims.UserID, clusterName, ns, "exec", "pods") {
			_ = conn.WriteJSON(map[string]string{"type": "auth_failed", "message": "无该容器的 exec 权限"})
			audit.Log(audit.Record(claims.UserID, claims.Username, "exec", clusterName, ns, "pods", pod, "权限拒绝", "denied"))
			return
		}
		e, err := mgr.Get(clusterName)
		if err != nil {
			_ = conn.WriteJSON(map[string]string{"type": "error", "message": err.Error()})
			return
		}

		// 容器缺省：取注解指定的默认容器，否则第一个
		if container == "" {
			p, err := e.Client.CoreV1().Pods(ns).Get(c.Request.Context(), pod, metav1.GetOptions{})
			if err != nil {
				_ = conn.WriteJSON(map[string]string{"type": "error", "message": "获取 Pod 失败: " + err.Error()})
				return
			}
			container = defaultContainerOf(p)
		}

		_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})

		session := NewTerminalSession(conn)
		go session.pump()

		// 录像：内容写内存缓冲，周期落库（无 PVC 部署下 Pod 重启最多丢最后一个周期）
		sessionID := fmt.Sprintf("%d-%s", time.Now().UnixMilli(), randomID(6))
		var recordRow *model.ExecRecording
		recordRow = &model.ExecRecording{
			SessionID: sessionID,
			Username:  claims.Username,
			Cluster:   clusterName,
			Namespace: ns,
			Pod:       pod,
			Container: container,
			StartedAt: time.Now(),
			// EndedAt 留 NULL = 「录像中」（WS 仍连接）；断连/退出 update EndedAt
		}
		if cerr := db.Create(recordRow).Error; cerr != nil {
			// 创建失败：本场录制没有 DB 行可写（周期/收尾全部无效）——明确日志而非静默丢
			// 会话本身继续可用（终端功能与录制解耦），仅录像缺失
			log.Printf("[recording] 录像行创建失败（本场录制不会入库）: %v", cerr)
		}
		rec := newRecordedSession(session, func(content string, size int64, dur int, overflow bool) {
			if recordRow.ID == 0 {
				return // Create 已失败：无行可写
			}
			updates := map[string]any{
				"size_bytes": size,
				"duration":   dur,
				"content":    content,
			}
			if overflow {
				updates["risk_why"] = "录像超过大小上限已截断（内容不完整）"
			}
			if uerr := db.Model(recordRow).Updates(updates).Error; uerr != nil {
				log.Printf("[recording] 周期落库失败（会话 %s）: %v", sessionID, uerr)
			}
		})
		// 周期落库：30s 一次（Pod 被杀最多丢最后一个周期）。
		// flushDone：收尾前等周期 goroutine 退出——迟到的周期写库会用旧快照覆盖最终内容
		flushStop := make(chan struct{})
		flushDone := make(chan struct{})
		go func() {
			defer close(flushDone)
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					rec.FlushPush()
				case <-flushStop:
					return
				}
			}
		}()

		audit.Log(audit.Record(claims.UserID, claims.Username, "exec", clusterName, ns, "pods", pod, "容器 "+container+" 会话 "+sessionID, "success"))

		// 构造 exec 请求
		req := e.Client.CoreV1().RESTClient().Post().
			Resource("pods").
			Namespace(ns).
			Name(pod).
			SubResource("exec")
		req.VersionedParams(&corev1.PodExecOptions{
			Command:   defaultExecCommand,
			Container: container,
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, scheme.ParameterCodec)

		var stdin io.ReadWriter = session
		var sizeQueue remotecommand.TerminalSizeQueue = session
		if rec != nil {
			stdin = rec
			sizeQueue = rec // resize 也走录制器，尺寸事件进 cast
		}
		// 高危命令拦截：包在录像层之外——录像先记录原始输入再过滤，警告经录像层写入
		stdin = &guardedSession{inner: stdin, claims: claims, cluster: clusterName, ns: ns, pod: pod}
		streamErr := streamExec(c.Request.Context(), e, req.URL(), stdin, sizeQueue)

		endMsg := map[string]string{"type": "end"}
		if streamErr != nil {
			endMsg["message"] = streamErr.Error()
		}
		_ = session.writeControl(endMsg)
		session.Close()
		if rec != nil {
			close(flushStop)  // 停周期计时器
			<-flushDone       // 等在途周期写库完成：杜绝旧快照覆盖最终内容的竞态
			rec.Close()
			// 最终落库：内容 + 结束时间一次写入；成功后回填内存结构供 AI 风险评估读取
			content, size, dur, overflow := rec.Snapshot()
			updates := map[string]any{
				"size_bytes": size,
				"duration":   dur,
				"content":    content,
				"ended_at":   time.Now(), // 真实结束时间戳（审计/时长统计用；NULL = 会话进行中）
			}
			if overflow {
				updates["risk_why"] = "录像超过大小上限已截断（内容不完整）"
			}
			uerr := db.Model(recordRow).Updates(updates).Error
			if uerr != nil {
				// 最终落库失败：无本地文件兜底，本场内容丢失——明确日志（比静默丢可诊断）
				log.Printf("[recording] 最终落库失败（会话 %s 内容丢失）: %v", sessionID, uerr)
			} else {
				recordRow.Content = content
				recordRow.SizeBytes = size
				// AI 风险评估：异步执行，不阻塞会话收尾（模型调用秒级~十几秒，失败静默留空=未评估）
				if riskEval != nil {
					go riskEval(recordRow)
				}
			}
		}
		conn.Close()
	}
}

// streamExec 优先 WebSocket 协议，失败退回 SPDY；均使用无超时的流式配置
func streamExec(ctx context.Context, e *cluster.Entry, url *url.URL, stdin io.ReadWriter, sizeQueue remotecommand.TerminalSizeQueue) error {
	cfg := e.StreamConfig()
	exec, err := remotecommand.NewWebSocketExecutor(cfg, "GET", url.String())
	if err == nil {
		err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdin:             stdin,
			Stdout:            stdin,
			Stderr:            stdin,
			Tty:               true,
			TerminalSizeQueue: sizeQueue,
		})
		if err == nil {
			return nil
		}
	}
	exec, err = remotecommand.NewSPDYExecutor(cfg, "POST", url)
	if err != nil {
		return err
	}
	return exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:             stdin,
		Stdout:            stdin,
		Stderr:            stdin,
		Tty:               true,
		TerminalSizeQueue: sizeQueue,
	})
}

func defaultContainerOf(p *corev1.Pod) string {
	if v, ok := p.Annotations["kubectl.kubernetes.io/default-container"]; ok && v != "" {
		return v
	}
	for _, ct := range p.Spec.Containers {
		return ct.Name
	}
	return ""
}

// randomID crypto/rand 十六进制短 ID
func randomID(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
