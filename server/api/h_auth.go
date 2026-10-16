package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"kubeyard/internal/audit"
	"kubeyard/internal/auth"
	"kubeyard/internal/middleware"
	"kubeyard/internal/model"
)

type loginReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// Login POST /api/v1/auth/login
func (h *H) Login(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	// 仅 SSO 模式：密码登录整体关闭，本地超管保留密码兜底（防认证中心故障锁死平台）
	if h.ssoOnlyActive() {
		var u model.User
		if err := h.db.Where("username = ? AND source = ? AND role = ?", req.Username, "local", model.RoleSuperAdmin).First(&u).Error; err != nil {
			c.JSON(http.StatusForbidden, gin.H{"message": "平台已启用统一认证登录，请使用 SSO 登录"})
			return
		}
	}
	res, err := auth.Login(h.db, req.Username, req.Password, c.ClientIP())
	status := "success"
	detail := ""
	if err != nil {
		status, detail = "failed", err.Error()
	}
	audit.Log(audit.Record(0, req.Username, "login", "", "", "", "", detail, status))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"access_token":  res.AccessToken,
		"refresh_token": res.RefreshToken,
		"user":          res.User,
	})
}

type refreshReq struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// Refresh POST /api/v1/auth/refresh
func (h *H) Refresh(c *gin.Context) {
	var req refreshReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	claims, err := auth.Parse(req.RefreshToken)
	if err != nil || claims.Type != auth.TokenRefresh || auth.TokenBlocked(h.db, claims) {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "刷新令牌无效"})
		return
	}
	var user model.User
	if err := h.db.First(&user, claims.UserID).Error; err != nil || user.Status != 1 {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "用户不存在或已禁用"})
		return
	}
	// 到期只拦服务账号；个人账号到期不阻断，权限随授权到期消失
	if user.Expired() && user.AccountType == "service" {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "账号已过期，请申请续期"})
		return
	}
	// 旋转：旧的 refresh 作废，发新对
	auth.Revoke(h.db, claims, "refresh-rotate")
	access, _, err := auth.Issue(user.ID, user.Username, user.Role, auth.TokenAccess)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "签发失败"})
		return
	}
	refresh, _, err := auth.Issue(user.ID, user.Username, user.Role, auth.TokenRefresh)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "签发失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"access_token": access, "refresh_token": refresh})
}

// Logout POST /api/v1/auth/logout
func (h *H) Logout(c *gin.Context) {
	jti := c.GetString("jti")
	claims, err := auth.Parse(currentBearer(c))
	if err == nil {
		auth.Revoke(h.db, claims, "logout")
	}
	_ = jti
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "logout", "", "", "", "", "", "success"))
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// Me GET /api/v1/auth/me
func (h *H) Me(c *gin.Context) {
	var user model.User
	if err := h.db.First(&user, middleware.UID(c)).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "用户不存在"})
		return
	}
	c.JSON(http.StatusOK, user)
}

func currentBearer(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if len(h) > 7 && h[:7] == "Bearer " {
		return h[7:]
	}
	return ""
}
