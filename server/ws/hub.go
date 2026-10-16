package ws

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"kubeyard/internal/auth"
	"kubeyard/internal/database"
	"kubeyard/internal/model"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// 同源部署（前端由本服务托管），放行全部 Origin；跨域部署请按需收紧
	CheckOrigin: func(r *http.Request) bool { return true },
}

type authFrame struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

// Upgrade 升级 WS 连接
func Upgrade(c *gin.Context) (*websocket.Conn, error) {
	return upgrader.Upgrade(c.Writer, c.Request, nil)
}

// AuthFirstFrame 首帧 JSON 鉴权：{"type":"auth","token":"<access>"}，10s 超时
func AuthFirstFrame(conn *websocket.Conn) (*auth.Claims, error) {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	mt, data, err := conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("读取首帧失败: %w", err)
	}
	if mt != websocket.TextMessage {
		return nil, fmt.Errorf("首帧必须是 JSON 文本")
	}
	var frame authFrame
	if err := json.Unmarshal(data, &frame); err != nil || frame.Type != "auth" {
		return nil, fmt.Errorf("首帧格式错误")
	}
	claims, err := auth.Parse(frame.Token)
	if err != nil || claims.Type != auth.TokenAccess {
		return nil, fmt.Errorf("token 无效")
	}
	if auth.TokenBlocked(database.DB, claims) {
		return nil, fmt.Errorf("token 已吊销")
	}
	var u model.User
	if err := database.DB.First(&u, claims.UserID).Error; err != nil || u.Status != 1 {
		return nil, fmt.Errorf("用户不可用")
	}
	// 回传最新角色（角色变更后旧 token 里的角色可能过期）
	claims.Role = u.Role
	_ = conn.SetReadDeadline(time.Time{})
	return claims, nil
}
