package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"time"

	"gorm.io/gorm"

	"kubeyard/api"
	"kubeyard/internal/audit"
	"kubeyard/internal/auth"
	"kubeyard/internal/authz"
	"kubeyard/internal/cluster"
	"kubeyard/internal/config"
	"kubeyard/internal/cryptox"
	"kubeyard/internal/database"
	"kubeyard/internal/events"
	"kubeyard/internal/model"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("[kubeyard] 加载配置失败: %v", err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("[kubeyard] 创建数据目录失败: %v", err)
	}

	// 数据库启动重试：主从切换/数据库启动中时按 3 秒间隔退避重试（最多 100 次 ≈ 5 分钟），避免容器反复重启；
	// 首次部署库不存在时自动创建（表由 AutoMigrate 自动建，管理员账号自动播种）
	database.EnsureSchema(cfg.Database.Host, cfg.Database.Port, cfg.Database.User, cfg.Database.Password, cfg.Database.DBName)
	var db *gorm.DB
	for attempt := 1; ; attempt++ {
		db, err = database.Init(cfg.Database.DSN())
		if err == nil {
			break
		}
		if attempt >= 100 {
			log.Fatalf("[kubeyard] 初始化数据库失败（已重试 %d 次）: %v", attempt, err)
		}
		log.Printf("[kubeyard] 数据库暂不可用（%v），3 秒后重试 %d/100…", err, attempt+1)
		time.Sleep(3 * time.Second)
	}

	// 密钥随数据库持久化（需在数据库就绪后解析）
	cryptox.Init(resolveKey(db, cfg, "KUBEYARD_ENCRYPTION_KEY", "security.encryption.key", "encryption"))
	auth.InitKeys(resolveKey(db, cfg, "KUBEYARD_JWT_KEY", "security.jwt.key", "jwt"))
	authz.SetEngine(cfg.Server.AuthzEngine)

	// 新权限表跟随旧表构建；同步失败时 legacy/shadow 仍安全，enforce 拒绝启动避免误收窄。
	if err := authz.SyncFromLegacy(database.DB); err != nil {
		if authz.Engine() == authz.EngineEnforce {
			log.Fatalf("[kubeyard] 权限引擎 enforce 前同步失败: %v", err)
		}
		log.Printf("[WARN] 权限引擎影子表同步失败，继续使用 legacy 判定: %v", err)
	}

	audit.Init(db)

	// 录像僵尸行自愈：服务重启时正在录制的会话（收尾未执行）——补状态/找回内容/标丢失
	api.HealRecordings(database.DB, cfg.DataDir)

	mgr, err := cluster.LoadFromDB(database.DB, cfg)
	if err != nil {
		log.Fatalf("[kubeyard] 加载集群失败: %v", err)
	}
	go mgr.StartHealthCheck()
	events.Start(mgr, database.DB) // 事件采集：30s 轮询入库，7 天保留

	router := api.NewRouter(cfg, mgr, database.DB)
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	log.Printf("[kubeyard] 服务已启动: http://0.0.0.0%s (前端目录: %s)", addr, cfg.Server.WebDist)
	if err := router.Run(addr); err != nil {
		log.Fatalf("[kubeyard] 服务退出: %v", err)
	}
}

// resolveKey 密钥解析：环境变量 > 旧版 keys.json（导入数据库）> 数据库 settings 表 > 自动生成并入库。
// 密钥仅用于读取历史版本加密的凭据（现已明文存储），随数据库持久化，重建容器/换机器后无需任何配置。
func resolveKey(db *gorm.DB, cfg *config.Config, envName, settingKey, legacyPurpose string) []byte {
	if v := os.Getenv(envName); v != "" {
		return config.DeriveKey(v)
	}
	if b, ok := cfg.LegacyKey(legacyPurpose); ok {
		saveKey(db, settingKey, b)
		return b
	}
	var s model.Setting
	if err := db.Where("`key` = ?", settingKey).First(&s).Error; err == nil {
		if b, derr := base64.StdEncoding.DecodeString(s.Value); derr == nil && len(b) == 32 {
			return b
		}
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("[kubeyard] 生成随机密钥失败: %v", err)
	}
	saveKey(db, settingKey, b)
	log.Printf("[kubeyard] 未设置 %s，已自动生成随机密钥并存入数据库（随数据库备份即可）", envName)
	return b
}

func saveKey(db *gorm.DB, key string, b []byte) {
	if err := db.Save(&model.Setting{Key: key, Value: base64.StdEncoding.EncodeToString(b)}).Error; err != nil {
		log.Fatalf("[kubeyard] 密钥写入数据库失败: %v", err)
	}
}
