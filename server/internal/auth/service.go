package auth

import (
	"errors"
	"sync"
	"time"

	"kubeyard/internal/cryptox"
	"kubeyard/internal/model"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var ErrInvalidCredential = errors.New("用户名或密码错误")

type LoginResult struct {
	User         *model.User
	AccessToken  string
	RefreshToken string
}

// ---------- 登录失败锁定（内存实现，单实例足够）----------
type failState struct {
	count int
	until time.Time
}

var (
	failMu sync.Mutex
	fails  = map[string]*failState{}
)

const (
	maxFailCount = 5
	lockDuration = 10 * time.Minute
)

func loginLocked(key string) bool {
	failMu.Lock()
	defer failMu.Unlock()
	s, ok := fails[key]
	if !ok {
		return false
	}
	// 仅在达到阈值后检查/清理锁定窗口。此前无条件比较 until——未达阈值时 until 是
	// 零值，now.After(零值) 恒真把累计状态删掉，失败计数永远停在 1，锁定从未生效
	//（外部评审核实）
	if s.count >= maxFailCount {
		if time.Now().Before(s.until) {
			return true
		}
		delete(fails, key) // 锁定窗口已过：清零重新计数
	}
	return false
}

func recordFail(key string) {
	failMu.Lock()
	defer failMu.Unlock()
	s, ok := fails[key]
	if !ok {
		s = &failState{}
		fails[key] = s
	}
	// 已在锁定窗口内继续失败：窗口顺延（防止锁定过期瞬间再试错重新累计）
	if s.count >= maxFailCount && time.Now().Before(s.until) {
		s.until = time.Now().Add(lockDuration)
		return
	}
	s.count++
	if s.count >= maxFailCount {
		s.until = time.Now().Add(lockDuration)
	}
}

func clearFail(key string) {
	failMu.Lock()
	defer failMu.Unlock()
	delete(fails, key)
}

// Login 统一登录入口：本地账号优先，未命中走 LDAP（按优先级），成功自动开户
func Login(db *gorm.DB, username, password, ip string) (*LoginResult, error) {
	key := username + "|" + ip
	if loginLocked(key) {
		return nil, errors.New("失败次数过多，账号已临时锁定，请 10 分钟后重试")
	}

	var user model.User
	err := db.Where("username = ?", username).First(&user).Error
	if err == nil {
		if user.Status != 1 {
			return nil, errors.New("账号已被禁用")
		}
		// 账号到期只拦服务账号（程序调用需要明确失败）；个人账号到期不阻断登录，
		// 权限随授权/权限组到期自动消失（登录后看不到任何资源）
		if user.Expired() && user.AccountType == "service" {
			return nil, errors.New("账号已过期，请联系管理员续期")
		}
		if user.Source == "local" {
			if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) == nil {
				clearFail(key)
				return issueTokens(db, &user)
			}
			recordFail(key)
			return nil, ErrInvalidCredential
		}
	}

	// LDAP 通道
	var cfgs []model.LDAPConfig
	if err := db.Where("enabled = ?", true).Order("priority ASC").Find(&cfgs).Error; err == nil {
		for i := range cfgs {
			c := &cfgs[i]
			bindPW := ""
			if c.BindPassword != "" {
				if raw, e := cryptox.Decrypt(c.BindPassword); e == nil {
					bindPW = string(raw)
					// 历史密文读取成功后自动转存明文（一次性迁移，之后不再依赖密钥）
					if bindPW != c.BindPassword {
						db.Model(c).Update("bind_password", bindPW)
					}
				}
			}
			lu, e := LDAPAuthenticate(c, bindPW, username, password)
			if e != nil {
				continue
			}
			// 首次登录自动开户
			if err := db.Where("username = ?", username).First(&user).Error; err != nil {
				user = model.User{
					Username:    username,
					DisplayName: lu.DisplayName,
					Email:       lu.Email,
					Source:      "ldap",
					Role:        c.DefaultRole,
					Status:      1,
					LastLoginAt: nowPtr(),
				}
				if err := db.Create(&user).Error; err != nil {
					return nil, err
				}
			} else {
				user.DisplayName = orDefault(lu.DisplayName, user.DisplayName)
				user.Email = orDefault(lu.Email, user.Email)
				user.Source = "ldap"
				user.Status = 1
				user.LastLoginAt = nowPtr()
				db.Save(&user)
			}
			clearFail(key)
			return issueTokens(db, &user)
		}
	}

	recordFail(key)
	return nil, ErrInvalidCredential
}

func issueTokens(db *gorm.DB, u *model.User) (*LoginResult, error) {
	access, _, err := Issue(u.ID, u.Username, u.Role, TokenAccess)
	if err != nil {
		return nil, err
	}
	refresh, _, err := Issue(u.ID, u.Username, u.Role, TokenRefresh)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	u.LastLoginAt = &now
	db.Model(u).Update("last_login_at", now)
	return &LoginResult{User: u, AccessToken: access, RefreshToken: refresh}, nil
}

// IssueTokens SSO 等免密通道认证成功后签发平台自身 JWT（与密码登录同一登录态）
func IssueTokens(db *gorm.DB, u *model.User) (*LoginResult, error) {
	return issueTokens(db, u)
}

// Revoke 吊销指定 token（登出 / 禁用用户 / 改授权）
func Revoke(db *gorm.DB, claims *Claims, reason string) {
	db.Create(&model.TokenRevocation{
		JTI:       claims.ID,
		UserID:    claims.UserID,
		Reason:    reason,
		ExpiresAt: claims.ExpiresAt.Time,
	})
}

// userAllPrefix 用户级吊销行的 JTI 前缀：与单 token 行区分（单 token 行按 JTI
// 精确匹配，用户级行按 user_id 匹配，混用会把单次登出放大成全用户下线）
const userAllPrefix = "user-all-"

// RevokeAllOfUser 用户级吊销：该用户全部已签发 token 立即失效（禁用/改角色/
// 改授权时）。行里 ExpiresAt = +7d 只是 GC 兜底（7d 后所有旧 token 自身也已
// 过期）；实际判定在 TokenBlocked——只拦「签发早于本行」的 token，用户重新
// 启用后再登录拿到的新 token（iat 晚于本行）不受影响
func RevokeAllOfUser(db *gorm.DB, userID uint, reason string) {
	db.Create(&model.TokenRevocation{
		JTI:       userAllPrefix + randomHex(8),
		UserID:    userID,
		Reason:    reason,
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	})
}

// TokenBlocked 请求鉴权统一入口：单 token 吊销（JTI 精确命中），或用户级吊销
// （user-all- 行未过期、且 token 签发早于该行——后签发的 token 是重新启用/
// 重新登录后的新会话，不在打击面内）。此前用户级吊销写入的行永远匹配不到
// 任何真实 token 的 JTI，禁用/降权后旧 access token 在最长 2h 内仍可用
func TokenBlocked(db *gorm.DB, claims *Claims) bool {
	issued := time.Time{}
	if claims.IssuedAt != nil {
		issued = claims.IssuedAt.Time
	}
	var n int64
	db.Model(&model.TokenRevocation{}).
		Where("jti = ? OR (user_id = ? AND jti LIKE ? AND created_at > ? AND expires_at > ?)",
			claims.ID, claims.UserID, userAllPrefix+"%", issued, time.Now()).
		Count(&n)
	return n > 0
}

// StartRevocationGC 每小时清理过期吊销行（单 token 行的 ExpiresAt = token 自身
// 过期时间，用户级行 = 写入 +7d），防表无限增长；启动时先清一次存量
func StartRevocationGC(db *gorm.DB) {
	db.Where("expires_at < ?", time.Now()).Delete(&model.TokenRevocation{})
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for range t.C {
		db.Where("expires_at < ?", time.Now()).Delete(&model.TokenRevocation{})
	}
}

func nowPtr() *time.Time {
	t := time.Now()
	return &t
}
