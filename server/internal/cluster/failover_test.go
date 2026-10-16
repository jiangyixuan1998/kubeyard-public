package cluster

import "testing"

// TestSameClusterIdentity 故障切换身份判定（外部评审核实的空指纹漏洞）：
// 当前集群指纹为空（导入时读不到 kube-system）而候选非空时，此前条件放行——
// 仍可能切到 kubeconfig 里登记的其他集群。修复后两侧均非空且相同才切换
func TestSameClusterIdentity(t *testing.T) {
	cases := []struct {
		cur, cand string
		expect    bool
	}{
		{"uid-a", "uid-a", true},  // 同一集群：放行
		{"uid-a", "uid-b", false}, // 指纹不同：kubeconfig 里的其他集群
		{"uid-a", "", false},      // 候选读不到指纹（不可达/无权限）
		{"", "uid-a", false},      // 当前读不到指纹：无法确认候选身份，fail-closed
		{"", "", false},           // 两侧都读不到
	}
	for _, c := range cases {
		if got := sameClusterIdentity(c.cur, c.cand); got != c.expect {
			t.Errorf("sameClusterIdentity(%q, %q) = %v, want %v", c.cur, c.cand, got, c.expect)
		}
	}
}
