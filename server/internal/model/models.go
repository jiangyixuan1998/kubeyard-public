package model

import (
	"time"

	"gorm.io/gorm"
)

// ---------- 集群 ----------
type Cluster struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"uniqueIndex;size:64" json:"name"`
	Description string    `gorm:"size:255" json:"description"`
	APIServers  string    `gorm:"size:512" json:"api_servers"`             // JSON 数组，多 master 兜底
	Status      string    `gorm:"size:16;default:'Pending'" json:"status"` // Healthy/Degraded/Offline/Pending
	Version     string    `gorm:"size:32" json:"version"`
	NodeCount   int       `json:"node_count"`
	Labels      string    `gorm:"size:1024" json:"labels"`
	GrafanaURL  string    `gorm:"size:512" json:"grafana_url"` // Pod 监控看板地址前缀（含 var-namespace 前的全部参数），前端拼接 namespace 与 pod
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ---------- 集群凭据（敏感，独立成表）----------
type ClusterCredential struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	ClusterID   uint       `gorm:"uniqueIndex" json:"cluster_id"`
	Type        string     `gorm:"size:32;default:'kubeconfig'" json:"type"` // kubeconfig(默认) / serviceaccount
	ContextName string     `gorm:"size:64" json:"context_name"`              // 多 context 文件导入时选定的 context
	AuthType    string     `gorm:"size:32" json:"auth_type"`                 // 解析出的凭据形式 token/clientcert（仅展示）
	CipherText  string     `gorm:"type:mediumtext" json:"-"`                 // 整份 kubeconfig 明文（历史 AES-256-GCM 密文兼容读取，启动自动转明文）
	KeyID       string     `gorm:"size:16" json:"key_id"`                    // 历史遗留字段（早期加密密钥版本），现仅占位
	Fingerprint string     `gorm:"size:64" json:"fingerprint"`               // 凭据指纹（展示用，不回显内容）
	ExpiresAt   *time.Time `json:"expires_at"`
	UpdatedBy   uint       `json:"updated_by"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// ---------- 用户 ----------
type User struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	Username     string     `gorm:"uniqueIndex;size:64" json:"username"`
	DisplayName  string     `gorm:"size:128" json:"display_name"`
	Email        string     `gorm:"size:128" json:"email"`
	Team         string     `gorm:"size:128" json:"team"`                           // 所属团队（工单申请单填写，展示用）
	AccountType  string     `gorm:"size:16;default:'personal'" json:"account_type"` // personal 个人（NSSO 登录）/ service 服务（本地密码账号，程序调用）
	Source       string     `gorm:"size:16;default:'local'" json:"source"`          // local / ldap / sso
	PasswordHash string     `gorm:"size:128" json:"-"`                              // bcrypt，仅 local
	Role         string     `gorm:"size:32;default:'Viewer'" json:"role"`           // SuperAdmin/ClusterAdmin(由grants表达)/Developer/Viewer
	Status       int        `gorm:"default:1" json:"status"`                        // 1启用 0禁用
	ExpiresAt    *time.Time `json:"expires_at"`                                     // 账号有效期；NULL=永久。到期后个人账号仅失去权限（不阻断登录），服务账号拒绝登录
	LastLoginAt  *time.Time `json:"last_login_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// Expired 账号有效期是否已过（NULL=永久）；到期语义：个人账号失去全部授权（登录不拦），服务账号拒绝登录
func (u *User) Expired() bool {
	return u.ExpiresAt != nil && u.ExpiresAt.Before(time.Now())
}

// ---------- 授权（五元组 + 细粒度动作）----------
type UserGrant struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	UserID    uint           `gorm:"index:idx_grant" json:"user_id"`
	ClusterID uint           `gorm:"index:idx_grant" json:"cluster_id"`
	Namespace string         `gorm:"size:255;index:idx_grant" json:"namespace"` // "*" 全部，支持逗号多选
	Resource  string         `gorm:"size:255;default:'*'" json:"resource"`      // "*" 或 "pods,services"
	Verb      string         `gorm:"size:128;default:'*'" json:"verb"`          // get,list,create,update,delete,exec,logs,download 或 "*"
	Actions   string         `gorm:"size:255;default:'*'" json:"actions"`       // 细粒度动作/字段：image,resources,env,replicas,restart 或 *；仅 verb 含 update 时生效
	ExpiresAt *time.Time     `gorm:"index" json:"expires_at"`                   // NULL=永久；到期后授权自动失效
	CreatedBy uint           `json:"created_by"`
	CreatedAt time.Time      `json:"created_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// ---------- API 令牌（服务端集成：Jenkins / 工单平台调用授权接口用）----------
type ApiToken struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	Name        string     `gorm:"size:64" json:"name"`          // 用途标识，如 jenkins
	TokenHash   string     `gorm:"size:64;uniqueIndex" json:"-"` // sha256 hex，明文只在创建时返回一次
	TokenPrefix string     `gorm:"size:16" json:"token_prefix"`  // 令牌前缀（展示比对用）
	LastUsedAt  *time.Time `json:"last_used_at"`
	RevokedAt   *time.Time `json:"revoked_at"`
	CreatedBy   uint       `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
}

// ---------- 权限组：组内配置授权规则，用户加入组即获得并集权限 ----------
type GrantGroup struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:64;uniqueIndex" json:"name"`
	Description string    `gorm:"size:255" json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type GrantGroupRule struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	GroupID   uint   `gorm:"index:idx_group_rule" json:"group_id"`
	ClusterID uint   `gorm:"index:idx_group_rule" json:"cluster_id"`
	Namespace string `gorm:"size:255" json:"namespace"`            // "*" 全部，支持逗号多选
	Resource  string `gorm:"size:255;default:'*'" json:"resource"` // "*" 或 "pods,services"
	Verb      string `gorm:"size:128;default:'*'" json:"verb"`     // get,list,create,update,... 或 "*"
	Actions   string `gorm:"size:255;default:'*'" json:"actions"`  // 细粒度动作/字段
}

type UserGroup struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	UserID    uint       `gorm:"index:uniq_user_group,unique" json:"user_id"`
	GroupID   uint       `gorm:"index:uniq_user_group,unique" json:"group_id"`
	ExpiresAt *time.Time `gorm:"index" json:"expires_at"` // 成员关系时限：NULL=永久，到期自动失去该组权限
	CreatedAt time.Time  `json:"created_at"`              // 入组时间（组权限的授权起点；存量行为零值，前端显示 —）
}

// ---------- 新权限引擎（AccessProfile + AccessBinding）----------
// Profile 是可复用能力集，Binding 只表达「谁 / 在哪个集群和命名空间 / 到何时」。
// 当前版本由 UserGrant + GrantGroupRule 幂等迁移生成，旧表仍是权威数据源；
// KUBEYARD_AUTHZ_ENGINE=enforce 前必须观察 shadow 差异并确认同步无告警。
type AccessProfile struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"uniqueIndex;size:160" json:"name"`
	Resources   string    `gorm:"size:1024" json:"resources"` // 与 UserGrant.Resource 同语义："*"、"pods,services" 或 CR
	Verbs       string    `gorm:"size:512" json:"verbs"`      // 与 UserGrant.Verb 同语义："*"、逗号列表
	Actions     string    `gorm:"size:1024" json:"actions"`   // 与 UserGrant.Actions 同语义："*"、动作键或字段路径
	Description string    `gorm:"size:255" json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type AccessBinding struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	DedupKey  string     `gorm:"uniqueIndex;size:64" json:"-"`
	ProfileID uint       `gorm:"index:idx_access_binding" json:"profile_id"`
	UserID    uint       `gorm:"index:idx_access_binding;index:idx_access_binding_user" json:"user_id"`
	GroupID   uint       `gorm:"index:idx_access_binding;index:idx_access_binding_group" json:"group_id"`
	ClusterID uint       `gorm:"index:idx_access_binding" json:"cluster_id"`
	Namespace string     `gorm:"size:255;index:idx_access_binding" json:"namespace"`
	ExpiresAt *time.Time `gorm:"index" json:"expires_at"`
	CreatedBy uint       `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// ---------- LDAP 配置（前端可配置）----------
type LDAPConfig struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	Name            string    `gorm:"size:64" json:"name"`
	Enabled         bool      `gorm:"default:false" json:"enabled"`
	Priority        int       `gorm:"default:0" json:"priority"` // 越小越先尝试
	Host            string    `gorm:"size:255" json:"host"`
	Port            int       `json:"port"`
	Security        string    `gorm:"size:16;default:'none'" json:"security"` // none/starttls/ldaps
	SkipTLSVerify   bool      `gorm:"default:false" json:"skip_tls_verify"`
	BindDN          string    `gorm:"size:255" json:"bind_dn"`
	BindPassword    string    `gorm:"type:text" json:"-"` // 明文落库（历史 AES 密文兼容读取）；JSON 永不回显
	UserBaseDN      string    `gorm:"size:255" json:"user_base_dn"`
	UserFilter      string    `gorm:"size:255;default:'(sAMAccountName=%s)'" json:"user_filter"`
	SearchScope     string    `gorm:"size:8;default:'sub'" json:"search_scope"`
	AttrUsername    string    `gorm:"size:64;default:'sAMAccountName'" json:"attr_username"`
	AttrDisplayName string    `gorm:"size:64;default:'displayName'" json:"attr_display_name"`
	AttrEmail       string    `gorm:"size:64;default:'mail'" json:"attr_email"`
	DefaultRole     string    `gorm:"size:32;default:'Viewer'" json:"default_role"`
	UpdatedBy       uint      `json:"updated_by"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ---------- SSO 统一认证（OAuth 2.0 授权码，企业认证中心对接）----------
// 与本地/LDAP 密码登录并行共存：SSO 登录成功后按 AD 账号匹配 users 表（与 LDAP 同源同值），
// 命中即复用已有账号与授权，未命中按 AutoCreate 自动开户；登录态仍是平台自身 JWT。
type OAuthConfig struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	Name         string `gorm:"size:64;default:'企业统一认证'" json:"name"`
	Enabled      bool   `gorm:"default:false" json:"enabled"`
	ClientID     string `gorm:"size:128" json:"client_id"`
	ClientSecret string `gorm:"size:255" json:"-"` // 仅服务端使用，JSON 永不回显
	// 三个接口地址按《oauth2.0应用集成手册》配置；开发联调填测试环境，生产就绪后改配置即可
	AuthorizeURL  string `gorm:"size:512;default:'https://sso.example.com/openapi/oauth/authorize'" json:"authorize_url"`
	TokenURL      string `gorm:"size:512;default:'https://sso.example.com/openapi/oauth/token'" json:"token_url"`
	UserInfoURL   string `gorm:"size:512;default:'https://sso.example.com/openapi/oauth/userinfo'" json:"userinfo_url"`
	RedirectURI   string `gorm:"size:512" json:"redirect_uri"` // 需预先在认证中心注册：https://<平台域名>/api/v1/auth/oauth/callback
	UsernameField string `gorm:"size:64;default:'yumADAccount'" json:"username_field"`
	DefaultRole   string `gorm:"size:32;default:'Viewer'" json:"default_role"`
	AutoCreate    bool   `gorm:"default:true" json:"auto_create"`
	// 阶段二切换开关：true 时仅允许 SSO 登录（本地超管密码登录保留为兜底，防认证中心故障锁死平台）
	OnlySSO   bool      `json:"only_sso"`
	UpdatedBy uint      `json:"updated_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ---------- 审计 ----------
type AuditLog struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	UserID       uint      `gorm:"index" json:"user_id"`
	Username     string    `gorm:"size:64;index" json:"username"`
	Action       string    `gorm:"size:64;index" json:"action"` // login.success/resource.delete/exec.open/...
	Cluster      string    `gorm:"size:64;index" json:"cluster"`
	Namespace    string    `gorm:"size:64;index" json:"namespace"`
	ResourceType string    `gorm:"size:64" json:"resource_type"`
	ResourceName string    `gorm:"size:255" json:"resource_name"`
	Detail       string    `gorm:"type:text" json:"detail"` // JSON 上下文
	SourceIP     string    `gorm:"size:64" json:"source_ip"`
	UserAgent    string    `gorm:"size:255" json:"user_agent"`
	Status       string    `gorm:"size:16" json:"status"` // success/failure
	CreatedAt    time.Time `gorm:"index:idx_audit_time" json:"created_at"`
}

// ---------- 集群事件（30s 轮询采集入库，保留 7 天）----------
type ClusterEvent struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// cluster+last_time 复合索引：事件采集器启动播种水位要取「该集群最新一条」
	// （WHERE cluster=? ORDER BY last_time DESC LIMIT 1），只有 (cluster,event_uid)
	// 唯一索引时排序走不了索引，大集群全表 filesort 实测能拖到 readTimeout 30s
	Cluster         string    `gorm:"size:64;index:uniq_cluster_event,unique;index:idx_cluster_last_time,priority:1" json:"cluster"`
	EventUID        string    `gorm:"size:64;index:uniq_cluster_event,unique" json:"event_uid"` // K8s 事件 UID，去重更新用
	Namespace       string    `gorm:"size:64;index" json:"namespace"`
	Type            string    `gorm:"size:16" json:"type"` // Normal / Warning
	Reason          string    `gorm:"size:64" json:"reason"`
	ObjectType      string    `gorm:"size:64" json:"object_type"` // Pod/Deployment/...
	ObjectName      string    `gorm:"size:255;index" json:"object_name"`
	Message         string    `gorm:"type:text" json:"message"`
	Count           int64     `json:"count"`
	SourceComponent string    `gorm:"size:128" json:"source_component"`
	FirstTime       time.Time `json:"first_time"`
	LastTime        time.Time `gorm:"index:idx_event_last_time;index:idx_cluster_last_time,priority:2" json:"last_time"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ---------- 定时任务（定时重启 / 定时扩缩容）----------
// Type: restart / scale；Trigger: once（RunAt 到点执行一次）/ cron（Cron 为 5 段标准表达式：分 时 日 月 周）；
// Resource: deployments / statefulsets / daemonsets；Replicas 仅 scale 用（daemonsets 不支持扩缩容）；
// LastStatus: success / failed / missed / running；once 任务执行后自动停用保留记录。
type ScheduleTask struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	Name      string     `gorm:"size:128" json:"name"`
	Type      string     `gorm:"size:16;index" json:"type"`
	Cluster   string     `gorm:"size:64;index" json:"cluster"`
	Namespace string     `gorm:"size:64" json:"namespace"`
	Resource  string     `gorm:"size:64" json:"resource"`
	ResName   string     `gorm:"size:255" json:"res_name"`
	Trigger   string     `gorm:"size:8" json:"trigger"`
	RunAt     *time.Time `json:"run_at"`
	Cron      string     `gorm:"size:64" json:"cron"`
	Replicas  *int32     `json:"replicas"`
	Enabled   bool       `gorm:"default:true" json:"enabled"`

	LastRunAt   *time.Time `json:"last_run_at"`
	NextRunAt   *time.Time `json:"next_run_at"`
	LastStatus  string     `gorm:"size:16" json:"last_status"`
	LastError   string     `gorm:"type:text" json:"last_error"`
	CreatedBy   uint       `gorm:"index" json:"created_by"`
	CreatorName string     `gorm:"size:64" json:"creator_name"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// ---------- 终端录像元数据 ----------
type ExecRecording struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	SessionID string     `gorm:"uniqueIndex;size:64" json:"session_id"`
	AuditID   uint       `gorm:"index" json:"audit_id"`
	Username  string     `gorm:"size:64;index" json:"username"`
	Cluster   string     `gorm:"size:64" json:"cluster"`
	Namespace string     `gorm:"size:64" json:"namespace"`
	Pod       string     `gorm:"size:255" json:"pod"`
	Container string     `gorm:"size:255" json:"container"`
	FilePath  string     `gorm:"size:512" json:"file_path"`
	Content   string     `gorm:"type:mediumtext" json:"-"` // 录像内容（asciicast v2 文本）入库存储；早期数据/超限会话回退 FilePath 文件
	SizeBytes int64      `json:"size_bytes"`
	Duration  int        `json:"duration"` // 秒
	StartedAt time.Time  `gorm:"index" json:"started_at"`
	EndedAt   *time.Time `gorm:"index" json:"ended_at"`                     // NULL=录像中（WS 仍连接，未结束）
	RiskLevel string     `gorm:"size:8;index;default:''" json:"risk_level"` // 风险等级：空=未评估 lost=内容丢失 none=空会话 low/medium/high
	RiskWhy   string     `gorm:"size:255" json:"risk_why"`                  // 评级理由（一句话，模型生成；空=未评估/评估失败）
	UpdatedAt time.Time  `json:"updated_at"`                                // 周期落库触碰时间：自愈租约判据（90s 无触碰 = 失活可自愈）
}

// ---------- Token 吊销 ----------
type TokenRevocation struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	JTI       string    `gorm:"uniqueIndex;size:64" json:"jti"`
	UserID    uint      `gorm:"index" json:"user_id"`
	Reason    string    `gorm:"size:128" json:"reason"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// ---------- 系统设置 ----------
type Setting struct {
	Key       string    `gorm:"primaryKey;size:64" json:"key"`
	Value     string    `gorm:"type:text" json:"value"`
	UpdatedBy uint      `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ---------- AI 调用记录 ----------
// 每次 AI 功能调用记一行：按天计数做每用户配额 + 简要留痕。
// 不落 prompt 全文（可能含集群信息），审计日志里另有摘要。
type AiUsageLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index:idx_aiusage_user_day" json:"user_id"`
	Day       string    `gorm:"size:10;index:idx_aiusage_user_day" json:"day"` // YYYY-MM-DD，配额按天
	Feature   string    `gorm:"size:32" json:"feature"`                        // diagnose / yaml_explain / yaml_generate / test
	Target    string    `gorm:"size:255" json:"target"`                        // 诊断对象 / 摘要（截断）
	Status    string    `gorm:"size:16" json:"status"`                         // success / failed
	CreatedAt time.Time `json:"created_at"`
}

// ---------- 资源暂存草稿 ----------
// 表单编辑的「暂存」：payload 与 resedit 的 payload 完全同构，只存平台、不写集群；
// 应用时原样走 resedit 通道（权限校验/字段 diff/审计全部复用），成功后删除草稿。
// resource 为裸 plural（内置资源）；同目标同模块重复暂存 = 覆盖。
type ResourceDraft struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	ClusterID uint   `gorm:"index:idx_resdraft,unique" json:"cluster_id"`
	Namespace string `gorm:"size:255;index:idx_resdraft,unique" json:"namespace"`
	Group     string `gorm:"size:128" json:"group"` // API group（内置资源多为空，apps/networking.k8s.io 等）
	Version   string `gorm:"size:64" json:"version"`
	Resource  string `gorm:"size:128;index:idx_resdraft,unique" json:"resource"`
	Name      string `gorm:"size:255;index:idx_resdraft,unique" json:"name"`
	Section   string `gorm:"size:64;index:idx_resdraft,unique" json:"section"`
	// Payload 与 resedit 的 payload 完全同构；ResourceVer 为暂存时的 resourceVersion（应用时漂移提示用）；
	// SectionLabel 为模块中文名（列表页展示）
	Payload      string    `gorm:"type:text" json:"payload"`
	ResourceVer  string    `gorm:"size:64" json:"resource_ver"`
	SectionLabel string    `gorm:"size:64" json:"section_label"`
	CreatedBy    uint      `gorm:"index" json:"created_by"`
	CreatorName  string    `gorm:"size:128" json:"creator_name"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ---------- 站内消息 ----------
// 权限变更通知（新增/移除/续期）与到期预警（<7 天扫描，去重键保证每次到期事件只提示一次）；
// DedupKey 为 NULL 的唯一索引在 MySQL 下互不冲突——变更类事件不设去重键（每次变更都该通知）。
type Notification struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	UserID    uint       `gorm:"index:idx_notif_user" json:"user_id"`
	Type      string     `gorm:"size:32;index" json:"type"` // grant_added / grant_removed / grant_renewed / grant_expiring / account_expiring
	Title     string     `gorm:"size:128" json:"title"`
	Content   string     `gorm:"size:512" json:"content"`
	DedupKey  *string    `gorm:"size:255;uniqueIndex" json:"-"`
	ReadAt    *time.Time `gorm:"index" json:"read_at"`
	CreatedAt time.Time  `json:"created_at"`
}

// 平台角色常量
const (
	RoleSuperAdmin   = "SuperAdmin"
	RoleClusterAdmin = "ClusterAdmin"
	RoleDeveloper    = "Developer"
	RoleViewer       = "Viewer"
)
