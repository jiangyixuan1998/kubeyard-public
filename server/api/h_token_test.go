package api

import (
	"testing"

	"kubeyard/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestIntegVerbDisplayExpandsUpdateActions(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:integ-verb-display?mode=memory&cache=shared"))
	if err != nil {
		t.Skip("sqlite unavailable:", err)
	}
	if err := db.AutoMigrate(&model.Setting{}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		resource string
		verb     string
		actions  string
		want     string
	}{
		{
			name:     "workload actions",
			resource: "deployments",
			verb:     "get,list,watch,update,delete",
			actions:  "image,restart,replicas",
			want:     "get（查看详情）, list（列表）, watch（监听）, update（更新镜像、重启、副本数）, delete（删除）",
		},
		{
			name:     "wildcard actions",
			resource: "deployments",
			verb:     "update",
			actions:  "*",
			want:     "update（全部更新）",
		},
		{
			name:     "resource outside action catalog",
			resource: "example.com/widgets",
			verb:     "update",
			actions:  "update",
			want:     "update（完整更新）",
		},
		{
			name:     "field path token",
			resource: "deployments",
			verb:     "update",
			actions:  "spec.replicas",
			want:     "update（spec.replicas）",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := integVerbDisplay(db, tt.resource, tt.verb, tt.actions); got != tt.want {
				t.Fatalf("integVerbDisplay() = %q, want %q", got, tt.want)
			}
		})
	}
}
