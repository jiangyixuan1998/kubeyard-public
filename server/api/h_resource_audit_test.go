package api

import (
	"strings"
	"testing"
)

func TestFormEditDiffCoversNodeAndSchedulingSections(t *testing.T) {
	beforeNode := map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{"owner": "alice", "token": "secret-before"}},
		"spec": map[string]any{
			"taints": []any{map[string]any{"key": "dedicated", "value": "old", "effect": "NoSchedule"}},
		},
	}
	afterNode := map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{"owner": "bob", "token": "secret-after"}},
		"spec": map[string]any{
			"taints": []any{map[string]any{"key": "dedicated", "value": "new", "effect": "NoSchedule"}},
		},
	}
	taints := formEditDiff("nodes", false, "taints", beforeNode, afterNode)
	if !strings.Contains(taints, "dedicated") || !strings.Contains(taints, "old") || !strings.Contains(taints, "new") {
		t.Fatalf("taint diff should show before/after values: %s", taints)
	}
	annotations := formEditDiff("nodes", false, "annotations", beforeNode, afterNode)
	if !strings.Contains(annotations, "owner") || !strings.Contains(annotations, "token") ||
		strings.Contains(annotations, "secret-before") || strings.Contains(annotations, "secret-after") {
		t.Fatalf("annotation audit should identify changed keys without recording values: %s", annotations)
	}

	beforeWorkload := map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
		"nodeName": "node-a",
		"affinity": map[string]any{"nodeAffinity": map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": "old"}},
		"volumes":  []any{map[string]any{"name": "data", "emptyDir": map[string]any{}}},
	}}}}
	afterWorkload := map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
		"nodeName": "node-b",
		"affinity": map[string]any{"nodeAffinity": map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": "new"}},
		"volumes":  []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "claim-a"}}},
	}}}}
	for _, section := range []string{"scheduling", "storage"} {
		diff := formEditDiff("deployments", false, section, beforeWorkload, afterWorkload)
		if strings.Contains(diff, "前后无差异") || !strings.Contains(diff, "node-") && section == "scheduling" {
			t.Fatalf("%s diff should describe the change: %s", section, diff)
		}
	}
}

func TestFormEditDiffRedactsConfigDataValues(t *testing.T) {
	before := map[string]any{"data": map[string]any{"db-password": "old-secret"}}
	after := map[string]any{"data": map[string]any{"db-password": "new-secret", "config.yaml": "x"}}
	diff := formEditDiff("secrets", false, "data", before, after)
	if !strings.Contains(diff, "db-password") || !strings.Contains(diff, "config.yaml") ||
		strings.Contains(diff, "old-secret") || strings.Contains(diff, "new-secret") {
		t.Fatalf("data audit should list changed keys without values: %s", diff)
	}
}
