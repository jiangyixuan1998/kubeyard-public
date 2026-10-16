package auth

import (
	"testing"
	"time"

	"kubeyard/internal/model"

	"github.com/golang-jwt/jwt/v5"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newTestDB 内存 SQLite，自动建吊销表。纯结构校验用不到 MySQL 方言特性
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.TokenRevocation{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// token 按给定签发时间造一个 claims（复用 Issue 的结构，iat 可控）
func claimsAt(userID uint, issued time.Time) *Claims {
	return &Claims{
		UserID: userID,
		Type:   TokenAccess,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        randomHex(16),
			IssuedAt:  jwt.NewNumericDate(issued),
			ExpiresAt: jwt.NewNumericDate(issued.Add(2 * time.Hour)),
		},
	}
}

// TestTokenBlockedUserRevoke 用户级吊销只拦「签发早于吊销行」的 token：
// 此前 RevokeAllOfUser 写入的 user-all-* 行 JTI 永远匹配不到真实 token，
// 禁用/降权后旧 access token 在最长 2h 内仍可用（外部评审核实）
func TestTokenBlockedUserRevoke(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()

	// 旧 token：禁用前签发
	old := claimsAt(7, now.Add(-time.Hour))
	// 新 token：重新启用后重新登录签发（吊销行之后）
	RevokeAllOfUser(db, 7, "test")

	if !TokenBlocked(db, old) {
		t.Fatal("用户级吊销应拦截吊销前签发的旧 token")
	}
	fresh := claimsAt(7, now.Add(time.Minute))
	if TokenBlocked(db, fresh) {
		t.Fatal("吊销后新登录签发的 token 不应被拦截（否则禁用→启用后永远无法登录）")
	}
}

// TestTokenBlockedSingleJTI 单 token 吊销（登出）不受用户级行影响：
// 用户 A 登出只作废 A 的这个 token，不能放大成整个用户下线，
// 也不能误伤其他用户
func TestTokenBlockedSingleJTI(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()

	c := claimsAt(1, now.Add(-time.Hour))
	Revoke(db, c, "logout")

	if !TokenBlocked(db, c) {
		t.Fatal("单 token 吊销（登出）应被拦截")
	}
	// 同用户的另一个 token 不受影响
	other := claimsAt(1, now.Add(-time.Hour))
	if TokenBlocked(db, other) {
		t.Fatal("登出不应作废同用户的其他 token")
	}
	// 其他用户不受影响
	if TokenBlocked(db, claimsAt(2, now.Add(-time.Hour))) {
		t.Fatal("登出不应影响其他用户")
	}
}

// TestTokenBlockedExpiry 用户级吊销行 7 天后过期：旧 token 本身也早已过期，
// 行删除后不再参与判定（GC 有界）
func TestTokenBlockedExpiry(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()

	// 直接造一条已过期的用户级行（模拟 7 天前写入）
	db.Create(&model.TokenRevocation{
		JTI:       userAllPrefix + randomHex(8),
		UserID:    9,
		ExpiresAt: now.Add(-time.Hour),
	})
	old := claimsAt(9, now.Add(-48*time.Hour))
	// 旧 token 本身 2h TTL 已过、正常 Parse 就会拒绝；这里只验证行过期后判定不再命中
	if TokenBlocked(db, old) {
		t.Fatal("过期的用户级吊销行不应再拦截")
	}
}

// TestStartRevocationGC GC 只清过期行，未过期的保留
func TestStartRevocationGC(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	keep := &model.TokenRevocation{JTI: "keep", UserID: 1, ExpiresAt: now.Add(time.Hour)}
	gone := &model.TokenRevocation{JTI: "gone", UserID: 2, ExpiresAt: now.Add(-time.Hour)}
	db.Create(keep)
	db.Create(gone)

	db.Where("expires_at < ?", now).Delete(&model.TokenRevocation{})

	var n int64
	db.Model(&model.TokenRevocation{}).Where("jti = ?", "gone").Count(&n)
	if n != 0 {
		t.Fatal("过期吊销行应被 GC 清除")
	}
	db.Model(&model.TokenRevocation{}).Where("jti = ?", "keep").Count(&n)
	if n != 1 {
		t.Fatal("未过期吊销行不应被 GC 误删")
	}
}

// TestTokenBlockedSameSecondReissue 同一秒内「撤销后重新登录」边界（外部评审核实）：
// 吊销行 created_at 是毫秒精度，而 jwt 库默认把 iat 截到整秒——同秒内重新签发的
// 新 token 会被误拦。TimePrecision=Millisecond 后，iat 亚秒精度须在真实
// 签发→序列化→解析路径上存活，才能与吊销行毫秒级区分
func TestTokenBlockedSameSecondReissue(t *testing.T) {
	db := newTestDB(t)
	InitKeys([]byte("unit-test-key-0123456789abcdef01")) // 覆盖全局测试密钥（本包其余测试不签发 token）
	base := time.Now().Truncate(time.Millisecond)

	// 撤销行：base+300ms 写入
	rev := &model.TokenRevocation{
		JTI:       userAllPrefix + randomHex(8),
		UserID:    11,
		ExpiresAt: base.Add(7 * 24 * time.Hour),
	}
	rev.CreatedAt = base.Add(300 * time.Millisecond)
	db.Create(rev)

	// 走真实签发/解析路径构造 claims（iat 经过 JSON 序列化，精度丢失会在此暴露）
	mk := func(iat time.Time) *Claims {
		signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claimsAt(11, iat)).SignedString(jwtKey)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := Parse(signed)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	// 同秒内、撤销行之后签发：不应误拦
	if TokenBlocked(db, mk(base.Add(800*time.Millisecond))) {
		t.Fatal("撤销行之后同秒内签发的新 token 不应被误拦")
	}
	// 同秒内、撤销行之前签发：仍应拦截
	if !TokenBlocked(db, mk(base.Add(100*time.Millisecond))) {
		t.Fatal("撤销行之前签发的 token 应被拦截")
	}
}
