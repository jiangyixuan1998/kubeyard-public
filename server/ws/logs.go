package ws

import (
	"bufio"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"kubeyard/internal/audit"
	"kubeyard/internal/cluster"
	"kubeyard/internal/middleware"
)

// HandleLogs GET /api/v1/ws/logs?cluster=&namespace=&pod=&container=&follow=&tail_lines=&previous=
func HandleLogs(mgr *cluster.Manager, db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		clusterName := c.Query("cluster")
		ns := c.Query("namespace")
		pod := c.Query("pod")
		container := c.Query("container")
		follow := c.Query("follow") == "true"
		previous := c.Query("previous") == "true"
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
		if !middleware.Allowed(db, claims.Role, claims.UserID, clusterName, ns, "logs", "pods") {
			_ = conn.WriteJSON(map[string]string{"type": "auth_failed", "message": "无该容器的日志权限"})
			audit.Log(audit.Record(claims.UserID, claims.Username, "logs", clusterName, ns, "pods", pod, "权限拒绝", "denied"))
			return
		}
		e, err := mgr.Get(clusterName)
		if err != nil {
			_ = conn.WriteJSON(map[string]string{"type": "error", "message": err.Error()})
			return
		}
		_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})

		opts := &corev1.PodLogOptions{Container: container, Follow: follow, Previous: previous}
		if tail := c.Query("tail_lines"); tail != "" {
			var n int64
			for _, ch := range tail {
				if ch < '0' || ch > '9' {
					n = 0
					break
				}
				n = n*10 + int64(ch-'0')
			}
			if n > 0 {
				opts.TailLines = &n
			}
		}
		// 流式客户端：无 Timeout，否则 follow 模式 30s 被掐断
		streamClient, err := kubernetes.NewForConfig(e.StreamConfig())
		if err != nil {
			_ = conn.WriteJSON(map[string]string{"type": "error", "message": "内部错误: " + err.Error()})
			return
		}
		req := streamClient.CoreV1().Pods(ns).GetLogs(pod, opts)
		stream, err := req.Stream(c.Request.Context())
		if err != nil {
			_ = conn.WriteJSON(map[string]string{"type": "error", "message": "拉取日志失败: " + err.Error()})
			return
		}
		defer stream.Close()

		audit.Log(audit.Record(claims.UserID, claims.Username, "logs", clusterName, ns, "pods", pod, "容器 "+container, "success"))

		var writeMu sync.Mutex
		ctx := c.Request.Context()

		// 断连检测：任意读错误/关闭帧即取消日志流
		go func() {
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return // conn 已由 defer 关闭；ctx 随请求取消
				}
			}
		}()

		scanner := bufio.NewScanner(stream)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				_ = conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
					time.Now().Add(5*time.Second))
				return
			default:
			}
			writeMu.Lock()
			err := conn.WriteMessage(websocket.BinaryMessage, append(scanner.Bytes(), '\n'))
			writeMu.Unlock()
			if err != nil {
				return // 客户端断开
			}
		}
		if err := scanner.Err(); err != nil {
			writeMu.Lock()
			_ = conn.WriteMessage(websocket.TextMessage,
				[]byte(`{"type":"error","message":"`+sanitizeJSON(err.Error())+`"}`))
			writeMu.Unlock()
		}
		writeMu.Lock()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"end"}`))
		writeMu.Unlock()
	}
}

func sanitizeJSON(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch {
		case b == '"' || b == '\\':
			out = append(out, '\\', b)
		case b == '\n':
			out = append(out, '\\', 'n')
		case b < 0x20:
			out = append(out, ' ')
		default:
			out = append(out, b)
		}
	}
	return string(out)
}
