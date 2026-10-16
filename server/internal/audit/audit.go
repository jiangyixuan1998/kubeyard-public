package audit

import (
	"log"
	"time"

	"kubeyard/internal/model"

	"gorm.io/gorm"
)

var (
	db *gorm.DB
	ch = make(chan model.AuditLog, 2048)
)

// Init 启动异步审计写入协程
func Init(d *gorm.DB) {
	db = d
	go worker()
}

func worker() {
	for a := range ch {
		if a.CreatedAt.IsZero() {
			a.CreatedAt = time.Now()
		}
		if err := db.Create(&a).Error; err != nil {
			log.Printf("[audit] 写入失败: %v", err)
		}
	}
}

// Log 异步记录；队列满时降级为同步写（保证不丢）
func Log(a model.AuditLog) {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	select {
	case ch <- a:
	default:
		if err := db.Create(&a).Error; err != nil {
			log.Printf("[audit] 降级写入失败: %v", err)
		}
	}
}

// Record 快捷构造
func Record(userID uint, username, action, cluster, namespace, rtype, rname, detail, status string) model.AuditLog {
	return model.AuditLog{
		UserID:       userID,
		Username:     username,
		Action:       action,
		Cluster:      cluster,
		Namespace:    namespace,
		ResourceType: rtype,
		ResourceName: rname,
		Detail:       detail,
		Status:       status,
	}
}
