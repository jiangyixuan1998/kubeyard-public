package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"kubeyard/internal/audit"
	"kubeyard/internal/auth"
	"kubeyard/internal/middleware"
	"kubeyard/internal/model"
)

// ---------- SSO 统一认证（OAuth 2.0 授权码，对接企业认证中心）----------
//
// 流程（手册《oauth2.0应用集成手册》）：
//  1. 浏览器 GET /auth/oauth/start → 302 到认证中心 authorize（client_id/redirect_uri/response_type=code/state）
//  2. 认证中心登录后带 code 回调 /auth/oauth/callback
//  3. 服务端 code 换 access_token（client_secret 仅此一步使用，浏览器不可见）
//  4. access_token 换 userinfo，按 AD 账号匹配/开户 users 表，签发平台自身 JWT
//  5. 302 到前端 /sso-callback#access_token=..&refresh_token=..（fragment 不进服务器日志）
//
// 与本地/LDAP 密码登录并行共存，切换到「仅 SSO」只改配置开关（OnlySSO，本地超管保留密码兜底）。

const oauthStateCookie = "ky_oauth_state"

type oauthVO struct {
	model.OAuthConfig
	HasSecret bool `json:"has_secret"` // 前端展示「已配置/未配置」，密钥本身永不回显
}

// oauthCfg 取唯一一条 SSO 配置；空字段回落手册默认（gorm default 只在插入时生效，
// 首次打开管理页/未保存前也要能给出可用的测试环境地址），ID=0 表示尚未保存过
func (h *H) oauthCfg() model.OAuthConfig {
	var cfg model.OAuthConfig
	h.db.First(&cfg)
	if cfg.Name == "" {
		cfg.Name = "企业统一认证"
	}
	if cfg.AuthorizeURL == "" {
		cfg.AuthorizeURL = "https://sso.example.com/openapi/oauth/authorize"
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = "https://sso.example.com/openapi/oauth/token"
	}
	if cfg.UserInfoURL == "" {
		cfg.UserInfoURL = "https://sso.example.com/openapi/oauth/userinfo"
	}
	if cfg.UsernameField == "" {
		cfg.UsernameField = "yumADAccount"
	}
	if cfg.DefaultRole == "" {
		cfg.DefaultRole = "Viewer"
	}
	// 兜底：库里已有脏数据（带首尾空格）时读取也裁掉，避免 %20 域名
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	cfg.AuthorizeURL = strings.TrimSpace(cfg.AuthorizeURL)
	cfg.TokenURL = strings.TrimSpace(cfg.TokenURL)
	cfg.UserInfoURL = strings.TrimSpace(cfg.UserInfoURL)
	cfg.RedirectURI = strings.TrimSpace(cfg.RedirectURI)
	cfg.UsernameField = strings.TrimSpace(cfg.UsernameField)
	return cfg
}

// GetOAuthConfig GET /api/v1/admin/oauth
func (h *H) GetOAuthConfig(c *gin.Context) {
	cfg := h.oauthCfg()
	c.JSON(http.StatusOK, oauthVO{OAuthConfig: cfg, HasSecret: cfg.ClientSecret != ""})
}

// PutOAuthConfig PUT /api/v1/admin/oauth — 单条配置 upsert；client_secret 留空则保留原值
func (h *H) PutOAuthConfig(c *gin.Context) {
	var req struct {
		Name          string `json:"name"`
		Enabled       bool   `json:"enabled"`
		ClientID      string `json:"client_id"`
		ClientSecret  string `json:"client_secret"`
		AuthorizeURL  string `json:"authorize_url"`
		TokenURL      string `json:"token_url"`
		UserInfoURL   string `json:"userinfo_url"`
		RedirectURI   string `json:"redirect_uri"`
		UsernameField string `json:"username_field"`
		DefaultRole   string `json:"default_role"`
		AutoCreate    bool   `json:"auto_create"`
		OnlySSO       bool   `json:"only_sso"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	// 粘贴带入的首尾空白会被 URL 编码成 %20 污染回调域名，存库前统一裁掉
	req.ClientID = strings.TrimSpace(req.ClientID)
	req.AuthorizeURL = strings.TrimSpace(req.AuthorizeURL)
	req.TokenURL = strings.TrimSpace(req.TokenURL)
	req.UserInfoURL = strings.TrimSpace(req.UserInfoURL)
	req.RedirectURI = strings.TrimSpace(req.RedirectURI)
	req.UsernameField = strings.TrimSpace(req.UsernameField)
	if req.Enabled && (req.ClientID == "" || req.RedirectURI == "") {
		c.JSON(http.StatusBadRequest, gin.H{"message": "启用前需填写 Client ID 与回调地址"})
		return
	}
	cfg := h.oauthCfg()
	cfg.Name = orDefaultStr(req.Name, cfg.Name)
	cfg.Enabled = req.Enabled
	cfg.ClientID = req.ClientID
	if req.ClientSecret != "" {
		cfg.ClientSecret = req.ClientSecret
	}
	cfg.AuthorizeURL = orDefaultStr(req.AuthorizeURL, cfg.AuthorizeURL)
	cfg.TokenURL = orDefaultStr(req.TokenURL, cfg.TokenURL)
	cfg.UserInfoURL = orDefaultStr(req.UserInfoURL, cfg.UserInfoURL)
	cfg.RedirectURI = req.RedirectURI
	cfg.UsernameField = orDefaultStr(req.UsernameField, cfg.UsernameField)
	if req.DefaultRole != "" {
		cfg.DefaultRole = req.DefaultRole
	}
	cfg.AutoCreate = req.AutoCreate
	// 阶段二开关只能在 SSO 已启用时打开，避免无可用通道
	cfg.OnlySSO = req.OnlySSO && cfg.Enabled
	cfg.UpdatedBy = middleware.UID(c)
	if cfg.ID == 0 {
		if err := h.db.Create(&cfg).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "保存失败: " + err.Error()})
			return
		}
	} else if err := h.db.Save(&cfg).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "保存失败: " + err.Error()})
		return
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "oauth_config", "", "", "", cfg.Name,
		fmt.Sprintf("SSO 统一认证配置已保存（enabled=%v only_sso=%v auto_create=%v）", cfg.Enabled, cfg.OnlySSO, cfg.AutoCreate), "success"))
	c.JSON(http.StatusOK, oauthVO{OAuthConfig: cfg, HasSecret: cfg.ClientSecret != ""})
}

func orDefaultStr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// OAuthStatus GET /api/v1/auth/oauth/status — 登录页展示 SSO 按钮 / 隐藏密码表单用（公开）
func (h *H) OAuthStatus(c *gin.Context) {
	cfg := h.oauthCfg()
	c.JSON(http.StatusOK, gin.H{"enabled": cfg.Enabled && cfg.ID != 0, "only_sso": cfg.OnlySSO, "name": cfg.Name})
}

// OAuthStart GET /api/v1/auth/oauth/start — 生成 state 防 CSRF，302 到认证中心授权页
func (h *H) OAuthStart(c *gin.Context) {
	cfg := h.oauthCfg()
	if cfg.ID == 0 || !cfg.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{"message": "SSO 统一认证未启用"})
		return
	}
	state := randomPassword() // h_user.go 的随机 hex，兼作一次性 nonce
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(oauthStateCookie, state, 600, "/api/v1/auth/oauth/callback", "", false, true)
	q := url.Values{}
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", cfg.RedirectURI)
	q.Set("response_type", "code")
	q.Set("scope", "all")
	q.Set("state", state)
	q.Set("oauth_timestamp", strconv.FormatInt(time.Now().UnixNano()/int64(time.Millisecond), 10))
	sep := "?"
	if strings.Contains(cfg.AuthorizeURL, "?") {
		sep = "&"
	}
	c.Redirect(http.StatusFound, cfg.AuthorizeURL+sep+q.Encode())
}

// OAuthCallback GET /api/v1/auth/oauth/callback — code 换 token 换 userinfo，匹配/开户后签发平台 JWT
func (h *H) OAuthCallback(c *gin.Context) {
	fail := func(msg string) {
		c.Redirect(http.StatusFound, "/sso-callback#error="+url.QueryEscape(msg))
	}
	cfg := h.oauthCfg()
	if cfg.ID == 0 || !cfg.Enabled {
		fail("SSO 统一认证未启用")
		return
	}
	// state 校验（一次性，用完即清）
	if c.Query("state") == "" || c.Query("state") != getCookie(c, oauthStateCookie) {
		fail("登录会话校验失败，请重新发起 SSO 登录")
		return
	}
	c.SetCookie(oauthStateCookie, "", -1, "/api/v1/auth/oauth/callback", "", false, true)
	code := c.Query("code")
	if code == "" {
		fail("认证中心未返回授权码")
		return
	}

	// code 换 access_token（client_secret 仅服务端持有）
	accessToken, err := oauthExchange(cfg, code)
	if err != nil {
		fail("获取访问令牌失败: " + err.Error())
		return
	}
	// access_token 换 userinfo
	info, err := oauthUserInfo(cfg, accessToken)
	if err != nil {
		fail("获取用户信息失败: " + err.Error())
		return
	}
	username := pickOauthField(info, cfg.UsernameField, "yumADAccount", "uid", "username", "loginName", "account")
	if username == "" {
		// 诊断日志：userinfo 已拿到但没有任何账号字段。手册示例里未关联 AD 账号的用户
		// 各账号字段返回字符串 "null"（被按空处理），也会走到这里——提示用户侧问题。
		log.Printf("[oauth] userinfo 缺少账号字段（username_field=%s），实际字段: %s",
			cfg.UsernameField, oauthFieldNames(info))
		audit.Log(audit.Record(0, "", "login.sso", "", "", "", "",
			"SSO 登录失败：userinfo 缺少账号字段（字段: "+oauthFieldNames(info)+"）", "failed"))
		fail("认证中心返回的用户信息中没有可用的账号（AD 账号为空）。通常是该账号未在认证中心关联 AD 账号，请联系管理员确认；若字段名不一致，请检查 SSO 配置的「账号字段」")
		return
	}
	displayName := pickOauthField(info, "yumLocalName", "displayName", "name", "cn")
	email := pickOauthField(info, "mail", "email")

	// 匹配/开户：AD 账号与 LDAP 同源，老用户命中同一条记录，权限原样保留
	now := time.Now()
	var user model.User
	err = h.db.Where("username = ?", username).First(&user).Error
	if err != nil {
		if !cfg.AutoCreate {
			fail("账号 " + username + " 未在平台开通，请联系管理员")
			return
		}
		user = model.User{
			Username:    username,
			DisplayName: displayName,
			Email:       email,
			Source:      "sso",
			Role:        cfg.DefaultRole,
			Status:      1,
			LastLoginAt: &now,
		}
		if err := h.db.Create(&user).Error; err != nil {
			fail("自动开户失败: " + err.Error())
			return
		}
	} else {
		if user.Status != 1 {
			fail("账号已被禁用")
			return
		}
		// 到期只拦服务账号（与密码登录一致，防到期服务账号借同名 AD 账号走 SSO 绕过）；
		// 个人账号到期不阻断登录：SSO 正常放行，权限随授权/权限组到期自动消失（看不到任何资源）
		if user.Expired() && user.AccountType == "service" {
			fail("账号已过期，请联系管理员续期")
			return
		}
		if displayName != "" {
			user.DisplayName = displayName
		}
		if email != "" {
			user.Email = email
		}
		user.Source = "sso"
		user.LastLoginAt = &now
		h.db.Save(&user)
	}

	res, err := auth.IssueTokens(h.db, &user)
	status, detail := "success", "SSO 统一认证登录成功"
	if err != nil {
		status, detail = "failed", "签发令牌失败: "+err.Error()
	}
	audit.Log(audit.Record(0, username, "login.sso", "", "", "", "", detail, status))
	if err != nil {
		fail("登录失败: " + err.Error())
		return
	}
	c.Redirect(http.StatusFound, "/sso-callback#access_token="+url.QueryEscape(res.AccessToken)+
		"&refresh_token="+url.QueryEscape(res.RefreshToken))
}

// getCookie 读 cookie（不存在返回空串）
func getCookie(c *gin.Context, name string) string {
	v, err := c.Cookie(name)
	if err != nil {
		return ""
	}
	return v
}

// oauthExchange code 换 access_token（授权码换令牌，POST 表单，手册 2.1.2）
func oauthExchange(cfg model.OAuthConfig, code string) (string, error) {
	form := url.Values{}
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)
	form.Set("redirect_uri", cfg.RedirectURI)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("oauth_timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.PostForm(cfg.TokenURL, form)
	if err != nil {
		return "", fmt.Errorf("认证中心连接失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token 接口返回 %d: %s", resp.StatusCode, truncateStr(string(body), 200))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		Token       string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("token 响应解析失败")
	}
	if out.AccessToken != "" {
		return out.AccessToken, nil
	}
	return out.Token, nil // 部分实现两字段同值
}

// oauthUserInfo access_token 换用户信息（手册 2.1.3）
func oauthUserInfo(cfg model.OAuthConfig, accessToken string) (map[string]any, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(cfg.UserInfoURL + "?access_token=" + url.QueryEscape(accessToken))
	if err != nil {
		return nil, fmt.Errorf("认证中心连接失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo 接口返回 %d: %s", resp.StatusCode, truncateStr(string(body), 200))
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("userinfo 响应解析失败")
	}
	return out, nil
}

// oauthFieldNames 列出 userinfo 返回的全部字段名（按字母序，值不落日志——可能含姓名/工号等个人信息）
func oauthFieldNames(info map[string]any) string {
	names := make([]string, 0, len(info))
	for k := range info {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// pickOAuthField 依次尝试候选字段取第一个非空值；手册示例里空值会返回字符串 "null"，按空处理
func pickOauthField(info map[string]any, fields ...string) string {
	for _, f := range fields {
		if v, ok := info[f].(string); ok {
			v = strings.TrimSpace(v)
			if v != "" && v != "null" {
				return v
			}
		}
	}
	return ""
}

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ssoOnlyActive 仅 SSO 模式已开启（供密码登录通道做准入判断）
func (h *H) ssoOnlyActive() bool {
	cfg := h.oauthCfg()
	return cfg.ID != 0 && cfg.Enabled && cfg.OnlySSO
}
