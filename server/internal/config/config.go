package config

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Config 平台配置：config.yaml + 环境变量覆盖（环境变量优先级更高）
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	DataDir  string         `yaml:"data_dir"`
}

type ServerConfig struct {
	Port           int    `yaml:"port"`
	Mode           string `yaml:"mode"` // debug / release
	WebDist        string `yaml:"web_dist"`
	AllowedOrigins string `yaml:"allowed_origins"` // CORS 白名单，逗号分隔；空或 * = 反射任意 Origin（内网默认），公网部署建议收紧
	AuthzEngine    string `yaml:"authz_engine"`    // legacy / shadow / enforce；shadow 为默认，只记录新旧差异不改判定
}

type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
}

func defaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port:        8000,
			Mode:        "release",
			WebDist:     "../web/dist",
			AuthzEngine: "shadow",
		},
		Database: DatabaseConfig{
			Host:   "127.0.0.1",
			Port:   3306,
			User:   "kubeyard",
			DBName: "kubeyard",
		},
		DataDir: "./data",
	}
}

func Load() (*Config, error) {
	cfg := defaultConfig()
	if raw, err := os.ReadFile("config.yaml"); err == nil {
		if err := yaml.Unmarshal(raw, cfg); err != nil {
			return nil, fmt.Errorf("解析 config.yaml: %w", err)
		}
	}
	envStr := func(k string, dst *string) {
		if v := os.Getenv(k); v != "" {
			*dst = v
		}
	}
	envInt := func(k string, dst *int) {
		if v := os.Getenv(k); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				*dst = n
			}
		}
	}
	envStr("KUBEYARD_WEB_DIST", &cfg.Server.WebDist)
	envStr("KUBEYARD_ALLOWED_ORIGINS", &cfg.Server.AllowedOrigins)
	envStr("KUBEYARD_AUTHZ_ENGINE", &cfg.Server.AuthzEngine)
	envInt("KUBEYARD_PORT", &cfg.Server.Port)
	envStr("KUBEYARD_MODE", &cfg.Server.Mode)
	envStr("KUBEYARD_DB_HOST", &cfg.Database.Host)
	envInt("KUBEYARD_DB_PORT", &cfg.Database.Port)
	envStr("KUBEYARD_DB_USER", &cfg.Database.User)
	envStr("KUBEYARD_DB_PASSWORD", &cfg.Database.Password)
	envStr("KUBEYARD_DB_NAME", &cfg.Database.DBName)
	envStr("KUBEYARD_DATA_DIR", &cfg.DataDir)
	return cfg, nil
}

// DSN go-sql-driver 按最后一个 '@' 切分用户信息，密码含 '@' 安全；
// timeout/readTimeout/writeTimeout 让断连/半开连接快速失败，配合连接池换新实现主从切换自愈
func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local&timeout=5s&readTimeout=30s&writeTimeout=30s",
		d.User, d.Password, d.Host, d.Port, d.DBName)
}

// 密钥解析见 main.go resolveKey：环境变量 > 数据库 settings 表 > 旧版 keys.json 导入 > 自动生成入库。
// 密钥随数据库持久化，换机器/重建容器/主从切换后无需任何配置，已加密的 LDAP 与集群凭据继续可用。

// DeriveKey 环境变量取值归一为 32 字节密钥：base64 的 32 字节 / 32 字符原串 / 其他任意串 SHA-256 派生
func DeriveKey(v string) []byte {
	if raw, err := base64.StdEncoding.DecodeString(v); err == nil && len(raw) == 32 {
		return raw
	}
	if len(v) == 32 {
		return []byte(v)
	}
	sum := sha256.Sum256([]byte(v))
	return sum[:]
}

// LegacyKey 读取旧版 <DataDir>/keys.json 中持久化的密钥（迁移导入数据库用）；不存在或无效返回 ok=false
func (c *Config) LegacyKey(purpose string) ([]byte, bool) {
	keys := map[string]string{}
	if raw, err := os.ReadFile(filepath.Join(c.DataDir, "keys.json")); err == nil {
		_ = json.Unmarshal(raw, &keys)
	}
	if b, err := base64.StdEncoding.DecodeString(keys[purpose]); err == nil && len(b) == 32 {
		return b, true
	}
	return nil, false
}
