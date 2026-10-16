package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	TokenAccess  = "access"
	TokenRefresh = "refresh"
)

var jwtKey []byte

func init() {
	// iat/exp 序列化精度提升到毫秒（RFC 7519 允许小数秒；旧整秒 token 解析不受影响）。
	// 库默认截到整秒，而吊销行 created_at 是毫秒精度——同一秒内「撤销后重新登录」
	// 签发的新 token iat 仍早于吊销行，会被 TokenBlocked 误拦（外部评审核实）
	jwt.TimePrecision = time.Millisecond
}

func InitKeys(k []byte) {
	if len(k) < 32 {
		panic("auth: JWT 密钥至少 32 字节")
	}
	jwtKey = k[:32]
}

type Claims struct {
	UserID   uint   `json:"uid"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Type     string `json:"typ"` // access / refresh
	jwt.RegisteredClaims
}

// Issue 签发 token：access 2h / refresh 7d，返回 (token, jti, error)
func Issue(userID uint, username, role, typ string) (string, string, error) {
	ttl := 2 * time.Hour
	if typ == TokenRefresh {
		ttl = 7 * 24 * time.Hour
	}
	jti := randomHex(16)
	c := Claims{
		UserID:   userID,
		Username: username,
		Role:     role,
		Type:     typ,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Subject:   fmt.Sprintf("%d", userID),
			Issuer:    "kubeyard",
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(jwtKey)
	return signed, jti, err
}

func Parse(tokenStr string) (*Claims, error) {
	c := &Claims{}
	t, err := jwt.ParseWithClaims(tokenStr, c, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("非法签名算法")
		}
		return jwtKey, nil
	})
	if err != nil || !t.Valid {
		return nil, errors.New("token 无效或已过期")
	}
	return c, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
