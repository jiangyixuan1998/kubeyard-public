package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"kubeyard/internal/audit"
	"kubeyard/internal/auth"
	"kubeyard/internal/cryptox"
	"kubeyard/internal/middleware"
	"kubeyard/internal/model"
)

type ldapVO struct {
	model.LDAPConfig
	HasPassword bool `json:"has_password"`
}

func voOf(c model.LDAPConfig) ldapVO {
	return ldapVO{LDAPConfig: c, HasPassword: c.BindPassword != ""}
}

// ListLDAP GET /api/v1/admin/ldap
func (h *H) ListLDAP(c *gin.Context) {
	var cfgs []model.LDAPConfig
	h.db.Order("priority ASC, id ASC").Find(&cfgs)
	out := []ldapVO{}
	for _, c := range cfgs {
		out = append(out, voOf(c))
	}
	c.JSON(http.StatusOK, out)
}

type ldapReq struct {
	model.LDAPConfig
	BindPasswordPlain string `json:"bind_password"` // 明文，仅提交；空=不修改
}

// CreateLDAP POST /api/v1/admin/ldap
func (h *H) CreateLDAP(c *gin.Context) {
	var req ldapReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	cfg := req.LDAPConfig
	cfg.ID = 0
	cfg.BindPassword = ""
	if req.BindPasswordPlain != "" {
		cipher, err := cryptox.Encrypt([]byte(req.BindPasswordPlain))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "密码加密失败"})
			return
		}
		cfg.BindPassword = cipher
	}
	cfg.UpdatedBy = middleware.UID(c)
	if err := h.db.Create(&cfg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "保存失败: " + err.Error()})
		return
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "ldap_create", "", "", "", cfg.Name, "新增 LDAP 配置", "success"))
	c.JSON(http.StatusOK, voOf(cfg))
}

// UpdateLDAP PUT /api/v1/admin/ldap/:id — bind_password 留空则保留原密文
func (h *H) UpdateLDAP(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var cfg model.LDAPConfig
	if err := h.db.First(&cfg, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "配置不存在"})
		return
	}
	var req ldapReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	oldCipher := cfg.BindPassword
	oldSnapshot := cfg // 审计用：修改前快照
	cfg = req.LDAPConfig
	cfg.ID = uint(id)
	cfg.BindPassword = oldCipher
	if req.BindPasswordPlain != "" {
		cipher, err := cryptox.Encrypt([]byte(req.BindPasswordPlain))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "密码加密失败"})
			return
		}
		cfg.BindPassword = cipher
	}
	cfg.UpdatedBy = middleware.UID(c)
	if err := h.db.Save(&cfg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "保存失败: " + err.Error()})
		return
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "ldap_update", "", "", "", cfg.Name,
		ldapDiffDetail(&oldSnapshot, &cfg, req.BindPasswordPlain != ""), "success"))
	c.JSON(http.StatusOK, voOf(cfg))
}

// DeleteLDAP DELETE /api/v1/admin/ldap/:id
func (h *H) DeleteLDAP(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var cfg model.LDAPConfig
	if err := h.db.First(&cfg, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "配置不存在"})
		return
	}
	h.db.Delete(&cfg)
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "ldap_delete", "", "", "", cfg.Name,
		fmt.Sprintf("删除 LDAP 配置（%s:%d，BaseDN %s，默认角色 %s）", cfg.Host, cfg.Port, cfg.UserBaseDN, cfg.DefaultRole), "success"))
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// ldapDiffDetail LDAP 配置变更差异（审计详情用）：只列出变化的关键字段，不记密码
func ldapDiffDetail(before, after *model.LDAPConfig, pwChanged bool) string {
	var diff []string
	pairs := []struct {
		name          string
		before, after any
	}{
		{"启用", before.Enabled, after.Enabled},
		{"地址", fmt.Sprintf("%s:%d", before.Host, before.Port), fmt.Sprintf("%s:%d", after.Host, after.Port)},
		{"加密", before.Security, after.Security},
		{"BindDN", before.BindDN, after.BindDN},
		{"用户 BaseDN", before.UserBaseDN, after.UserBaseDN},
		{"用户过滤器", before.UserFilter, after.UserFilter},
		{"名称属性", before.AttrUsername, after.AttrUsername},
		{"默认角色", before.DefaultRole, after.DefaultRole},
		{"优先级", before.Priority, after.Priority},
	}
	for _, p := range pairs {
		if fmt.Sprint(p.before) != fmt.Sprint(p.after) {
			diff = append(diff, fmt.Sprintf("%s: %v → %v", p.name, p.before, p.after))
		}
	}
	if pwChanged {
		diff = append(diff, "Bind 密码: 已重置")
	}
	if len(diff) == 0 {
		return "无字段变化"
	}
	return "变更明细:\n - " + strings.Join(diff, "\n - ")
}

// TestLDAP POST /api/v1/admin/ldap/test — 连通性测试 + 可选真实登录测试
func (h *H) TestLDAP(c *gin.Context) {
	var req struct {
		ID               uint   `json:"id"` // 传 id 且密码留空 → 用库里已存密码
		Name             string `json:"name"`
		Host             string `json:"host"`
		Port             int    `json:"port"`
		Security         string `json:"security"`
		SkipTLSVerify    bool   `json:"skip_tls_verify"`
		BindDN           string `json:"bind_dn"`
		BindPassword     string `json:"bind_password"`
		UserBaseDN       string `json:"user_base_dn"`
		UserFilter       string `json:"user_filter"`
		SearchScope      string `json:"search_scope"`
		AttrUsername     string `json:"attr_username"`
		AttrDisplayName  string `json:"attr_display_name"`
		AttrEmail        string `json:"attr_email"`
		TestUsername     string `json:"test_username"`
		TestUserPassword string `json:"test_user_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	cfg := model.LDAPConfig{
		Name:            req.Name,
		Host:            req.Host,
		Port:            req.Port,
		Security:        req.Security,
		SkipTLSVerify:   req.SkipTLSVerify,
		BindDN:          req.BindDN,
		UserBaseDN:      req.UserBaseDN,
		UserFilter:      req.UserFilter,
		SearchScope:     req.SearchScope,
		AttrUsername:    req.AttrUsername,
		AttrDisplayName: req.AttrDisplayName,
		AttrEmail:       req.AttrEmail,
	}
	bindPW := req.BindPassword
	if bindPW == "" && req.ID > 0 {
		var stored model.LDAPConfig
		if err := h.db.First(&stored, req.ID).Error; err == nil && stored.BindPassword != "" {
			if raw, e := cryptox.Decrypt(stored.BindPassword); e == nil {
				bindPW = string(raw)
			}
		}
	}
	steps := auth.LDAPTest(&cfg, bindPW, req.TestUsername)
	allOK := true
	for _, s := range steps {
		if !s.OK {
			allOK = false
		}
	}
	// 可选：真实用户登录验证（不落库）
	if allOK && req.TestUsername != "" && req.TestUserPassword != "" {
		lu, err := auth.LDAPAuthenticate(&cfg, bindPW, req.TestUsername, req.TestUserPassword)
		if err != nil {
			steps = append(steps, auth.LDAPTestStep{Name: "用户登录测试", OK: false, Message: err.Error()})
			allOK = false
		} else {
			detail := lu.Username
			if lu.DisplayName != "" {
				detail += " / " + lu.DisplayName
			}
			if lu.Email != "" {
				detail += " / " + lu.Email
			}
			steps = append(steps, auth.LDAPTestStep{Name: "用户登录测试", OK: true, Message: "认证成功: " + detail})
		}
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "ldap_test", "", "", "", req.Host, "", boolStatus(allOK)))
	c.JSON(http.StatusOK, gin.H{"steps": steps, "all_ok": allOK})
}

func boolStatus(ok bool) string {
	if ok {
		return "success"
	}
	return "failed"
}
