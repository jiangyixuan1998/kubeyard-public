package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"kubeyard/internal/middleware"
	"kubeyard/internal/model"
)

func TestValidateResourcePresetAffinity(t *testing.T) {
	valid := &middleware.PermPresets{
		ResourcePresetMode: "scoped",
		ResourcePresets: map[string]map[string]*middleware.RolePreset{
			"deployments": {
				"dev": {Verbs: []string{"get", "list", "update"}, Actions: []string{"image", "resources"}},
			},
			"pods": {
				"dev": {Verbs: []string{"get", "list", "exec", "logs"}, Actions: []string{"image"}},
			},
		},
	}
	if msg := validatePermPresets(valid); msg != "" {
		t.Fatalf("valid resource presets rejected: %s", msg)
	}

	invalid := &middleware.PermPresets{
		ResourcePresetMode: "scoped",
		ResourcePresets: map[string]map[string]*middleware.RolePreset{
			"deployments": {
				"dev": {Verbs: []string{"get", "exec"}, Actions: []string{"routes"}},
			},
		},
	}
	msg := validatePermPresets(invalid)
	if msg == "" || !strings.Contains(msg, "exec") {
		t.Fatalf("expected pod-only verb rejection, got %q", msg)
	}
}

func TestSyncPermPresetsFullModeClassifiesAndApplies(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:perm-preset-full-sync?mode=memory&cache=shared"))
	if err != nil {
		t.Skip("sqlite unavailable:", err)
	}
	if err := db.AutoMigrate(
		&model.Setting{}, &model.User{}, &model.UserGrant{},
		&model.GrantGroup{}, &model.GrantGroupRule{}, &model.UserGroup{},
		&model.AccessProfile{}, &model.AccessBinding{}, &model.AuditLog{},
	); err != nil {
		t.Fatal(err)
	}

	config := middleware.PermPresets{
		ResourcePresetMode: "scoped",
		ResourcePresets: map[string]map[string]*middleware.RolePreset{
			"deployments": {
				"dev": {Verbs: []string{"get", "list", "watch", "update"}, Actions: []string{"image", "resources"}},
			},
		},
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Setting{Key: "perm_presets", Value: string(raw)}).Error; err != nil {
		t.Fatal(err)
	}
	middleware.InvalidatePermPresets()
	t.Cleanup(middleware.InvalidatePermPresets)

	admin := &model.User{Username: "alice"}
	if err := db.Create(admin).Error; err != nil {
		t.Fatal(err)
	}
	grants := []model.UserGrant{
		{UserID: admin.ID, Resource: "deployments", Verb: "get,list,watch,update,delete", Actions: "image,resources"},
		{UserID: admin.ID, Resource: "pods", Verb: "get,list,watch,update,delete", Actions: "image"},
		{UserID: admin.ID, Resource: "endpoints", Verb: "get,update", Actions: "removed-action"},
	}
	if err := db.Create(&grants).Error; err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	call := func(apply bool) map[string]any {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		body := `{"mode":"all","apply":` + map[bool]string{false: "false", true: "true"}[apply] + `}`
		c.Request = httptest.NewRequest(http.MethodPost, "/sync", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("uid", uint(1))
		c.Set("username", "admin")
		h := &H{db: db}
		middleware.InvalidatePermPresets()
		h.SyncPermPresets(c)
		if w.Code != http.StatusOK {
			t.Fatalf("sync status = %d, body = %s", w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	dry := call(false)
	items := dry["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 dry-run items, got %d: %#v", len(items), items)
	}
	types := map[string]int{}
	for _, rawItem := range items {
		item := rawItem.(map[string]any)
		types[item["sync_type"].(string)]++
	}
	if types["align:dev"] != 1 || types["stale"] != 1 {
		t.Fatalf("unexpected dry-run classification: %#v", types)
	}

	out := call(true)
	if got := int(out["applied"].(float64)); got != 2 {
		t.Fatalf("applied = %d, want 2", got)
	}
	var deployment, pod, endpoint model.UserGrant
	if err := db.First(&deployment, grants[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if deployment.Verb != "get,list,watch,update" || deployment.Actions != "image,resources" {
		t.Fatalf("deployment not scoped to resource preset: %s / %s", deployment.Verb, deployment.Actions)
	}
	if err := db.First(&pod, grants[1].ID).Error; err != nil {
		t.Fatal(err)
	}
	if pod.Verb != "get,list,watch,update,delete" || pod.Actions != "image" {
		t.Fatalf("pod not aligned to trimmed global preset: %s / %s", pod.Verb, pod.Actions)
	}
	if err := db.First(&endpoint, grants[2].ID).Error; err != nil {
		t.Fatal(err)
	}
	if endpoint.Actions != "" {
		t.Fatalf("stale action was not removed: %q", endpoint.Actions)
	}
}
