package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"kubeyard/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestAutoRiskEvaluatorRetriesTransientModelFailure(t *testing.T) {
	var requests atomic.Int32
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(w, "temporary model outage", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"{\"level\":\"low\",\"why\":\"只读命令\"}"}}]}`)
		fmt.Fprintln(w)
		fmt.Fprintln(w, "data: [DONE]")
		fmt.Fprintln(w)
	}))
	defer modelServer.Close()

	db, err := gorm.Open(sqlite.Open("file:risk-eval-retry?mode=memory&cache=shared"))
	if err != nil {
		t.Skip("sqlite unavailable:", err)
	}
	if err := db.AutoMigrate(&model.ExecRecording{}, &model.Setting{}, &model.AiUsageLog{}); err != nil {
		t.Fatal(err)
	}
	settings := []model.Setting{
		{Key: "ai.enabled", Value: "true"},
		{Key: "ai.provider", Value: "openai"},
		{Key: "ai.base_url", Value: modelServer.URL},
		{Key: "ai.model", Value: "test-model"},
		{Key: "ai.api_key", Value: "test-key"},
	}
	for _, setting := range settings {
		if err := db.Create(&setting).Error; err != nil {
			t.Fatal(err)
		}
	}

	rec := &model.ExecRecording{
		SessionID: "session-retry",
		StartedAt: time.Now(),
		Content:   "[0.1,\"i\",\"ls\\r\"]\n",
	}
	if err := db.Create(rec).Error; err != nil {
		t.Fatal(err)
	}

	(&H{db: db}).aiRiskEvaluator()(rec)

	var saved model.ExecRecording
	if err := db.First(&saved, rec.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.RiskLevel != "low" || saved.RiskWhy != "只读命令" {
		t.Fatalf("retry result not persisted: level=%q why=%q", saved.RiskLevel, saved.RiskWhy)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("model should be retried once after transient failure; requests=%d", got)
	}
}
