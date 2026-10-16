package middleware

import "testing"

// TestEntryMatchClusterScoped entryMatch 的集群级语义（外部评审核实）：
// want 为空串 = 集群级资源（nodes/namespaces 等），仅 ns=* 或空授权命中；
// 具体命名空间授权不放行——此前任意 ns 授权均命中，仅授 ns-a 的用户可列全集群节点
func TestEntryMatchClusterScoped(t *testing.T) {
	cases := []struct {
		granted, want string
		expect        bool
	}{
		{"*", "", true},          // ns=* 授权：集群级资源放行
		{"", "", true},           // 授权侧空串视为 *
		{"ns-a,ns-b", "", false}, // 具体 ns 授权：不放行集群级资源
		{"ns-a", "ns-a", true},   // namespaced 资源正常匹配
		{"ns-a,ns-b", "ns-b", true},
		{"*", "*", true}, // want=* 仅 * 命中
		{"ns-a", "*", false},
	}
	for _, c := range cases {
		if got := entryMatch(c.granted, c.want); got != c.expect {
			t.Errorf("entryMatch(%q, %q) = %v, want %v", c.granted, c.want, got, c.expect)
		}
	}
}

// TestFieldViolationsMetadataGuard 字段级授权不再整体忽略 metadata：
// finalizers/ownerReferences 改动应被拒绝；labels/annotations/name 按平台设计
// 走 update 通道、不在字段级管控（服务端管理字段也不算差异）
func TestFieldViolationsMetadataGuard(t *testing.T) {
	allowed := []string{"spec.replicas"}
	old := map[string]any{
		"metadata": map[string]any{"name": "app", "labels": map[string]any{"a": "b"}, "finalizers": []any{}},
		"spec":     map[string]any{"replicas": int64(1)},
	}

	// 只改 spec.replicas：通过
	ok := map[string]any{
		"metadata": map[string]any{"name": "app", "labels": map[string]any{"a": "b"}, "finalizers": []any{}},
		"spec":     map[string]any{"replicas": int64(2)},
	}
	if bad := FieldViolations(old, ok, allowed); len(bad) != 0 {
		t.Fatalf("仅改授权字段不应有越权路径: %v", bad)
	}

	// 加 finalizer：拒绝（此前 metadata 整体忽略，可绕过）
	addFinalizer := map[string]any{
		"metadata": map[string]any{"name": "app", "labels": map[string]any{"a": "b"},
			"finalizers": []any{"example.com/hold"}},
		"spec": map[string]any{"replicas": int64(2)},
	}
	bad := FieldViolations(old, addFinalizer, allowed)
	if len(bad) == 0 || bad[0] != "metadata.finalizers" {
		t.Fatalf("改 finalizers 应报 metadata.finalizers 越权: %v", bad)
	}

	// 改 labels：平台设计走 update 通道，字段级不管控
	chgLabels := map[string]any{
		"metadata": map[string]any{"name": "app", "labels": map[string]any{"a": "c"}, "finalizers": []any{}},
		"spec":     map[string]any{"replicas": int64(2)},
	}
	if bad := FieldViolations(old, chgLabels, allowed); len(bad) != 0 {
		t.Fatalf("labels 走 update 通道不应被字段级拦截: %v", bad)
	}

	// 服务端管理字段缺失/变化不算差异（精简 YAML apply 场景）
	slim := map[string]any{
		"metadata": map[string]any{"name": "app", "finalizers": []any{}},
		"spec":     map[string]any{"replicas": int64(2)},
	}
	if bad := FieldViolations(old, slim, allowed); len(bad) != 0 {
		t.Fatalf("服务端管理字段缺失不应算差异: %v", bad)
	}

	// ownerReferences 同 finalizers 一样管控
	addOwner := map[string]any{
		"metadata": map[string]any{"name": "app", "labels": map[string]any{"a": "b"}, "finalizers": []any{},
			"ownerReferences": []any{map[string]any{"kind": "Deployment", "name": "x"}}},
		"spec": map[string]any{"replicas": int64(2)},
	}
	if bad := FieldViolations(old, addOwner, allowed); len(bad) == 0 {
		t.Fatal("改 ownerReferences 应被拒绝")
	}
}

func TestEffectiveRolePresetResourceScoping(t *testing.T) {
	pp := PermPresets{
		Actions:            builtinActions,
		Presets:            builtinPresets,
		ResourcePresets:    builtinResourcePresets,
		ResourcePresetMode: "legacy",
	}

	legacy := EffectiveRolePreset(pp, "dev", "deployments")
	if !containsToken(legacy.Verbs, "delete") {
		t.Fatal("legacy mode must keep the old global dev preset")
	}

	pp.ResourcePresetMode = "scoped"
	pods := EffectiveRolePreset(pp, "dev", "pods")
	if !containsToken(pods.Verbs, "delete") || !containsToken(pods.Verbs, "exec") {
		t.Fatal("scoped pod dev preset should allow delete and exec")
	}

	deployments := EffectiveRolePreset(pp, "dev", "deployments")
	if containsToken(deployments.Verbs, "delete") || containsToken(deployments.Verbs, "exec") {
		t.Fatal("scoped deployment dev preset should not allow delete or exec")
	}

	mixed := EffectiveRolePreset(pp, "dev", "pods,deployments")
	if containsToken(mixed.Verbs, "delete") || containsToken(mixed.Verbs, "exec") {
		t.Fatal("mixed pods+deployments preset must use the intersection and avoid privilege expansion")
	}

	override := RolePreset{Verbs: []string{"get", "list"}, Actions: []string{"*"}}
	pp.ResourcePresets["deployments"] = map[string]*RolePreset{"dev": &override}
	custom := EffectiveRolePreset(pp, "dev", "deployments")
	if !verbSetEqualForTest(custom.Verbs, override.Verbs) {
		t.Fatal("explicit resource preset should override the recommended default")
	}
}

func containsToken(tokens []string, token string) bool {
	for _, v := range tokens {
		if v == token {
			return true
		}
	}
	return false
}

func verbSetEqualForTest(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, v := range a {
		if !containsToken(b, v) {
			return false
		}
	}
	return true
}
