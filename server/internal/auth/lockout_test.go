package auth

import (
	"testing"
	"time"
)

// TestLoginLockout 登录失败锁定：连续失败达阈值后锁定，窗口内拒绝；
// 窗口过后重新计数。此前 loginLocked 未达阈值时因 until 零值把累计状态删掉，
// 失败计数永远停在 1，锁定从未生效（外部评审核实）
func TestLoginLockout(t *testing.T) {
	key := "lockuser|1.2.3.4"
	defer delete(fails, key)

	// 4 次失败（阈值 5）：不应锁定
	for i := 0; i < maxFailCount-1; i++ {
		recordFail(key)
	}
	if loginLocked(key) {
		t.Fatal("未达阈值不应锁定")
	}
	// 第 5 次失败：触发锁定
	recordFail(key)
	if !loginLocked(key) {
		t.Fatal("达到阈值应锁定")
	}

	// 锁定窗口内继续失败：窗口顺延、不重新计数
	until := fails[key].until
	recordFail(key)
	if fails[key].count != maxFailCount {
		t.Fatalf("锁定窗口内失败不应重置计数: got %d", fails[key].count)
	}
	// 窗口顺延 = 不早于原窗口（Windows 时钟粒度下两次 time.Now 可能同值，用 >= 语义）
	if fails[key].until.Before(until) {
		t.Fatal("锁定窗口内继续失败应顺延窗口")
	}
	if !loginLocked(key) {
		t.Fatal("窗口顺延后仍应处于锁定")
	}

	// 窗口过后：解锁并清零（下次失败从 1 重新累计）
	fails[key].until = time.Now().Add(-time.Second)
	if loginLocked(key) {
		t.Fatal("锁定窗口已过应解锁")
	}
	if _, ok := fails[key]; ok {
		t.Fatal("窗口已过应清零状态重新计数")
	}
}

// TestLoginLockoutPerKey 不同用户/IP 的失败计数互不影响
func TestLoginLockoutPerKey(t *testing.T) {
	k1, k2 := "usera|1.1.1.1", "usera|2.2.2.2"
	defer delete(fails, k1)
	defer delete(fails, k2)

	for i := 0; i < maxFailCount; i++ {
		recordFail(k1)
	}
	if !loginLocked(k1) {
		t.Fatal("k1 应锁定")
	}
	if loginLocked(k2) {
		t.Fatal("k2 不应受 k1 影响")
	}
}

// TestClearFailOnSuccess 成功登录清零失败计数
func TestClearFailOnSuccess(t *testing.T) {
	key := "clearuser|1.2.3.4"
	defer delete(fails, key)

	recordFail(key)
	recordFail(key)
	clearFail(key)
	if loginLocked(key) {
		t.Fatal("清零后不应锁定")
	}
	if _, ok := fails[key]; ok {
		t.Fatal("clearFail 应删除状态")
	}
}
