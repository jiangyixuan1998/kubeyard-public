package database

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"os"
	"time"

	"kubeyard/internal/model"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DB 全局数据库句柄（单体应用，直接全局访问）
var DB *gorm.DB

// truncSQL 包一层 logger.Interface：超长 SQL（如事件批量 upsert 几千个占位符）在错误日志里截断，
// 避免 GORM 打印错误时把整条巨长 SQL 刷进控制台。maxBytes=0 表示不截断。
func truncSQL(inner logger.Interface, maxBytes int) logger.Interface {
	return &truncLogger{inner: inner, max: maxBytes}
}

type truncLogger struct {
	inner logger.Interface
	max   int
}

func (l *truncLogger) LogMode(level logger.LogLevel) logger.Interface {
	return &truncLogger{inner: l.inner.LogMode(level), max: l.max}
}

func (l *truncLogger) Info(ctx context.Context, msg string, args ...any) {
	l.inner.Info(ctx, msg, args...)
}

func (l *truncLogger) Warn(ctx context.Context, msg string, args ...any) {
	l.inner.Warn(ctx, msg, args...)
}

func (l *truncLogger) Error(ctx context.Context, msg string, args ...any) {
	l.inner.Error(ctx, msg, args...)
}

func (l *truncLogger) Trace(ctx context.Context, begin time.Time, fc func() (sql string, rows int64), err error) {
	if l.max <= 0 {
		l.inner.Trace(ctx, begin, fc, err)
		return
	}
	l.inner.Trace(ctx, begin, func() (string, int64) {
		sql, rows := fc()
		if len(sql) > l.max {
			sql = sql[:l.max] + " ...（SQL 过长已截断）"
		}
		return sql, rows
	}, err)
}

// EnsureSchema 首次部署自动建库：连接 MySQL 服务端（不指定库）执行 CREATE DATABASE IF NOT EXISTS。
// 库不存在时 gorm.Open 会直接失败，走不到 AutoMigrate，所以必须在 Init 之前调用。
func EnsureSchema(host string, port int, user, password, dbname string) {
	if dbname == "" {
		return
	}
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/?charset=utf8mb4&parseTime=True&loc=Local&timeout=5s",
		user, password, host, port)
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return // 无权限或连不上：交给 Init 的重试循环报错
	}
	sqlDB, err := db.DB()
	if err == nil {
		defer sqlDB.Close()
	}
	if err := db.Exec("CREATE DATABASE IF NOT EXISTS `" + dbname + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci").Error; err != nil {
		log.Printf("[WARN] 自动建库失败（账号可能无 CREATE 权限）: %v", err)
	}
}

func Init(dsn string) (*gorm.DB, error) {
	// 只记错误且不打印参数值：默认慢查询日志会把事件采集/录像等大事务的完整 SQL（含所有内容明细）刷进控制台
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: truncSQL(logger.New(
			log.New(os.Stdout, "\r\n", log.LstdFlags),
			logger.Config{
				LogLevel:                  logger.Error, // 仅错误，慢查询/常规 SQL 不打印
				IgnoreRecordNotFoundError: true,
				ParameterizedQueries:      true, // 错误日志只带 SQL 模板，不带数据明细
			},
		), 2000),
	})
	if err != nil {
		return nil, fmt.Errorf("连接 MySQL: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetMaxIdleConns(5)
	// 主从切换容错：连接 3 分钟强制换新，指向旧主（只读/断连）的连接最迟一个周期内自愈，
	// 运行期间数据库切换无需改任何配置；database/sql 对坏连接的查询会自动重试建新连接
	sqlDB.SetConnMaxLifetime(3 * time.Minute)

	if err := db.AutoMigrate(
		&model.Cluster{},
		&model.ClusterCredential{},
		&model.ApiToken{},
		&model.User{},
		&model.UserGrant{},
		&model.GrantGroup{},
		&model.GrantGroupRule{},
		&model.UserGroup{},
		&model.AccessProfile{},
		&model.AccessBinding{},
		&model.LDAPConfig{},
		&model.OAuthConfig{},
		&model.AuditLog{},
		&model.ExecRecording{},
		&model.ClusterEvent{},
		&model.TokenRevocation{},
		&model.Setting{},
		&model.AiUsageLog{},
		&model.ScheduleTask{},
		&model.ResourceDraft{},
		&model.Notification{},
	); err != nil {
		return nil, fmt.Errorf("自动建表: %w", err)
	}

	seedAdmin(db)
	DB = db
	return db, nil
}

// seedAdmin 首次启动播种内置管理员，随机密码仅打印一次
func seedAdmin(db *gorm.DB) {
	var count int64
	db.Model(&model.User{}).Where("username = ?", "admin").Count(&count)
	if count > 0 {
		return
	}
	pw, err := randomPassword(12)
	if err != nil {
		log.Fatalf("[kubeyard] 生成管理员密码失败: %v", err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	admin := model.User{
		Username:     "admin",
		DisplayName:  "平台管理员",
		Source:       "local",
		PasswordHash: string(hash),
		Role:         model.RoleSuperAdmin,
		Status:       1,
	}
	if err := db.Create(&admin).Error; err != nil {
		log.Printf("[WARN] 播种 admin 失败: %v", err)
		return
	}
	log.Println("==========================================================")
	log.Printf("  初始管理员账号: admin")
	log.Printf("  初始密码      : %s （仅显示一次，请立即登录并修改）", pw)
	log.Println("==========================================================")
}

const pwChars = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789"

func randomPassword(n int) (string, error) {
	out := make([]byte, n)
	for i := range out {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(pwChars))))
		if err != nil {
			return "", err
		}
		out[i] = pwChars[v.Int64()]
	}
	return string(out), nil
}
