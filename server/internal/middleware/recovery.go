package middleware

import (
	"fmt"
	"log"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
)

// PanicRecovery panic 兜底（替换 gin.Recovery）：堆栈进服务日志（log.Printf，随容器日志采集，
// gin 自带的只写 stderr 且 500 响应体为空）；响应带 message 的 JSON，前端能弹出可诊断的报错
// 而不是静默失败。已在 handler 内写过响应的（如 WS 升级中途 panic）只记日志不再补写。
func PanicRecovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("[kubeyard] panic %s %s: %v\n%s",
					c.Request.Method, c.Request.URL.Path, err, debug.Stack())
				if !c.Writer.Written() {
					c.AbortWithStatusJSON(http.StatusInternalServerError,
						gin.H{"message": fmt.Sprintf("服务器内部错误: %v", err)})
				}
			}
		}()
		c.Next()
	}
}
