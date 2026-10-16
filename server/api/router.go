package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"kubeyard/internal/auth"
	"kubeyard/internal/cluster"
	"kubeyard/internal/config"
	"kubeyard/internal/middleware"
	"kubeyard/ws"
)

// H 全局 handler 依赖
type H struct {
	cfg *config.Config
	mgr *cluster.Manager
	db  *gorm.DB
}

func NewRouter(cfg *config.Config, mgr *cluster.Manager, db *gorm.DB) *gin.Engine {
	if cfg.Server.Mode == "release" {
		gin.SetMode(gin.ReleaseMode)
	}
	h := &H{cfg: cfg, mgr: mgr, db: db}
	go h.dashboardLoop()          // 集群首页预热：30s 定时拉取，接口只读缓存
	go h.scheduleLoop()           // 定时任务调度：每分钟对齐 tick，执行到期的定时重启/扩缩容
	go h.notifyScanLoop()         // 站内消息：每小时扫描 <7 天到期的授权/组权限/账号，去重后生成预警
	go auth.StartRevocationGC(db) // token 吊销表：每小时清理过期行，防无限增长
	r := gin.New()
	// multipart 内存占用上限（上传文件超过此值自动落临时盘，handler 结束后 Gin 自动清理）
	r.MaxMultipartMemory = 32 << 20 // 32MB
	r.Use(gin.Logger(), middleware.PanicRecovery(), middleware.CORS(cfg.Server.AllowedOrigins))

	api := r.Group("/api/v1")

	// 公开：登录/刷新 + SSO 统一认证
	api.POST("/auth/login", h.Login)
	api.POST("/auth/refresh", h.Refresh)
	api.GET("/auth/oauth/status", h.OAuthStatus)
	api.GET("/auth/oauth/start", h.OAuthStart)
	api.GET("/auth/oauth/callback", h.OAuthCallback)

	// WebSocket：浏览器 WS 无法携带 Authorization 头，走首帧 JSON 鉴权（handler 内自鉴权）
	api.GET("/ws/exec", ws.HandleExec(mgr, db, h.aiRiskEvaluator()))
	api.GET("/ws/logs", ws.HandleLogs(mgr, db))

	// 认证区
	authed := api.Group("", middleware.JWTAuth())
	{
		authed.POST("/auth/logout", h.Logout)
		authed.GET("/auth/me", h.Me)
		authed.GET("/me/grants", h.MyGrants)

		// 站内消息（权限变更通知 + 到期预警；登录即可，只能读写自己的）
		authed.GET("/me/notifications", h.MyNotifications)
		authed.POST("/me/notifications/read", h.ReadNotifications)

		// AI 助手（严格只读：诊断/解释/生成草稿/排查对话/巡检/部署审查；生成物经人工确认走
		// 既有 apply 通道，AI 无任何写路径；调用记审计与用量，按天配额）
		authed.POST("/ai/diagnose", h.AiDiagnose)
		authed.POST("/ai/yaml-explain", h.AiYamlExplain)
		authed.POST("/ai/yaml-generate", h.AiYamlGenerate)
		authed.POST("/ai/yaml-review", h.AiYamlReview)
		authed.POST("/ai/chat", h.AiChat)
		authed.POST("/ai/inspect", h.AiInspect)
		authed.POST("/ai/logs", h.AiLogs)
		authed.POST("/ai/yaml-edit", h.AiYamlEdit)
		authed.POST("/ai/recording-summary", h.AiRecordingSummary)

		authed.GET("/clusters", h.ListClusters)
		authed.GET("/dashboard", h.Dashboard)
		authed.GET("/clusters/:cluster/test", h.TestCluster)

		authed.GET("/clusters/:cluster/namespaces", h.ListNamespaces)
		authed.GET("/clusters/:cluster/events", h.ListEvents)
		authed.GET("/clusters/:cluster/pods/:namespace/:name/download", h.DownloadPodFile)
		authed.POST("/clusters/:cluster/pods/:namespace/:name/upload", h.UploadPodFile)
		authed.GET("/clusters/:cluster/pods/:namespace/:name/files", h.ListPodFiles)
		authed.GET("/clusters/:cluster/pods/:namespace/:name/describe", h.DescribePod)
		// 节点概要：角色 + CPU/内存/异构资源（容量/可分配/已分配 requests + Pod 分配明细）
		authed.GET("/clusters/:cluster/nodes/:name/summary", h.NodeSummary)
		// 节点运维：封锁/恢复调度（cordon）；排空拆两步——drain-plan 只读分类预览（二次确认依据），
		// evict 单 Pod 驱逐（前端逐个调用，实现逐 Pod 动效与中止）
		authed.POST("/clusters/:cluster/nodes/:name/cordon", h.Cordon)
		authed.POST("/clusters/:cluster/nodes/:name/drain-plan", h.DrainPlan)
		authed.POST("/clusters/:cluster/nodes/:name/evict", h.EvictPod)
		// 调度候选数据（节点名/标签键值）：权限=任一工作负载的 update+scheduling 动作（无需 nodes get/list）；
		// 无权限返回空集静默降级为手输
		authed.GET("/clusters/:cluster/sched-options", h.SchedOptions)

		// 通用资源 CRUD
		authed.GET("/clusters/:cluster/resources", h.ListResources)
		authed.GET("/clusters/:cluster/crs", h.ListCRs)
		authed.GET("/clusters/:cluster/resource", h.GetResource)
		authed.GET("/clusters/:cluster/image-history", h.ImageHistory)
		authed.PUT("/clusters/:cluster/resource", h.UpdateResource)
		authed.PUT("/clusters/:cluster/resedit", h.ResourceFormEdit)
		// 资源暂存草稿（表单编辑「暂存」：只存平台不写集群，应用时走 resedit）
		authed.PUT("/clusters/:cluster/resdraft", h.DraftStage)
		authed.GET("/clusters/:cluster/resdrafts", h.DraftList)
		authed.DELETE("/clusters/:cluster/resdraft", h.DraftDelete)
		authed.DELETE("/clusters/:cluster/resource", h.DeleteResource)
		authed.POST("/clusters/:cluster/apply", h.ApplyYAML)
		authed.POST("/clusters/:cluster/scale", h.Scale)
		authed.POST("/clusters/:cluster/restart", h.Restart)
		authed.POST("/clusters/:cluster/stop", h.Stop)
		authed.POST("/clusters/:cluster/start", h.Start)

		// 定时任务（定时重启 / 定时扩缩容）
		authed.GET("/schedule-tasks", h.ListScheduleTasks)
		authed.GET("/schedule-tasks/preview", h.PreviewCron)
		authed.POST("/clusters/:cluster/schedule-tasks", h.CreateScheduleTask)
		authed.PUT("/schedule-tasks/:id", h.UpdateScheduleTask)
		authed.DELETE("/schedule-tasks/:id", h.DeleteScheduleTask)
		authed.POST("/schedule-tasks/:id/run", h.RunScheduleTask)

		// 审计 / 终端录像：登录即可访问，handler 内按管理员管辖范围过滤（SuperAdmin 全量）
		authed.GET("/admin/audits", h.ListAudits)
		authed.GET("/admin/exec-recordings", h.ListExecRecordings)
		authed.GET("/admin/exec-recordings/:id/cast", h.GetRecordingCast)
		authed.POST("/admin/exec-recordings/risk-reeval-batch", h.RecordingRiskReEvalBatch)
		authed.POST("/admin/exec-recordings/:id/risk-reeval", h.RecordingRiskReEval)
	}

	// 管理区（SuperAdmin）
	admin := api.Group("/admin", middleware.JWTAuth(), middleware.RequireSuperAdmin())
	{
		admin.GET("/clusters", h.AdminListClusters)
		admin.POST("/clusters", h.CreateCluster)
		admin.PUT("/clusters/:cluster", h.UpdateCluster)
		admin.DELETE("/clusters/:cluster", h.DeleteCluster)
		admin.POST("/clusters/:cluster/test", h.AdminTestCluster)
		admin.GET("/clusters/:cluster/ns-aliases", h.GetNSAliases)
		admin.PUT("/clusters/:cluster/ns-aliases", h.PutNSAliases)

		admin.POST("/kubeconfig/contexts", h.KubeconfigContexts)
		admin.POST("/kubeconfig/test", h.KubeconfigTest)

		admin.GET("/users", h.ListUsers)
		admin.GET("/users/export", h.ExportUsers)
		admin.POST("/users", h.CreateUser)
		admin.PUT("/users/:id", h.UpdateUser)
		admin.DELETE("/users/:id", h.DeleteUser)
		admin.GET("/users/:id/grants", h.GetUserGrants)
		admin.PUT("/users/:id/grants", h.PutUserGrants)
		admin.GET("/users/:id/perms", h.GetUserPerms)

		admin.GET("/groups", h.ListGroups)
		admin.GET("/groups/export", h.ExportGroups)
		admin.POST("/groups", h.CreateGroup)
		admin.GET("/groups/:id", h.GetGroup)
		admin.PUT("/groups/:id", h.UpdateGroup)
		admin.DELETE("/groups/:id", h.DeleteGroup)
		admin.GET("/users/:id/groups", h.GetUserGroups)

		admin.GET("/ldap", h.ListLDAP)
		admin.POST("/ldap", h.CreateLDAP)
		admin.PUT("/ldap/:id", h.UpdateLDAP)
		admin.DELETE("/ldap/:id", h.DeleteLDAP)
		admin.POST("/ldap/test", h.TestLDAP)

		admin.GET("/oauth", h.GetOAuthConfig)
		admin.PUT("/oauth", h.PutOAuthConfig)

		admin.GET("/settings", h.GetSettings)
		admin.PUT("/settings", h.PutSettings)

		// AI 模型接入配置（协议/地址/模型/Key/配额；Key 不回显，留空保留）
		admin.GET("/ai", h.GetAIConfig)
		admin.PUT("/ai", h.PutAIConfig)
		admin.POST("/ai/test", h.TestAI)

		admin.GET("/perm-presets", h.GetPermPresets)
		admin.PUT("/perm-presets", h.PutPermPresets)
		admin.DELETE("/perm-presets", h.DeletePermPresets)
		admin.POST("/perm-presets/sync", h.SyncPermPresets)

		// 新权限引擎只读观测区：写路径仍由既有授权页 / 工单接口负责
		admin.GET("/authz/profiles", h.ListAccessProfiles)
		admin.GET("/authz/bindings", h.ListAccessBindings)
		admin.GET("/authz/stats", h.GetAuthzStats)

		admin.GET("/tokens", h.ListApiTokens)
		admin.GET("/resource-drafts", h.AdminListDrafts)
		admin.POST("/tokens", h.CreateApiToken)
		admin.DELETE("/tokens/:id", h.RevokeApiToken)
	}

	// 集成 API（X-API-Token 服务端令牌，供 Jenkins / 工单平台调用）
	integ := api.Group("/integ", h.TokenAuth())
	{
		integ.POST("/grants", h.IntegCreateGrant)
		integ.POST("/grants/revoke", h.IntegRevokeGrants)
		integ.GET("/grants", h.IntegListGrants)
		integ.GET("/groups", h.IntegListGroups)
		integ.GET("/groups/:id", h.IntegGroupDetail)
		integ.POST("/group-members", h.IntegAddGroupMember)
		integ.POST("/group-members/remove", h.IntegRemoveGroupMember)
		// 工单申请单：集群枚举 / 账号开通（个人+服务）/ 账号状态查询
		integ.GET("/clusters", h.IntegListClusters)
		integ.GET("/users/:username", h.IntegGetUser)
		integ.POST("/users", h.IntegProvisionUser)
		// 全量用户 + 权限清单（安全部门盘点；与 /admin/users/export 同口径）
		integ.GET("/users", h.IntegListUsers)
	}

	// 前端静态资源（web/dist）
	dist := cfg.Server.WebDist
	if st, err := os.Stat(dist); err == nil && st.IsDir() {
		r.NoRoute(func(c *gin.Context) {
			if strings.HasPrefix(c.Request.URL.Path, "/api/") {
				c.JSON(http.StatusNotFound, gin.H{"message": "接口不存在"})
				return
			}
			// 尝试静态文件，否则回退 index.html（SPA 路由）
			p := filepath.Join(dist, filepath.Clean("/"+c.Request.URL.Path))
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				c.File(p)
				return
			}
			c.File(filepath.Join(dist, "index.html"))
		})
	} else {
		r.NoRoute(func(c *gin.Context) {
			c.JSON(http.StatusNotFound, gin.H{"message": "接口不存在"})
		})
	}

	return r
}
