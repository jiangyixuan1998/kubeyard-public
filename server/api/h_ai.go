package api

// ---------- AI 助手（严格只读） ----------
//
// 能力：事件对象诊断 / 日志分析 / YAML 解释 / 生成草稿 / 自然语言修改 / 部署审查 /
//       排查对话 / 总览巡检 / 终端录像审计摘要。
// 边界约定（与平台权限体系一致）：
//   - AI 永远不写集群：生成的 YAML 只是草稿，进编辑器由人确认后走既有 apply/resedit
//     通道（权限校验/字段 diff/审计全部复用），AI 本身没有任何写路径
//   - 喂给模型的上下文按用户权限裁剪：诊断需要目标资源的 get/list 授权，
//     与事件中心的可见性口径一致
//   - 模型调用由后端代理，API Key 只存 Setting 表不回显前端（留空 = 保留原值，
//     与 OAuth clientSecret 同一套约定）；每次调用记审计与用量，按天配额

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"kubeyard/internal/ai"
	"kubeyard/internal/audit"
	"kubeyard/internal/cluster"
	"kubeyard/internal/middleware"
	"kubeyard/internal/model"
)

// kindPlurals 事件表里的 Kind（单数）→ 鉴权用的资源 plural
var kindPlurals = map[string]string{
	"Pod": "pods", "Deployment": "deployments", "StatefulSet": "statefulsets",
	"DaemonSet": "daemonsets", "ReplicaSet": "replicasets", "Job": "jobs", "CronJob": "cronjobs",
	"Node": "nodes", "Namespace": "namespaces", "PersistentVolumeClaim": "persistentvolumeclaims",
	"Service": "services", "Ingress": "ingresses", "ConfigMap": "configmaps", "Secret": "secrets",
	"HorizontalPodAutoscaler": "horizontalpodautoscalers", "PersistentVolume": "persistentvolumes",
	"StorageClass": "storageclasses", "ReplicationController": "replicationcontrollers",
}

func kindToPlural(k string) string {
	if p, ok := kindPlurals[k]; ok {
		return p
	}
	return strings.ToLower(k) + "s"
}

// secretValRe 尽力脱敏：值跟在 password/token/secret/api_key/credential 类键名后面的一律打码。
// Secret data 块里的键名命中同样生效；键名不命中的 base64 值属平台已知边界，不做启发式猜测。
// token_type 是 OAuth 标准字段（值恒为 grant 类型，非机密），替换时排除。
var secretValRe = regexp.MustCompile(`(?im)^(\s*[\w.-]*(?:password|passwd|token|secret|api_?key|credential)[\w.-]*\s*:\s*).+$`)
var tokenTypeRe = regexp.MustCompile(`(?m)^\s*token_type\s*:`)

// inlineSecretRes 内联形态脱敏（日志/事件消息里常见，不是 YAML 键值行）：
// Authorization 头、Bearer 令牌、sk- 密钥、k=v 参数（JDBC 连接串 / URL 查询参数）。
// 日志全文进模型（日志分析 / 诊断附带崩溃日志）后这些是主要漏网点。
var inlineSecretRes = []struct {
	re   *regexp.Regexp
	repl string
}{
	// Authorization 头整行打码（到行尾，覆盖 "Bearer xxx" 等一切凭据形态）
	{regexp.MustCompile(`(?im)^(.*authorization\s*:\s*).+$`), "$1******"},
	{regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9\-._~+/]+=*`), "Bearer ******"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9\-]{16,}`), "sk-******"},
	{regexp.MustCompile(`(?i)([\w.-]*(?:password|passwd|token|secret|api_?key|credential)[\w.-]*)=([^\s&;]+)`), "$1=******"},
}

func maskSecrets(s string) string {
	s = secretValRe.ReplaceAllStringFunc(s, func(line string) string {
		if tokenTypeRe.MatchString(line) { // token_type: Bearer 是标准 OAuth 字段，保留
			return line
		}
		return secretValRe.ReplaceAllString(line, "$1******")
	})
	for _, p := range inlineSecretRes {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}

// ---------- 配置管理（SuperAdmin） ----------

type aiCfgVO struct {
	Provider   string `json:"provider"`
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	HasKey     bool   `json:"has_key"`
	MaxTokens  int    `json:"max_tokens"`
	DailyQuota int    `json:"daily_quota"` // 每用户每日调用上限，0 = 默认 30
	Enabled    bool   `json:"enabled"`
}

// settingGet 读单个 Setting 值（不存在返回空串）。小助手：AI 调用高频路径避免全表拉取。
func (h *H) settingGet(key string) string {
	var s model.Setting
	if err := h.db.Where("`key` = ?", key).First(&s).Error; err != nil {
		return ""
	}
	return s.Value
}

// settingPut upsert 单个 Setting 值
func (h *H) settingPut(key, value string) {
	var s model.Setting
	if err := h.db.Where("`key` = ?", key).First(&s).Error; err == nil {
		s.Value = value
		h.db.Save(&s)
	} else {
		h.db.Create(&model.Setting{Key: key, Value: value})
	}
}

// GetAIConfig GET /api/v1/admin/ai — 配置回显（Key 不回显，只给 has_key）
func (h *H) GetAIConfig(c *gin.Context) {
	vo := aiCfgVO{
		Provider:   h.settingGet("ai.provider"),
		BaseURL:    h.settingGet("ai.base_url"),
		Model:      h.settingGet("ai.model"),
		HasKey:     h.settingGet("ai.api_key") != "",
		MaxTokens:  atoiDefault(h.settingGet("ai.max_tokens"), 0),
		DailyQuota: atoiDefault(h.settingGet("ai.daily_quota"), 30),
		Enabled:    h.settingGet("ai.enabled") == "true",
	}
	c.JSON(http.StatusOK, vo)
}

// PutAIConfig PUT /api/v1/admin/ai — 保存配置；api_key 留空保留原值
func (h *H) PutAIConfig(c *gin.Context) {
	var req struct {
		Provider   string `json:"provider"`
		BaseURL    string `json:"base_url"`
		Model      string `json:"model"`
		APIKey     string `json:"api_key"`
		MaxTokens  int    `json:"max_tokens"`
		DailyQuota int    `json:"daily_quota"`
		Enabled    bool   `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	cfg := ai.Config{Provider: req.Provider, BaseURL: req.BaseURL, Model: req.Model, APIKey: req.APIKey}
	if req.APIKey == "" { // 留空保留
		cfg.APIKey = h.settingGet("ai.api_key")
	}
	if err := cfg.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if req.MaxTokens < 0 || req.MaxTokens > 16384 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "单次回复上限取值 0-16384"})
		return
	}
	if req.DailyQuota < 0 || req.DailyQuota > 10000 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "每日配额取值 0-10000"})
		return
	}
	h.settingPut("ai.provider", req.Provider)
	h.settingPut("ai.base_url", strings.TrimSpace(req.BaseURL))
	h.settingPut("ai.model", strings.TrimSpace(req.Model))
	if req.APIKey != "" {
		h.settingPut("ai.api_key", req.APIKey)
	}
	h.settingPut("ai.max_tokens", fmt.Sprintf("%d", req.MaxTokens))
	h.settingPut("ai.daily_quota", fmt.Sprintf("%d", req.DailyQuota))
	h.settingPut("ai.enabled", fmt.Sprintf("%t", req.Enabled))
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "ai.config", "", "", "", "",
		fmt.Sprintf("更新 AI 模型配置（%s / %s）", providerName(req.Provider), req.Model), "success"))
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}

func providerName(p string) string {
	switch p {
	case "anthropic":
		return "Anthropic Claude"
	case "azure":
		return "Azure OpenAI"
	default:
		return "OpenAI 兼容"
	}
}

// TestAI POST /api/v1/admin/ai/test — 连通性测试（发一句话，收到任意增量即成功）
func (h *H) TestAI(c *gin.Context) {
	cfg, _, _, err := h.aiLoad()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	got := ""
	err = ai.ChatStream(ctx, cfg, "你是连通性测试目标。", "请只回复四个字：连通正常", func(d string) {
		if got == "" {
			got = d
		}
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": "连通失败: " + err.Error()})
		return
	}
	h.aiLog(middleware.UID(c), "test", providerName(cfg.Provider)+" "+cfg.Model, "success")
	c.JSON(http.StatusOK, gin.H{"message": "连通正常（" + providerName(cfg.Provider) + " / " + cfg.Model + "）"})
}

// ---------- 通用：配置装载 / 配额 / 用量 / SSE ----------

// aiLoad 从 Setting 表组装模型配置；返回 (配置, 每日配额, 是否启用, 错误)
func (h *H) aiLoad() (ai.Config, int, bool, error) {
	cfg := ai.Config{
		Provider:  h.settingGet("ai.provider"),
		BaseURL:   h.settingGet("ai.base_url"),
		Model:     h.settingGet("ai.model"),
		APIKey:    h.settingGet("ai.api_key"),
		MaxTokens: atoiDefault(h.settingGet("ai.max_tokens"), 0),
	}
	quota := atoiDefault(h.settingGet("ai.daily_quota"), 30)
	if quota <= 0 {
		quota = 30
	}
	enabled := h.settingGet("ai.enabled") == "true"
	if !enabled {
		return cfg, quota, false, fmt.Errorf("AI 功能未启用，请联系平台管理员在系统设置中配置")
	}
	if err := cfg.Validate(); err != nil {
		return cfg, quota, false, fmt.Errorf("AI 模型配置不完整: %v", err)
	}
	return cfg, quota, true, nil
}

// aiGate 功能前置检查：开关 / 配置 / 每日配额；不通过时已写好 JSON 错误响应
func (h *H) aiGate(c *gin.Context) (*ai.Config, bool) {
	cfg, quota, _, err := h.aiLoad()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": err.Error()})
		return nil, false
	}
	uid := middleware.UID(c)
	var used int64
	day := time.Now().Format("2006-01-02")
	h.db.Model(&model.AiUsageLog{}).
		Where("user_id = ? AND day = ? AND feature <> 'test'", uid, day).Count(&used)
	if used >= int64(quota) {
		c.JSON(http.StatusTooManyRequests, gin.H{"message": fmt.Sprintf("今日 AI 调用已达上限（%d 次/天），明天再来", quota)})
		return nil, false
	}
	return &cfg, true
}

// utf8Trunc 按字节上限截断字符串并保证切在 UTF-8 字符边界：中文/emoji 是
// 多字节序列，被切在中间就成了非法字节，进 JSON 序列化（模型请求/接口响应/
// 入库）会被替换成 U+FFFD 乱码。n ≥ len(s) 时也只修正尾部自身残缺的序列
// （字节流按上限读取、恰好停在字符中间的场合）
func utf8Trunc(s string, n int) string {
	if n < 0 {
		return ""
	}
	if n > len(s) {
		n = len(s)
	}
	// 尾字节是残缺/非法序列（截断切在字符中间，或流本身停在字符中间）时回退；
	// 正常 ASCII 与完整多字节字符（含真实编码的 U+FFFD）不动
	for n > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:n])
		if size > 1 || (size == 1 && r != utf8.RuneError) {
			break
		}
		n--
	}
	return s[:n]
}

// aiLog 记一行用量（配额计数 + 留痕）；target 截断到 255
func (h *H) aiLog(uid uint, feature, target, status string) {
	if len(target) > 255 {
		target = utf8Trunc(target, 255)
	}
	h.db.Create(&model.AiUsageLog{
		UserID: uid, Day: time.Now().Format("2006-01-02"),
		Feature: feature, Target: target, Status: status,
	})
}

// atoiDefault 小整数解析助手（Setting 值都是字符串）
func atoiDefault(s string, def int) int {
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return def
		}
		n = n*10 + int(ch-'0')
		if n > 1_000_000 {
			return def
		}
	}
	if s == "" {
		return def
	}
	return n
}

// aiSSE 初始化 SSE 响应；返回帧发送函数（delta / error / done 三种事件）
func aiSSE(c *gin.Context) func(map[string]any) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	return func(v map[string]any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(c.Writer, "data: %s\n\n", b)
		c.Writer.Flush()
	}
}

// ---------- 功能一：事件对象 AI 诊断 ----------

// AiDiagnose POST /api/v1/ai/diagnose {cluster, namespace, object_type, object_name}
// 上下文 = 该对象近 24h 事件（平台事件库）+（Pod）实例状态 /（工作负载）关联 Pod 状态与其事件。
// 权限：目标资源在目标命名空间的 get 或 list 授权（与事件中心可见性同口径，集群级对象按集群内 list）。
func (h *H) AiDiagnose(c *gin.Context) {
	var req struct {
		Cluster    string `json:"cluster" binding:"required"`
		Namespace  string `json:"namespace"`
		ObjectType string `json:"object_type" binding:"required"`
		ObjectName string `json:"object_name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	resource := kindToPlural(req.ObjectType)
	role, uid := middleware.Role(c), middleware.UID(c)
	target := req.ObjectType + "/" + req.ObjectName

	// 权限：AI 能看见的不能比用户能看见的多
	if role != model.RoleSuperAdmin {
		ok := false
		if req.Namespace != "" {
			ok = middleware.Allowed(h.db, role, uid, req.Cluster, req.Namespace, "get", resource) ||
				middleware.Allowed(h.db, role, uid, req.Cluster, req.Namespace, "list", resource)
		} else {
			// 集群级对象（Node 等）：与事件中心同口径，按集群内该资源的 list 授权
			var cl model.Cluster
			if err := h.db.Where("name = ?", req.Cluster).First(&cl).Error; err == nil {
				grants := middleware.EffectiveGrants(h.db, uid)
				ok = middleware.HasListGrantInCluster(grants, cl.ID, resource)
			}
		}
		if !ok {
			c.JSON(http.StatusForbidden, gin.H{"message": "无该对象的查看权限，AI 诊断需要 " + resource + " 的 get/list 授权"})
			audit.Log(audit.Record(uid, middleware.Username(c), "ai.diagnose", req.Cluster, req.Namespace, resource, req.ObjectName, "权限拒绝", "denied"))
			return
		}
	}

	cfg, ok := h.aiGate(c)
	if !ok {
		return
	}
	e, err := h.mgr.Get(req.Cluster)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}

	// ---------- 组装上下文（全部只读） ----------
	ctx := c.Request.Context()
	var b strings.Builder
	fmt.Fprintf(&b, "集群: %s\n命名空间: %s\n对象: %s\n\n", req.Cluster, orNone(req.Namespace), target)

	// 对象自身近 24h 事件（平台事件库，30s 采集粒度、保留 7 天）。
	// namespace 必须精确匹配（含空串 = 集群级对象）：否则同名 Pod 在其他命名空间的事件
	// 可能落在用户无权可见的范围里；object_type 一并过滤，避免同命名空间同名不同类对象串台
	q := h.db.Where("cluster = ? AND namespace = ? AND object_type = ? AND object_name = ? AND last_time > ?",
		req.Cluster, req.Namespace, req.ObjectType, req.ObjectName, time.Now().Add(-24*time.Hour))
	var evs []model.ClusterEvent
	q.Order("last_time DESC").Limit(50).Find(&evs)
	b.WriteString("== 对象近 24h 事件（最新在前）==\n")
	if len(evs) == 0 {
		b.WriteString("（无）\n")
	}
	for i := range evs {
		fmt.Fprintf(&b, "  %s [%s] %s ×%d %s\n",
			evs[i].LastTime.Local().Format("01-02 15:04"), evs[i].Type, evs[i].Reason, evs[i].Count,
			strings.TrimSpace(evs[i].Message))
	}

	// Pod：补实例状态 + Pod YAML + 日志（容器状态/重启次数最能定位 CrashLoopBackOff / ImagePullBackOff）
	if resource == "pods" && req.Namespace != "" {
		if p, err := e.Client.CoreV1().Pods(req.Namespace).Get(ctx, req.ObjectName, metav1.GetOptions{}); err == nil {
			b.WriteString("\n== Pod 当前状态 ==\n")
			b.WriteString(podStatusText(p))
			// Pod 运行时 YAML（去掉 managedFields 与 status 大块，只留 spec/metadata 剩余部分）：
			// 资源限额/探针/环境变量/挂载等配置问题只看状态和事件定位不到。同样过脱敏
			if y := podSpecYamlText(p); y != "" {
				b.WriteString("\n== Pod YAML（spec 与 metadata，status 已省略）==\n")
				b.WriteString(y)
			}
			// 当前实例日志尾部：运行中报错（连接失败/异常堆栈）通常在 recent 日志里，
			// previous 日志只覆盖崩溃前的最后一幕，两者互补
			if middleware.Allowed(h.db, role, uid, req.Cluster, req.Namespace, "logs", "pods") {
				if logText := h.currentLogsText(ctx, e, req.Namespace, req.ObjectName, p); logText != "" {
					b.WriteString("\n== 当前实例日志尾部（每个容器最多 100 行）==\n")
					b.WriteString(logText)
				}
			}
			// 重启过的容器附带「上一次崩溃实例」的尾部日志：CrashLoopBackOff 的根因
			// （panic / OOM 前的报错）几乎都在 previous 日志里，只有状态和事件定位不到。
			// 日志是独立授权（logs 动词）：AI 看到的 ≤ 用户能看到的，无 logs 权限就不带
			if podRestartTotal(p) > 0 && middleware.Allowed(h.db, role, uid, req.Cluster, req.Namespace, "logs", "pods") {
				if logText := h.previousLogsText(ctx, e, req.Namespace, req.ObjectName, p); logText != "" {
					b.WriteString("\n== 上一次崩溃实例的尾部日志（每个容器最多 80 行）==\n")
					b.WriteString(logText)
				}
			}
		}
	}

	// 工作负载：补运行时 YAML + 关联 Pod 状态与其事件（控制器的告警大多落在 Pod 上，
	// 只看对象自身事件会漏）；Deployment/StatefulSet/DaemonSet/ReplicaSet/Job/CronJob 通用
	if gv, isWl := workloadGVR[resource]; isWl && req.Namespace != "" {
		gvr := e.PreferredGVR(gv[0], gv[1], resource)
		if u, gerr := dynamicRes(e, gvr, req.Namespace).Get(ctx, req.ObjectName, metav1.GetOptions{}); gerr == nil {
			// 运行时 YAML（去 status/managedFields）：模板里的探针/资源限额/环境变量等
			// 配置根因和 Pod 诊断同理，状态与事件里看不到
			if y := objYamlText(u); y != "" {
				b.WriteString("\n== 工作负载 YAML（spec 与 metadata，status 已省略）==\n")
				b.WriteString(y)
			}
			if names, text := h.selectorPodsText(ctx, e, req.Namespace, u); text != "" {
				b.WriteString("\n== 关联 Pod 状态（按工作负载 selector 匹配，最多 10 个）==\n")
				b.WriteString(text)
				// 有关联 Pod 时同样补 previous 日志（重启次数最多的前 2 个 Pod，每容器 60 行）——
				// 工作负载诊断的落点就是这些异常实例；同样受 logs 授权约束
				if len(names) > 0 && middleware.Allowed(h.db, role, uid, req.Cluster, req.Namespace, "logs", "pods") {
					if logText := h.workloadPreviousLogsText(ctx, e, req.Namespace, names); logText != "" {
						b.WriteString("\n== 异常实例上一次崩溃的尾部日志（每容器最多 60 行）==\n")
						b.WriteString(logText)
					}
					// 「未就绪但容器在跑」的实例（探针失败/依赖报错但没崩）错误在当前日志里
					// 持续输出，previous 覆盖不到——补前 2 个此类实例的当前日志尾部
					if logText := h.workloadCurrentLogsText(ctx, e, req.Namespace, names); logText != "" {
						b.WriteString("\n== 异常实例当前日志尾部（每容器最多 100 行）==\n")
						b.WriteString(logText)
					}
				}
				if len(names) > 0 {
					var podEvs []model.ClusterEvent
					h.db.Where("cluster = ? AND namespace = ? AND object_name IN ? AND last_time > ?",
						req.Cluster, req.Namespace, names, time.Now().Add(-24*time.Hour)).
						Order("last_time DESC").Limit(20).Find(&podEvs)
					b.WriteString("\n== 关联 Pod 近 24h 事件（最多 20 条）==\n")
					if len(podEvs) == 0 {
						b.WriteString("（无）\n")
					}
					for i := range podEvs {
						fmt.Fprintf(&b, "  %s %s [%s] %s ×%d %s\n",
							podEvs[i].LastTime.Local().Format("01-02 15:04"), podEvs[i].ObjectName,
							podEvs[i].Type, podEvs[i].Reason, podEvs[i].Count, strings.TrimSpace(podEvs[i].Message))
					}
				}
			}
		}
	}

	system := "你是 Kubernetes 平台的只读诊断助手。基于用户提供的对象事件与状态做根因分析。" +
		"回答用中文，按「结论 / 根因分析 / 修复建议」三段组织，简洁、只依据给定信息、不确定就说不确定。" +
		"修复建议一律通过 KubeYard 平台的白屏编辑完成（改镜像/端口/资源/探针/环境变量等），" +
		"不要输出 kubectl apply/exec/edit 等需要直接操作集群的命令。"

	emit := aiSSE(c)
	// 日志/事件里可能带连接串、token 等敏感内容，发模型前统一脱敏（与 YAML 解释同口径）
	err = ai.ChatStream(ctx, *cfg, system, maskSecrets(b.String()), func(d string) { emit(map[string]any{"delta": d}) })
	if err != nil {
		emit(map[string]any{"error": "模型调用失败: " + err.Error()})
		h.aiLog(uid, "diagnose", req.Cluster+" "+target, "failed")
		audit.Log(audit.Record(uid, middleware.Username(c), "ai.diagnose", req.Cluster, req.Namespace, resource, req.ObjectName, "AI 诊断失败: "+err.Error(), "failed"))
		return
	}
	emit(map[string]any{"done": true})
	h.aiLog(uid, "diagnose", req.Cluster+" "+target, "success")
	audit.Log(audit.Record(uid, middleware.Username(c), "ai.diagnose", req.Cluster, req.Namespace, resource, req.ObjectName, "AI 诊断 "+target, "success"))
}

// workloadGVR 诊断上下文做工作负载增强的资源 → group/version（PreferredGVR 按集群 discovery 校正）
var workloadGVR = map[string][2]string{
	"deployments":  {"apps", "v1"},
	"statefulsets": {"apps", "v1"},
	"daemonsets":   {"apps", "v1"},
	"replicasets":  {"apps", "v1"},
	"jobs":         {"batch", "v1"},
	"cronjobs":     {"batch", "v1"},
}

// objYamlText 任意资源的运行时 YAML 快照（去 status 大块与 metadata.managedFields，
// 超 24KB 截断兜底）。诊断上下文用；敏感值由调用方统一过 maskSecrets。
func objYamlText(u *unstructured.Unstructured) string {
	c := u.DeepCopy()
	delete(c.Object, "status")
	if meta, ok := c.Object["metadata"].(map[string]any); ok {
		delete(meta, "managedFields")
	}
	data, err := yaml.Marshal(c.Object)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(data))
	if len(s) > 24<<10 {
		s = utf8Trunc(s, 24<<10) + "\n...（YAML 过长已截断）"
	}
	return s
}

// selectorPodsText 工作负载对象 → selector 匹配的 Pod 状态摘要（拿不到 selector/失败返回空，
// 诊断降级为仅对象事件）。CronJob 没有顶层 selector：Pod 由 Job 创建、标签继承 jobTemplate
// 的 Pod 模板，从那里取
func (h *H) selectorPodsText(ctx context.Context, e *cluster.Entry, ns string, u *unstructured.Unstructured) ([]string, string) {
	spec, _ := u.Object["spec"].(map[string]any)
	if spec == nil {
		return nil, ""
	}
	match, _, _ := unstructured.NestedMap(spec, "selector", "matchLabels")
	if len(match) == 0 {
		// CronJob：selector 落在 jobTemplate 的 Pod 模板标签上
		match, _, _ = unstructured.NestedMap(spec, "jobTemplate", "spec", "template", "metadata", "labels")
	}
	if len(match) == 0 {
		return nil, ""
	}
	pairs := make([]string, 0, len(match))
	for k, v := range match {
		pairs = append(pairs, fmt.Sprintf("%s=%v", k, v))
	}
	sort.Strings(pairs)
	pods, err := e.Client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: strings.Join(pairs, ","), Limit: 10,
	})
	if err != nil || len(pods.Items) == 0 {
		return nil, ""
	}
	names := make([]string, 0, len(pods.Items))
	var b strings.Builder
	for i := range pods.Items {
		p := &pods.Items[i]
		names = append(names, p.Name)
		fmt.Fprintf(&b, "  %s: %s（重启 %d 次）\n", p.Name, podPhaseText(p), podRestartTotal(p))
	}
	return names, b.String()
}

func podPhaseText(p *corev1.Pod) string {
	if p.Status.Reason != "" {
		return fmt.Sprintf("%s（%s: %s）", p.Status.Phase, p.Status.Reason, p.Status.Message)
	}
	return string(p.Status.Phase)
}

func podRestartTotal(p *corev1.Pod) int {
	n := 0
	for i := range p.Status.ContainerStatuses {
		n += int(p.Status.ContainerStatuses[i].RestartCount)
	}
	return n
}

// podStatusText Pod 状态摘要（复用 describe 的口径，取最能定位问题的字段）
func podStatusText(p *corev1.Pod) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  Phase: %s\n", podPhaseText(p))
	for i := range p.Spec.Containers {
		ct := &p.Spec.Containers[i]
		var st *corev1.ContainerStatus
		for j := range p.Status.ContainerStatuses {
			if p.Status.ContainerStatuses[j].Name == ct.Name {
				st = &p.Status.ContainerStatuses[j]
				break
			}
		}
		if st == nil {
			fmt.Fprintf(&b, "  容器 %s: 镜像 %s | 状态未知\n", ct.Name, ct.Image)
			continue
		}
		state := "<unset>"
		if st.State.Running != nil {
			state = "Running since " + st.State.Running.StartedAt.Time.Local().Format("01-02 15:04")
		} else if st.State.Waiting != nil {
			state = "Waiting (" + st.State.Waiting.Reason
			if st.State.Waiting.Message != "" {
				state += ": " + st.State.Waiting.Message
			}
			state += ")"
		} else if st.State.Terminated != nil {
			state = fmt.Sprintf("Terminated (%d, %s)", st.State.Terminated.ExitCode, st.State.Terminated.Reason)
		}
		fmt.Fprintf(&b, "  容器 %s: 镜像 %s | %s | 重启 %d 次\n", ct.Name, ct.Image, state, st.RestartCount)
	}
	return b.String()
}

func orNone(s string) string {
	if s == "" {
		return "（集群级）"
	}
	return s
}

// previousLogsText 单 Pod 各容器的上一次崩溃实例尾部日志（Previous=true，拿不到/无重启返回空）。
// 每容器最多 lines 行、总体积有上限，诊断上下文不失控；日志内容可能含敏感信息，
// 发模型前过 maskSecrets 由调用方统一处理（与 YAML 解释同一脱敏口径）。
func (h *H) previousLogsText(ctx context.Context, e *cluster.Entry, ns, podName string, p *corev1.Pod) string {
	sc, err := kubernetes.NewForConfig(e.StreamConfig())
	if err != nil {
		return ""
	}
	var b strings.Builder
	const perContainer = 80
	for i := range p.Spec.Containers {
		ct := &p.Spec.Containers[i]
		opts := &corev1.PodLogOptions{Container: ct.Name, Previous: true, TailLines: ptr(int64(perContainer))}
		req := sc.CoreV1().Pods(ns).GetLogs(podName, opts)
		lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		stream, err := req.Stream(lctx)
		if err != nil {
			cancel()
			continue // 拿不到（已清空/还没崩过）就跳过该容器
		}
		limited := io.LimitReader(stream, 32<<10)
		data, _ := io.ReadAll(limited)
		stream.Close()
		cancel()
		if len(bytes.TrimSpace(data)) == 0 {
			continue
		}
		fmt.Fprintf(&b, "-- 容器 %s --\n%s\n", ct.Name,
			strings.TrimSpace(utf8Trunc(string(data), len(data))))
	}
	return b.String()
}

// podSpecYamlText Pod 运行时 YAML 快照（只留 metadata/spec，status 大块省略；
// managedFields 内部跟踪信息体积大且无诊断价值，一并去掉）。
// 转回 corev1.Pod 是因为 typed 对象转 YAML 字段顺序稳定（JSON tag 序），
// 比 unstructured（map 遍历序随机）对模型更友好。超 24KB 截断兜底（极端大 Pod）。
// 敏感值由调用方统一过 maskSecrets。
func podSpecYamlText(p *corev1.Pod) string {
	trimmed := p.DeepCopy()
	trimmed.ManagedFields = nil
	trimmed.Status = corev1.PodStatus{}
	data, err := yaml.Marshal(trimmed)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(data))
	if len(s) > 24<<10 {
		s = utf8Trunc(s, 24<<10) + "\n...（YAML 过长已截断）"
	}
	return s
}

// currentLogsText 单 Pod 各容器的当前实例日志尾部（previous=false）。
// 每容器最多 lines 行、总体积有上限；容器多时只取前 3 个（init 容器通常无持续输出）。
func (h *H) currentLogsText(ctx context.Context, e *cluster.Entry, ns, podName string, p *corev1.Pod) string {
	var b strings.Builder
	const perContainer = 100
	n := len(p.Spec.Containers)
	if n > 3 {
		n = 3
	}
	for i := 0; i < n; i++ {
		ct := &p.Spec.Containers[i]
		data := h.tailLogsText(ctx, e, ns, podName, ct.Name, false, perContainer, 24<<10)
		if data == "" {
			continue
		}
		fmt.Fprintf(&b, "-- 容器 %s --\n%s\n", ct.Name, data)
	}
	return b.String()
}

// workloadPreviousLogsText 工作负载关联 Pod 的 previous 日志：只取重启次数最多的前 2 个 Pod
func (h *H) workloadPreviousLogsText(ctx context.Context, e *cluster.Entry, ns string, podNames []string) string {
	// podNames 按 selector 匹配返回，无序；重新拉状态太重，直接按名字倒序前 2 个再各查重启数不值得——
	// 简化：取前 2 个有 previous 日志的 Pod（K8s 侧拿不到也自动跳过）
	var b strings.Builder
	sc, err := kubernetes.NewForConfig(e.StreamConfig())
	if err != nil {
		return ""
	}
	const maxPods, perContainer = 2, 60
	for i, podName := range podNames {
		if i >= maxPods {
			break
		}
		var p corev1.Pod
		if pp, err := e.Client.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{}); err != nil {
			continue
		} else {
			p = *pp
		}
		if podRestartTotal(&p) == 0 {
			continue // 没重启过的不翻旧账
		}
		for j := range p.Spec.Containers {
			ct := &p.Spec.Containers[j]
			opts := &corev1.PodLogOptions{Container: ct.Name, Previous: true, TailLines: ptr(int64(perContainer))}
			req := sc.CoreV1().Pods(ns).GetLogs(podName, opts)
			lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			stream, err := req.Stream(lctx)
			if err != nil {
				cancel()
				continue
			}
			data, _ := io.ReadAll(io.LimitReader(stream, 24<<10))
			stream.Close()
			cancel()
			if len(bytes.TrimSpace(data)) == 0 {
				continue
			}
			fmt.Fprintf(&b, "-- %s / 容器 %s --\n%s\n", podName, ct.Name,
				strings.TrimSpace(utf8Trunc(string(data), len(data))))
		}
	}
	return b.String()
}

// workloadCurrentLogsText 工作负载关联 Pod 的当前日志：只取「未就绪但有容器在 Running」的
// 前 2 个 Pod——这类实例（探针失败/依赖报错但没崩）的错误在当前日志里持续输出；
// 崩溃实例由 workloadPreviousLogsText 覆盖，全部就绪的健康实例日志没有诊断增量
func (h *H) workloadCurrentLogsText(ctx context.Context, e *cluster.Entry, ns string, podNames []string) string {
	var b strings.Builder
	const maxPods = 2
	n := 0
	for _, podName := range podNames {
		if n >= maxPods {
			break
		}
		p, err := e.Client.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			continue
		}
		allReady, anyRunning := true, false
		for i := range p.Status.ContainerStatuses {
			st := &p.Status.ContainerStatuses[i]
			if !st.Ready {
				allReady = false
			}
			if st.State.Running != nil {
				anyRunning = true
			}
		}
		if allReady || !anyRunning {
			continue
		}
		if text := h.currentLogsText(ctx, e, ns, podName, p); text != "" {
			fmt.Fprintf(&b, "-- %s --\n%s\n", podName, text)
			n++
		}
	}
	return b.String()
}

// ptr int64 指针小助手（PodLogOptions.TailLines）
func ptr(n int64) *int64 {
	return &n
}

// ---------- 功能二：YAML 解释 ----------

// AiYamlExplain POST /api/v1/ai/yaml-explain {yaml} — 逐段解释字段含义 + 指出潜在问题。
// 用户只能粘贴自己已能看到的内容，登录即可用；输入脱敏后发模型。
func (h *H) AiYamlExplain(c *gin.Context) {
	var req struct {
		YAML string `json:"yaml" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误：缺少 YAML 内容"})
		return
	}
	if len(req.YAML) > 64<<10 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "YAML 过大（上限 64KB），请截取关注的片段"})
		return
	}
	cfg, ok := h.aiGate(c)
	if !ok {
		return
	}
	system := "你是 Kubernetes YAML 解读助手。解释用户给出的 YAML：先一句话说明这是什么资源、做什么用，" +
		"再按结构逐段解释关键字段含义（面向不熟悉 K8s 的运维人员，中文、简洁），" +
		"最后列出潜在问题（缺少资源限制/探针、端口与容器不一致、标签选择器可能匹配不到 Pod 等常见事故模式，没有就不列）。" +
		"只依据给定内容，不要臆测未给出的字段。"
	emit := aiSSE(c)
	err := ai.ChatStream(c.Request.Context(), *cfg, system, maskSecrets(req.YAML), func(d string) {
		emit(map[string]any{"delta": d})
	})
	if err != nil {
		emit(map[string]any{"error": "模型调用失败: " + err.Error()})
		h.aiLog(middleware.UID(c), "yaml_explain", "YAML 解释", "failed")
		return
	}
	emit(map[string]any{"done": true})
	h.aiLog(middleware.UID(c), "yaml_explain", "YAML 解释", "success")
}

// ---------- 功能三：自然语言生成 YAML 草稿 ----------

// AiYamlGenerate POST /api/v1/ai/yaml-generate {prompt} — 按描述生成 YAML 草稿。
// 生成物只是草稿：进 YAML 部署编辑器由人审阅，部署时走既有 apply 通道的权限校验与审计，AI 无任何写路径。
func (h *H) AiYamlGenerate(c *gin.Context) {
	var req struct {
		Prompt string `json:"prompt" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误：请描述要生成的资源"})
		return
	}
	if len(req.Prompt) > 4<<10 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "描述过长（上限 4KB）"})
		return
	}
	cfg, ok := h.aiGate(c)
	if !ok {
		return
	}
	system := "你是 Kubernetes YAML 生成助手。按用户的自然语言描述生成一份 YAML，规则：" +
		"只输出一个 YAML 代码块（```yaml ... ```），不要任何解释文字；" +
		"支持 Deployment/StatefulSet/DaemonSet/Job/CronJob/Service/Ingress/ConfigMap/PVC 等常见资源；" +
		"用户没提的字段用合理默认值（如 apiVersion/apps/v1、副本 1、不写 namespace 让部署时选择）；" +
		"镜像是必填项，用户没给就用 nginx:1.27 占位并在首行加注释 # TODO: 替换镜像。" +
		"这份 YAML 只是草稿，用户会在平台编辑器里确认后才部署。"
	emit := aiSSE(c)
	err := ai.ChatStream(c.Request.Context(), *cfg, system, req.Prompt, func(d string) {
		emit(map[string]any{"delta": d})
	})
	if err != nil {
		emit(map[string]any{"error": "模型调用失败: " + err.Error()})
		h.aiLog(middleware.UID(c), "yaml_generate", "生成草稿", "failed")
		return
	}
	emit(map[string]any{"done": true})
	h.aiLog(middleware.UID(c), "yaml_generate", "生成草稿", "success")
}

// ---------- 功能四：全局 AI 排查助手（多轮对话） ----------

// AiChat POST /api/v1/ai/chat {messages:[{role,content}...]} — 排查助手多轮对话，严格只读。
// 系统提示词明确告知助手可见范围=用户授权范围：没有授权的资源助手拿不到数据，
// 引导用户去申请权限而不是臆测；助手不输出 kubectl 类命令，修复路径一律指向平台功能。
// 历史消息截尾保留最近 12 条（3 轮问答上下文足够，控制 token 消耗）。
func (h *H) AiChat(c *gin.Context) {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	if len(req.Messages) == 0 || len(req.Messages) > 40 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "消息数为空或超出上限（40）"})
		return
	}
	for i := range req.Messages {
		if req.Messages[i].Role != "user" && req.Messages[i].Role != "assistant" {
			c.JSON(http.StatusBadRequest, gin.H{"message": "消息角色非法"})
			return
		}
		if len(req.Messages[i].Content) > 16<<10 {
			c.JSON(http.StatusBadRequest, gin.H{"message": "单条消息过长（上限 16KB）"})
			return
		}
	}
	cfg, ok := h.aiGate(c)
	if !ok {
		return
	}
	system := "你是 KubeYard Kubernetes 多集群平台的 AI 排查助手，帮运维人员分析问题、解答概念、给出排查思路。" +
		"规则：回答用中文，简洁分点；" +
		"你无法直接读取集群数据——用户可能会粘贴事件/状态/YAML 给你分析，只依据给定内容判断，不臆测；" +
		"涉及用户看不到的资源时，提示去「我的权限」确认授权或提交工单申请，而不是假装能看到；" +
		"修复建议一律指向 KubeYard 平台的功能（资源页白屏编辑、YAML 部署、事件中心、终端/日志等），" +
		"不要输出 kubectl apply/exec/edit 等绕过平台的命令；" +
		"涉及删除类高危操作时提醒先确认影响范围。"
	// 截尾保留最近 12 条
	msgs := req.Messages
	if len(msgs) > 12 {
		msgs = msgs[len(msgs)-12:]
	}
	aiMsgs := make([]ai.Message, 0, len(msgs))
	for _, m := range msgs {
		aiMsgs = append(aiMsgs, ai.Message{Role: m.Role, Content: m.Content})
	}
	emit := aiSSE(c)
	err := ai.ChatMsgs(c.Request.Context(), *cfg, system, aiMsgs, func(d string) {
		emit(map[string]any{"delta": d})
	})
	if err != nil {
		emit(map[string]any{"error": "模型调用失败: " + err.Error()})
		h.aiLog(middleware.UID(c), "chat", fmt.Sprintf("%d 条消息", len(msgs)), "failed")
		return
	}
	emit(map[string]any{"done": true})
	h.aiLog(middleware.UID(c), "chat", fmt.Sprintf("%d 条消息", len(msgs)), "success")
}

// ---------- 功能五：总览一键 AI 巡检 ----------

// AiInspect POST /api/v1/ai/inspect — 基于总览页预热数据做全局巡检点评。
// 上下文 = Dashboard 同源数据（dashData 组装后序列化），天然继承总览页的权限口径：
// 各集群清单按用户可见命名空间过滤、Warning 事件按可见性过滤——AI 看到的不会比用户在
// 总览页看到的多。数据超过 32KB 时截断明细（保统计头），巡检以概览为主足够。
func (h *H) AiInspect(c *gin.Context) {
	cfg, ok := h.aiGate(c)
	if !ok {
		return
	}
	// 复用 Dashboard 的响应组装（含权限过滤），序列化后作为巡检上下文
	dash := h.dashboardData(c)
	raw, err := json.Marshal(dash)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "总览数据序列化失败: " + err.Error()})
		return
	}
	if len(raw) > 32<<10 {
		// 截断保底：巡检只需要主要问题清单，统计字段在最前，明细溢出部分丢弃
		// （utf8Trunc 同时保证不切在多字节字符中间——JSON 里的中文注释/名称）
		raw = []byte(utf8Trunc(string(raw), 32<<10))
	}
	system := "你是 Kubernetes 平台巡检助手。以下是平台总览页的数据（JSON，已按当前用户权限过滤）。" +
		"输出一份简短巡检报告，中文，结构：「总体结论」一两句话；「需要关注」按优先级列出异常项" +
		"（NotReady 节点、异常 Pod、未就绪应用、Warning 事件热点，各给一句可能原因与建议排查方向）；" +
		"「无异常项」一句话带过。只依据给定数据，数据里没有的不要臆测；" +
		"修复建议指向 KubeYard 平台功能（资源页编辑、事件中心等），不输出 kubectl 命令。"
	emit := aiSSE(c)
	err = ai.ChatStream(c.Request.Context(), *cfg, system, string(raw), func(d string) {
		emit(map[string]any{"delta": d})
	})
	if err != nil {
		emit(map[string]any{"error": "模型调用失败: " + err.Error()})
		h.aiLog(middleware.UID(c), "inspect", "总览巡检", "failed")
		return
	}
	emit(map[string]any{"done": true})
	h.aiLog(middleware.UID(c), "inspect", "总览巡检", "success")
}

// ---------- 功能六：部署前 AI 审查 ----------

// AiYamlReview POST /api/v1/ai/yaml-review {yaml} — 部署前的快速风险审查。
// 与 yaml-explain 的区别：explain 面向理解（逐字段讲含义），review 面向行动（能不能直接部署、
// 有什么风险要改）。输出精简的风险清单，帮助用户在点「部署」前抓住低级错误。
func (h *H) AiYamlReview(c *gin.Context) {
	var req struct {
		YAML string `json:"yaml" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误：缺少 YAML 内容"})
		return
	}
	if len(req.YAML) > 64<<10 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "YAML 过大（上限 64KB），请截取关注的片段"})
		return
	}
	cfg, ok := h.aiGate(c)
	if !ok {
		return
	}
	system := "你是 Kubernetes 部署审查助手。用户即将在平台部署给出的 YAML，做部署前快速审查：" +
		"先给一句总评（可以部署 / 建议修改后再部署 / 有明显错误必须修改）；" +
		"再列问题清单（按严重度排序）：镜像 tag 是否 latest 或缺失、资源 requests/limits 是否缺失、" +
		"探针配置是否合理、端口是否一致、标签选择器是否匹配、命名空间/名称是否合法、" +
		"危险配置（hostPath/privileged/hostNetwork/敏感明文）等；没有问题就说没有问题，不要硬凑。" +
		"中文、简洁、每条一行；只依据给定内容，不臆测。"
	emit := aiSSE(c)
	err := ai.ChatStream(c.Request.Context(), *cfg, system, maskSecrets(req.YAML), func(d string) {
		emit(map[string]any{"delta": d})
	})
	if err != nil {
		emit(map[string]any{"error": "模型调用失败: " + err.Error()})
		h.aiLog(middleware.UID(c), "yaml_review", "部署前审查", "failed")
		return
	}
	emit(map[string]any{"done": true})
	h.aiLog(middleware.UID(c), "yaml_review", "部署前审查", "success")
}

// ---------- 功能七：日志页 AI 分析 ----------

// AiLogs POST /api/v1/ai/logs {cluster, namespace, pod, container, previous}
// 日志查看页一键分析：上下文 = 尾部日志（跟日志页同参数，previous 跟随页面「上一容器」开关）
// + Pod 状态 + 近 24h 事件。权限与日志查看完全同口径（logs 动词），AI 能看到的 ≤ 用户能看到的。
func (h *H) AiLogs(c *gin.Context) {
	var req struct {
		Cluster   string `json:"cluster" binding:"required"`
		Namespace string `json:"namespace" binding:"required"`
		Pod       string `json:"pod" binding:"required"`
		Container string `json:"container"`
		Previous  bool   `json:"previous"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	role, uid := middleware.Role(c), middleware.UID(c)
	if !middleware.Allowed(h.db, role, uid, req.Cluster, req.Namespace, "logs", "pods") {
		c.JSON(http.StatusForbidden, gin.H{"message": "无该容器的日志权限"})
		audit.Log(audit.Record(uid, middleware.Username(c), "ai.logs", req.Cluster, req.Namespace, "pods", req.Pod, "权限拒绝", "denied"))
		return
	}
	cfg, ok := h.aiGate(c)
	if !ok {
		return
	}
	e, err := h.mgr.Get(req.Cluster)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
		return
	}

	ctx := c.Request.Context()
	var b strings.Builder
	ct := req.Container
	if ct == "" {
		ct = "（默认容器）"
	}
	fmt.Fprintf(&b, "集群: %s\n命名空间: %s\nPod: %s\n容器: %s\n\n", req.Cluster, req.Namespace, req.Pod, ct)

	if p, err := e.Client.CoreV1().Pods(req.Namespace).Get(ctx, req.Pod, metav1.GetOptions{}); err == nil {
		b.WriteString("== Pod 当前状态 ==\n")
		b.WriteString(podStatusText(p))
		b.WriteString("\n")
	}

	// 尾部日志：跟日志页参数一致（previous 开关透传），400 行 / 48KB 上限
	logText := h.tailLogsText(ctx, e, req.Namespace, req.Pod, req.Container, req.Previous, 400, 48<<10)
	if logText != "" {
		if req.Previous {
			b.WriteString("\n== 上一次崩溃实例的尾部日志（最多 400 行）==\n")
		} else {
			b.WriteString("\n== 当前实例的尾部日志（最多 400 行）==\n")
		}
		b.WriteString(logText)
	} else {
		b.WriteString("\n== 日志 ==\n（未取到日志）\n")
	}

	// 近 24h 事件（精确四元组，与事件诊断同口径）
	var evs []model.ClusterEvent
	h.db.Where("cluster = ? AND namespace = ? AND object_type = ? AND object_name = ? AND last_time > ?",
		req.Cluster, req.Namespace, "Pod", req.Pod, time.Now().Add(-24*time.Hour)).
		Order("last_time DESC").Limit(20).Find(&evs)
	b.WriteString("\n== 近 24h 事件（最多 20 条）==\n")
	if len(evs) == 0 {
		b.WriteString("（无）\n")
	}
	for i := range evs {
		fmt.Fprintf(&b, "  %s [%s] %s ×%d %s\n",
			evs[i].LastTime.Local().Format("01-02 15:04"), evs[i].Type, evs[i].Reason, evs[i].Count,
			strings.TrimSpace(evs[i].Message))
	}

	system := "你是 Kubernetes 日志分析助手。基于给出的容器日志、Pod 状态与事件做分析，输出：" +
		"「错误聚类」把相同/同源的报错归并并给出出现次数与代表性原文；" +
		"「高频问题 TOP」按频率排出最值得处理的问题；" +
		"「时间线」日志错误与事件/重启的先后关联；" +
		"「结论与建议」根因判断与处理方向。" +
		"日志可能被截断（只取尾部），只依据给定内容，不臆测；中文、简洁分点。" +
		"修复建议指向平台功能（资源页编辑、事件中心等），不输出 kubectl 命令。"
	emit := aiSSE(c)
	err = ai.ChatStream(ctx, *cfg, system, maskSecrets(b.String()), func(d string) { emit(map[string]any{"delta": d}) })
	if err != nil {
		emit(map[string]any{"error": "模型调用失败: " + err.Error()})
		h.aiLog(uid, "logs", req.Cluster+" "+req.Pod, "failed")
		return
	}
	emit(map[string]any{"done": true})
	h.aiLog(uid, "logs", req.Cluster+" "+req.Pod, "success")
	audit.Log(audit.Record(uid, middleware.Username(c), "ai.logs", req.Cluster, req.Namespace, "pods", req.Pod,
		"AI 日志分析 "+req.Pod, "success"))
}

// tailLogsText 容器尾部日志（与日志查看同通道）：拿不到/为空返回空串。
// 日志内容可能含敏感信息，发模型前由调用方统一过 maskSecrets。
func (h *H) tailLogsText(ctx context.Context, e *cluster.Entry, ns, pod, container string, previous bool, lines int, maxBytes int64) string {
	sc, err := kubernetes.NewForConfig(e.StreamConfig())
	if err != nil {
		return ""
	}
	opts := &corev1.PodLogOptions{Container: container, Previous: previous, TailLines: ptr(int64(lines))}
	lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	stream, err := sc.CoreV1().Pods(ns).GetLogs(pod, opts).Stream(lctx)
	if err != nil {
		return ""
	}
	defer stream.Close()
	// LimitReader 按字节上限读取，可能恰好停在多字节字符中间：utf8Trunc 回退掉
	// 尾部残缺字节，否则进模型请求的 JSON 就是一个 U+FFFD 乱码
	data, _ := io.ReadAll(io.LimitReader(stream, maxBytes))
	return strings.TrimSpace(utf8Trunc(string(data), len(data)))
}

// ---------- 功能八：YAML 自然语言修改 ----------

// AiYamlEdit POST /api/v1/ai/yaml-edit {yaml, prompt} — 按自然语言指令输出修改后的完整 YAML。
// 产出仍是草稿：前端 diff 展示、人工确认后才填入编辑器，保存走既有 update 通道
// （权限校验/字段 diff/审计全部复用），AI 无任何写路径。
// 注意：输入不做脱敏——值必须经模型原样往返（脱敏后的 ****** 会覆盖真实值），
// 改由提示词强约束「未涉及字段一律原样保留」，且 diff 界面让人工核对敏感行未变。
func (h *H) AiYamlEdit(c *gin.Context) {
	var req struct {
		YAML   string `json:"yaml" binding:"required"`
		Prompt string `json:"prompt" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误：缺少 YAML 或修改指令"})
		return
	}
	if len(req.YAML) > 64<<10 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "YAML 过大（上限 64KB）"})
		return
	}
	if len(req.Prompt) > 4<<10 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "修改指令过长（上限 4KB）"})
		return
	}
	cfg, ok := h.aiGate(c)
	if !ok {
		return
	}
	system := "你是 Kubernetes YAML 修改助手。用户给出当前 YAML 和一条修改指令，输出修改后的完整 YAML。规则：" +
		"只输出一个 YAML 代码块（```yaml ... ```），不要任何解释文字；" +
		"只改动指令涉及的部分，其余字段（包括注释与顺序）必须逐字原样保留，绝不能改写或丢失；" +
		"不新增指令未要求的内容；镜像地址、密钥类值未涉及时原样复制；" +
		"指令与 YAML 内容无法对应时，输出原文并在 YAML 顶部用注释 # 说明原因。" +
		"这份 YAML 是草稿，用户会在 diff 确认后才应用。"
	user := "修改指令：" + req.Prompt + "\n\n当前 YAML：\n" + req.YAML
	emit := aiSSE(c)
	err := ai.ChatStream(c.Request.Context(), *cfg, system, user, func(d string) {
		emit(map[string]any{"delta": d})
	})
	if err != nil {
		emit(map[string]any{"error": "模型调用失败: " + err.Error()})
		h.aiLog(middleware.UID(c), "yaml_edit", "YAML 修改", "failed")
		return
	}
	emit(map[string]any{"done": true})
	h.aiLog(middleware.UID(c), "yaml_edit", "YAML 修改", "success")
}

// ---------- 功能九：终端录像 AI 审计摘要 ----------

// AiRecordingSummary POST /api/v1/ai/recording-summary {id} — 从 asciicast 提取命令序列生成审计摘要。
// 权限与录像回放同口径（管理员管辖范围：SuperAdmin 全量 / 集群管理员整集群 / 命名空间管理员授权范围）。
func (h *H) AiRecordingSummary(c *gin.Context) {
	var req struct {
		ID uint `json:"id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	var rec model.ExecRecording
	if err := h.db.First(&rec, req.ID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "录像不存在"})
		return
	}
	all, clusterAll, nsOf := middleware.AdminScope(h.db, middleware.Role(c), middleware.UID(c))
	if !all && !middleware.ScopeCovers(rec.Cluster, rec.Namespace, clusterAll, nsOf) {
		c.JSON(http.StatusForbidden, gin.H{"message": "无权查看该录像（不在您管辖的集群/命名空间范围内）"})
		return
	}
	cfg, ok := h.aiGate(c)
	if !ok {
		return
	}
	raw, rawErr := h.recordingRaw(&rec)
	if raw == "" {
		c.JSON(http.StatusNotFound, gin.H{"message": "录像内容不可用: " + rawErr})
		return
	}
	cmds, tailUnentered := castCommandsEx(raw)
	if len(cmds) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "录像中没有可识别的输入命令（可能只有输出/空会话）"})
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "用户: %s\n集群: %s\n命名空间: %s\nPod: %s\n容器: %s\n会话时长: %d 秒\n开始时间: %s\n\n== 输入的命令序列（按时序）==\n",
		rec.Username, rec.Cluster, rec.Namespace, rec.Pod, rec.Container, rec.Duration,
		rec.StartedAt.Local().Format("2006-01-02 15:04"))
	truncated := false
	for i, cmd := range cmds {
		fmt.Fprintf(&b, "%d. %s\n", i+1, cmd)
		// 超长会话截断：不设限会把整个会话塞进上下文，反而因超限整段失败
		if b.Len() > 48<<10 && i < len(cmds)-1 {
			fmt.Fprintf(&b, "（命令过多，共 %d 条，超出摘要上下文上限已截断，完整序列请查看录像回放）\n", len(cmds))
			truncated = true
			break
		}
	}
	// 被高危拦截层吞掉的命令单独列出：这些没有真正执行，与已执行命令混排会误报
	if blocked := castBlocked(raw); len(blocked) > 0 {
		b.WriteString("\n== 已被平台拦截、未执行的命令 ==\n")
		for i, cmd := range blocked {
			fmt.Fprintf(&b, "%d. %s\n", i+1, cmd)
		}
	}
	// 断连瞬间正在输入、未回车：该条只是输入过，不等于执行过（注明具体条目，
	// 避免模型把注记错安到已正常执行的末条命令上）
	if tailUnentered {
		last := cmds[len(cmds)-1]
		if r := []rune(last); len(r) > 60 {
			last = string(r[:60]) + "…"
		}
		fmt.Fprintf(&b, "\n（注：第 %d 条「%s」是会话结束时正在输入、尚未按回车，可能未实际执行）\n", len(cmds), last)
	}

	system := "你是容器终端操作的审计摘要助手。基于给出的终端会话命令序列生成审计摘要：" +
		"「一句话概述」这个人在这个容器里做了什么；" +
		"「操作分类」把命令归为 查看类 / 变更类 / 高危类，各列关键命令；" +
		"「风险提示」发现的高危或可疑操作（删除、提权、改配置、拉远端脚本等）。" +
		"命令序列提取自终端回放（含回显还原），可能有终端补全/方向键残留的噪音字符，合理忽略；" +
		"标注「已被平台拦截、未执行」的命令是尝试过但没有执行的，风险提示中注明未执行，不要计入已执行操作；" +
		"带「尚未按回车」注记的命令同理，只说明用户输入过、不等于已执行，其余命令均已按回车执行。" +
		"只依据给定内容，中文、简洁。"
	emit := aiSSE(c)
	err := ai.ChatStream(c.Request.Context(), *cfg, system, maskSecrets(b.String()), func(d string) {
		emit(map[string]any{"delta": d})
	})
	if err != nil {
		emit(map[string]any{"error": "模型调用失败: " + err.Error()})
		h.aiLog(middleware.UID(c), "recording", fmt.Sprintf("录像 #%d", rec.ID), "failed")
		return
	}
	emit(map[string]any{"done": true})
	logTail := ""
	if truncated {
		logTail = "，已截断"
	}
	h.aiLog(middleware.UID(c), "recording", fmt.Sprintf("录像 #%d（%d 条命令%s）", rec.ID, len(cmds), logTail), "success")
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "ai.recording", rec.Cluster, rec.Namespace, "pods", rec.Pod,
		fmt.Sprintf("AI 摘要录像 #%d（%s）", rec.ID, rec.Username), "success"))
}
