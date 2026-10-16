package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"kubeyard/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// 自愈逻辑回归：三类僵尸行（有内容补时间 / 文件找回 / 标 lost）+ 防路径穿越 + 正常行不动
func TestHealRecordings(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"))
	if err != nil {
		t.Skip("sqlite unavailable:", err)
	}
	if err := db.AutoMigrate(&model.ExecRecording{}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	castDir := filepath.Join(dir, "recordings", "20260101")
	os.MkdirAll(castDir, 0o755)
	good := filepath.Join(castDir, "good.cast")
	os.WriteFile(good, []byte(`{"version":2}`), 0o644)

	now := time.Now()
	// 僵尸行的 updated_at 拨回 2 小时前（租约判据：90s 内有刷新 = 活跃，不能动）
	stale := now.Add(-2 * time.Hour)
	db.Create(&model.ExecRecording{SessionID: "s1", Content: "abc", StartedAt: stale, UpdatedAt: stale})          // 1: 有内容 → 补时间
	db.Create(&model.ExecRecording{SessionID: "s2", FilePath: good, StartedAt: stale, UpdatedAt: stale})          // 2: 文件在 → 找回
	db.Create(&model.ExecRecording{SessionID: "s3", FilePath: "/etc/passwd", StartedAt: stale, UpdatedAt: stale}) // 3: 丢失/穿越 → lost
	db.Create(&model.ExecRecording{SessionID: "s4", EndedAt: &now, Content: "x", StartedAt: now})                // 正常行不动
	// 活跃行（updated_at 刚刷新）：其他副本正在录的会话——自愈必须跳过
	db.Create(&model.ExecRecording{SessionID: "s5", Content: "live", StartedAt: now, UpdatedAt: now})

	HealRecordings(db, dir)

	var s5 model.ExecRecording
	db.Where("session_id = ?", "s5").First(&s5)
	if s5.EndedAt != nil || s5.RiskLevel != "" {
		t.Errorf("s5 活跃会话被自愈误伤: ended=%v risk=%q", s5.EndedAt, s5.RiskLevel)
	}

	var r1, r2, r3, r4 model.ExecRecording
	db.Where("session_id = ?", "s1").First(&r1)
	db.Where("session_id = ?", "s2").First(&r2)
	db.Where("session_id = ?", "s3").First(&r3)
	db.Where("session_id = ?", "s4").First(&r4)

	if r1.EndedAt == nil {
		t.Error("s1 应补 ended_at")
	}
	if r2.Content != `{"version":2}` || r2.FilePath != "" {
		t.Errorf("s2 应找回内容并清空路径: content=%q fp=%q", r2.Content, r2.FilePath)
	}
	if _, err := os.Stat(good); !os.IsNotExist(err) {
		t.Error("s2 找回后本地文件应删除")
	}
	if r3.RiskLevel != "lost" || r3.EndedAt == nil {
		t.Errorf("s3 应标 lost: risk=%q ended=%v", r3.RiskLevel, r3.EndedAt)
	}
	if r4.EndedAt == nil || r4.Content != "x" {
		t.Error("s4 正常行不应被改动")
	}

	// 租约内活跃行保护（第二道）：带本地文件的行，updated_at 在租约窗口内（录制副本刚刷新）
	// → 不进自愈候选（条件更新也不命中）——本地文件不得被删、行不得被改。
	// 注：raced（查询后、更新前被刷新）分支靠 SQL 条件更新语义保证，单线程测试无法确定性触达
	race := filepath.Join(castDir, "race.cast")
	os.WriteFile(race, []byte("keep"), 0o644)
	db.Create(&model.ExecRecording{
		SessionID: "s6", FilePath: race,
		StartedAt: now.Add(-2 * time.Hour), UpdatedAt: now, // 内容过期但 updated_at 活跃
	})
	HealRecordings(db, dir)
	if _, err := os.Stat(race); err != nil {
		t.Error("租约内活跃行的本地文件不应被删除")
	}
	var s6 model.ExecRecording
	db.Where("session_id = ?", "s6").First(&s6)
	if s6.EndedAt != nil || s6.Content != "" || s6.RiskLevel != "" {
		t.Errorf("租约内活跃行不应被自愈改动: ended=%v content=%q risk=%q", s6.EndedAt, s6.Content, s6.RiskLevel)
	}
}
