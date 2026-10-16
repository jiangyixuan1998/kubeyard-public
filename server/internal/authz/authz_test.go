package authz

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"kubeyard/internal/model"
)

func newAuthzDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:authz_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.AutoMigrate(
		&model.Cluster{},
		&model.User{},
		&model.UserGrant{},
		&model.GrantGroup{},
		&model.GrantGroupRule{},
		&model.UserGroup{},
		&model.AccessProfile{},
		&model.AccessBinding{},
	)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func statsDelta(t *testing.T, run func()) Stats {
	t.Helper()
	before := GetStats()
	run()
	after := GetStats()
	return Stats{
		Checks:     after.Checks - before.Checks,
		Mismatches: after.Mismatches - before.Mismatches,
		Errors:     after.Errors - before.Errors,
	}
}

func TestCompareModes(t *testing.T) {
	t.Cleanup(func() { SetEngine(string(EngineShadow)) })
	nextCalled := false
	next := func() (bool, error) {
		nextCalled = true
		return true, nil
	}

	SetEngine(string(EngineLegacy))
	stats := statsDelta(t, func() {
		if got := CompareAllowed("test", "legacy", false, next); got {
			t.Fatal("legacy 模式应返回旧结果 false")
		}
	})
	if nextCalled || stats.Checks != 0 {
		t.Fatalf("legacy 模式不应执行新引擎: called=%t checks=%d", nextCalled, stats.Checks)
	}

	SetEngine(string(EngineShadow))
	stats = statsDelta(t, func() {
		if got := CompareAllowed("test", "shadow mismatch", false, next); got {
			t.Fatal("shadow 模式应保留旧结果 false")
		}
	})
	if !nextCalled || stats.Checks != 1 || stats.Mismatches != 1 {
		t.Fatalf("shadow 模式应执行并记录差异: called=%t stats=%+v", nextCalled, stats)
	}

	nextCalled = false
	SetEngine(string(EngineEnforce))
	stats = statsDelta(t, func() {
		if got := CompareAllowed("test", "enforce mismatch", false, next); !got {
			t.Fatal("enforce 模式应采用新结果 true")
		}
	})
	if !nextCalled || stats.Checks != 1 || stats.Mismatches != 1 {
		t.Fatalf("enforce 模式统计不正确: called=%t stats=%+v", nextCalled, stats)
	}
}

func TestCompareActionsFailureFailsClosedInEnforce(t *testing.T) {
	t.Cleanup(func() { SetEngine(string(EngineShadow)) })
	SetEngine(string(EngineEnforce))
	got := CompareActions("test", "engine error", []string{"image"}, func() ([]string, error) {
		return nil, fmt.Errorf("database unavailable")
	})
	if got == nil || len(got) != 0 {
		t.Fatalf("enforce 下引擎错误应返回空动作默认拒绝, got %#v", got)
	}
	if GetStats().Errors == 0 {
		t.Fatal("enforce 下引擎错误应计入 errors")
	}
}

func TestSyncAuthorizeParity(t *testing.T) {
	db := newAuthzDB(t)
	now := time.Now()
	future := now.Add(time.Hour)
	expired := now.Add(-time.Hour)

	cluster := model.Cluster{Name: "prod"}
	users := []model.User{
		{Username: "active"},
		{Username: "member"},
		{Username: "expired-member"},
		{Username: "cr-user"},
	}
	if err := db.Create(&cluster).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}

	grants := []model.UserGrant{
		{
			UserID: users[0].ID, ClusterID: cluster.ID, Namespace: "prod,stage",
			Resource: "deployments,pods", Verb: "get,update", Actions: "image,replicas",
			ExpiresAt: &future,
		},
		{
			UserID: users[0].ID, ClusterID: cluster.ID, Namespace: "*",
			Resource: "nodes", Verb: "get", Actions: "*",
		},
		{
			UserID: users[3].ID, ClusterID: cluster.ID, Namespace: "*",
			Resource: "customitems.example.com", Verb: "update", Actions: "update",
		},
	}
	if err := db.Create(&grants).Error; err != nil {
		t.Fatal(err)
	}
	group := model.GrantGroup{Name: "platform"}
	if err := db.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	rule := model.GrantGroupRule{
		GroupID: group.ID, ClusterID: cluster.ID, Namespace: "*",
		Resource: "deployments", Verb: "update", Actions: "*",
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	members := []model.UserGroup{
		{UserID: users[1].ID, GroupID: group.ID, ExpiresAt: &future},
		{UserID: users[2].ID, GroupID: group.ID, ExpiresAt: &expired},
	}
	if err := db.Create(&members).Error; err != nil {
		t.Fatal(err)
	}

	if err := SyncFromLegacy(db); err != nil {
		t.Fatalf("首次同步: %v", err)
	}
	var bindings int64
	if err := db.Model(&model.AccessBinding{}).Count(&bindings).Error; err != nil {
		t.Fatal(err)
	}
	if bindings != 4 {
		t.Fatalf("binding 数 = %d, want 4（组规则按能力收敛，不按成员复制）", bindings)
	}

	cases := []struct {
		name      string
		user      model.User
		namespace string
		verb      string
		resource  string
		want      bool
	}{
		{"specific ns allowed", users[0], "prod", "get", "deployments", true},
		{"second specific ns allowed", users[0], "stage", "update", "pods", true},
		{"unlisted ns denied", users[0], "other", "get", "deployments", false},
		{"cluster-scoped requires wildcard binding", users[0], "", "get", "nodes", true},
		{"cluster-scoped cannot use specific ns", users[0], "", "get", "deployments", false},
		{"active group member", users[1], "any", "update", "deployments", true},
		{"expired group member", users[2], "any", "update", "deployments", false},
		{"custom resource update token", users[3], "any", "update", "customitems.example.com", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Authorize(db, tc.user.ID, "prod", tc.namespace, tc.verb, tc.resource)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("Authorize = %v, want %v", got, tc.want)
			}
		})
	}

	actions, err := UpdateActions(db, users[0].ID, "prod", "prod", "deployments", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || actions[0] != "image" || actions[1] != "replicas" {
		t.Fatalf("UpdateActions = %v, want [image replicas]", actions)
	}
	actions, err = UpdateActions(db, users[1].ID, "prod", "prod", "deployments", true)
	if err != nil {
		t.Fatal(err)
	}
	if actions != nil {
		t.Fatalf("通配动作应返回 nil 表示完整放行，got %v", actions)
	}
	actions, err = UpdateActions(db, users[3].ID, "prod", "any", "customitems.example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	if actions != nil {
		t.Fatalf("CR 的 update 动作令牌应完整放行，got %v", actions)
	}

	if err := SyncFromLegacy(db); err != nil {
		t.Fatalf("重复同步: %v", err)
	}
	var profiles, secondBindings int64
	if err := db.Model(&model.AccessProfile{}).Count(&profiles).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.AccessBinding{}).Count(&secondBindings).Error; err != nil {
		t.Fatal(err)
	}
	if secondBindings != bindings {
		t.Fatalf("重复同步 binding 数变化: %d → %d", bindings, secondBindings)
	}
	if profiles == 0 {
		t.Fatal("同步应生成至少一个 Profile")
	}

	if err := db.Where("user_id = ?", users[0].ID).Delete(&model.UserGrant{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := SyncFromLegacy(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.AccessBinding{}).Where("user_id = ?", users[0].ID).Count(&bindings).Error; err != nil {
		t.Fatal(err)
	}
	if bindings != 0 {
		t.Fatalf("旧直授删除后仍残留 %d 条 shadow binding", bindings)
	}
}
