package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// 回归：批量重评路由必须命中（固定路径优先于 :id 参数路由——注册顺序已保证）
func TestBatchReEvalRouteRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/admin/exec-recordings/risk-reeval-batch", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	r.POST("/admin/exec-recordings/:id/risk-reeval", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"id": c.Param("id")})
	})
	// 批量路径不被 :id 吃掉
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/exec-recordings/risk-reeval-batch", strings.NewReader(`{"ids":[1]}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("batch route mismatched: %d %s", w.Code, w.Body.String())
	}
}
