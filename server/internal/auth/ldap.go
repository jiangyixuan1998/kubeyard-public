package auth

import (
	"crypto/tls"
	"errors"
	"fmt"
	"strings"

	"github.com/go-ldap/ldap/v3"
	"kubeyard/internal/model"
)

// LDAPUser LDAP 验证通过后提取的用户信息
type LDAPUser struct {
	Username    string
	DisplayName string
	Email       string
	DN          string
}

// LDAPTestStep 前端"测试连接"分步结果
type LDAPTestStep struct {
	Name    string   `json:"name"`
	OK      bool     `json:"ok"`
	Message string   `json:"message"`
	Details []string `json:"details,omitempty"`
}

func dialLDAP(c *model.LDAPConfig) (*ldap.Conn, error) {
	addr := fmt.Sprintf("%s:%d", c.Host, c.Port)
	tlsCfg := &tls.Config{InsecureSkipVerify: c.SkipTLSVerify} //nolint:gosec
	switch c.Security {
	case "ldaps":
		return ldap.DialURL("ldaps://"+addr, ldap.DialWithTLSConfig(tlsCfg))
	case "starttls":
		conn, err := ldap.DialURL("ldap://" + addr)
		if err != nil {
			return nil, err
		}
		if err := conn.StartTLS(tlsCfg); err != nil {
			conn.Close()
			return nil, fmt.Errorf("StartTLS 失败: %w", err)
		}
		return conn, nil
	default:
		return ldap.DialURL("ldap://" + addr)
	}
}

// LDAPAuthenticate 双 bind 验证：服务账号 bind → 搜索用户 → 用户 DN+密码二次 bind
func LDAPAuthenticate(c *model.LDAPConfig, bindPassword, username, password string) (*LDAPUser, error) {
	if c.Host == "" || c.UserBaseDN == "" {
		return nil, errors.New("LDAP 配置不完整")
	}
	conn, err := dialLDAP(c)
	if err != nil {
		return nil, fmt.Errorf("LDAP 连接失败: %w", err)
	}
	defer conn.Close()

	if err := conn.Bind(c.BindDN, bindPassword); err != nil {
		return nil, fmt.Errorf("服务账号绑定失败: %w", err)
	}

	filter := strings.ReplaceAll(c.UserFilter, "%s", ldap.EscapeFilter(username))
	scope := ldap.ScopeWholeSubtree
	if c.SearchScope == "one" {
		scope = ldap.ScopeSingleLevel
	}
	res, err := conn.Search(&ldap.SearchRequest{
		BaseDN:     c.UserBaseDN,
		Scope:      scope,
		Filter:     filter,
		Attributes: []string{c.AttrUsername, c.AttrDisplayName, c.AttrEmail},
		SizeLimit:  1,
	})
	if err != nil {
		return nil, fmt.Errorf("用户搜索失败（检查 BaseDN/过滤器）: %w", err)
	}
	if len(res.Entries) == 0 {
		return nil, errors.New("用户不存在")
	}
	entry := res.Entries[0]

	if err := conn.Bind(entry.DN, password); err != nil {
		return nil, errors.New("密码错误")
	}
	return &LDAPUser{
		Username:    orDefault(entry.GetAttributeValue(c.AttrUsername), username),
		DisplayName: entry.GetAttributeValue(c.AttrDisplayName),
		Email:       entry.GetAttributeValue(c.AttrEmail),
		DN:          entry.DN,
	}, nil
}

// LDAPTest 分步测试：连接 → 服务绑定 → 用户搜索；bindPassword 由调用方解析（明文或解密后）
func LDAPTest(c *model.LDAPConfig, bindPassword, testUsername string) []LDAPTestStep {
	steps := []LDAPTestStep{}

	conn, err := dialLDAP(c)
	if err != nil {
		return append(steps, LDAPTestStep{Name: "网络连接", OK: false, Message: err.Error()})
	}
	defer conn.Close()
	steps = append(steps, LDAPTestStep{Name: "网络连接", OK: true, Message: fmt.Sprintf("%s:%d (%s) 连接成功", c.Host, c.Port, c.Security)})

	if err := conn.Bind(c.BindDN, bindPassword); err != nil {
		return append(steps, LDAPTestStep{Name: "服务账号绑定", OK: false, Message: err.Error() + "（检查 Bind DN / 密码）"})
	}
	steps = append(steps, LDAPTestStep{Name: "服务账号绑定", OK: true, Message: "Bind 成功"})

	step := LDAPTestStep{Name: "用户搜索", OK: true, Message: "未提供测试用户名，跳过"}
	if testUsername != "" {
		filter := strings.ReplaceAll(c.UserFilter, "%s", ldap.EscapeFilter(testUsername))
		res, err := conn.Search(&ldap.SearchRequest{
			BaseDN:     c.UserBaseDN,
			Scope:      ldap.ScopeWholeSubtree,
			Filter:     filter,
			Attributes: []string{c.AttrUsername, c.AttrDisplayName, c.AttrEmail},
			SizeLimit:  1,
		})
		if err != nil {
			step.OK, step.Message = false, fmt.Sprintf("搜索失败: %v（检查 BaseDN / 过滤器）", err)
		} else if len(res.Entries) == 0 {
			step.OK, step.Message = false, "未匹配到用户（检查过滤器与账号名）"
		} else {
			e := res.Entries[0]
			step.Details = append(step.Details,
				"DN: "+e.DN,
				c.AttrUsername+": "+e.GetAttributeValue(c.AttrUsername),
				c.AttrDisplayName+": "+e.GetAttributeValue(c.AttrDisplayName),
				c.AttrEmail+": "+e.GetAttributeValue(c.AttrEmail),
			)
			step.Message = "匹配成功"
		}
	}
	return append(steps, step)
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
