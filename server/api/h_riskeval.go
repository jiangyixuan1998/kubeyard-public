package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"kubeyard/internal/ai"
	"kubeyard/internal/audit"
	"kubeyard/internal/middleware"
	"kubeyard/internal/model"
	"kubeyard/ws"

	"github.com/gin-gonic/gin"
)

// ---------- 终端会话 AI 风险评估 ----------
//
// 终端会话结束后自动调平台配置的模型评估命令序列风险（low/medium/high + 一句话理由），
// 结果持久化到录像行——管理员在录像列表直接按风险等级筛高危会话，不必逐个点「AI 摘要」。
// 评估失败静默（风险列留空=未评估）：AI 是增强能力，不能因模型不可用影响会话收尾。

// riskEvalTimeout 单次评估上限；模型挂起不能拖住 goroutine 池
const riskEvalTimeout = 2 * time.Minute
const riskEvalAttempts = 3

// 评估并发闸：多个终端同时结束时每个会话各起一个 goroutine，无上限的并发会挤爆
// 模型网关（网关对突发并发限流/断流 → 一批评估全失败 → 总有几个留空）。
// 全局 2 路并发 + 队列 200：多余的任务在队列里排队（排队不算失败），极端洪峰超队列才丢弃
var (
	riskEvalSema = make(chan struct{}, 2)                          // 并发上限
	riskEvalGate = make(chan struct{}, 200)                        // 队列容量
)

// acquireRiskSlot 获取评估槽位：先占队列位再等并发位；等待期间 ctx 取消即放弃
func acquireRiskSlot(ctx context.Context) bool {
	select {
	case riskEvalGate <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	defer func() { <-riskEvalGate }()
	select {
	case riskEvalSema <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// aiRiskEvaluator 供 ws.HandleExec 注入的评估回调：读录像 → 调模型 → 解析等级 → 回写行。
// 模型及结果写库做有限重试；失败时记录会话 ID，便于排查未评估记录。
func (h *H) aiRiskEvaluator() ws.RiskEvaluator {
	return func(rec *model.ExecRecording) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[ai-risk] 评估会话 %s panic: %v", rec.SessionID, r)
			}
		}()
		cfg, _, enabled, err := h.aiLoadQuiet()
		if err != nil || !enabled {
			log.Printf("[ai-risk] 会话 %s 未执行自动评审：AI 配置不可用（启用=%t，错误=%v）", rec.SessionID, enabled, err)
			return // AI 未启用：留空 = 未评估
		}

		// 全局限流：排队等并发槽位（重试全程持有，避免重试也落在并发峰值里互相挤兑）。
		// 排队上下文 10 分钟：队列极端积压时的兜底放弃（正常几十秒内轮到）
		queueCtx, queueCancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer queueCancel()
		if !acquireRiskSlot(queueCtx) {
			log.Printf("[ai-risk] 会话 %s 排队超时/取消，放弃自动评审", rec.SessionID)
			return
		}
		var result riskResult
		for attempt := 1; attempt <= riskEvalAttempts; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), riskEvalTimeout)
			result = h.evalRecordingRisk(ctx, cfg, rec)
			cancel()
			if result.level != "" {
				break
			}
			if attempt < riskEvalAttempts {
				log.Printf("[ai-risk] 会话 %s 第 %d/%d 次评审未得到有效结果，稍后重试", rec.SessionID, attempt, riskEvalAttempts)
				time.Sleep(time.Duration(attempt) * 2 * time.Second)
			}
		}
		if result.level == "" {
			log.Printf("[ai-risk] 会话 %s 自动评审失败：重试 %d 次后仍无有效结果", rec.SessionID, riskEvalAttempts)
			return // 评估失败（录像不可用/模型错误）：留空=未评估
		}
		var updateErr error
		for attempt := 1; attempt <= riskEvalAttempts; attempt++ {
			updateErr = h.db.Model(rec).Updates(map[string]any{"risk_level": result.level, "risk_why": result.why}).Error
			if updateErr == nil {
				break
			}
			if attempt < riskEvalAttempts {
				log.Printf("[ai-risk] 会话 %s 第 %d/%d 次评审结果写库失败: %v；稍后重试", rec.SessionID, attempt, riskEvalAttempts, updateErr)
				time.Sleep(time.Duration(attempt) * 2 * time.Second)
			}
		}
		if updateErr != nil {
			log.Printf("[ai-risk] 会话 %s 评审结果写库失败（已重试 %d 次）: %v", rec.SessionID, riskEvalAttempts, updateErr)
			return
		}
		if result.level != "none" {
			// 用量留痕：系统行为不计用户每日配额，但记录调用量可审计（AI 用量页可见）；
			// 空会话没调模型，不计用量
			h.aiLogSystem("risk-eval", fmt.Sprintf("录像 #%d（%s/%s）→ %s", rec.ID, rec.Cluster, rec.Pod, result.level))
		}
		log.Printf("[ai-risk] 会话 %s 自动评审完成：%s", rec.SessionID, result.level)
	}
}

// riskResult 评估结果
type riskResult struct {
	level string // low | medium | high
	why   string
}

// evalRecordingRisk 读录像提取命令序列 → 模型评级 → 解析 JSON 输出。失败返回零值。
func (h *H) evalRecordingRisk(ctx context.Context, cfg ai.Config, rec *model.ExecRecording) riskResult {
	raw, rawErr := h.recordingRaw(rec)
	if raw == "" {
		// 内容不可用：区分「文件被清理/丢失」与其他——给 lost 等级让 UI 显示明确状态
		if strings.Contains(rawErr, "不存在") || strings.Contains(rawErr, "不可用") {
			return riskResult{level: "lost", why: "录像内容丢失（" + rawErr + "），无法评估"}
		}
		return riskResult{}
	}
	cmds, tailUnentered := castCommandsEx(raw)
	if len(cmds) == 0 {
		// 空会话（纯输出/误开终端）：无命令可评，标 none 而非留空——
		// 留空 = 未评估（一直挂在「未评估」筛选里诱导重复重评），none = 无可评估内容
		return riskResult{level: "none", why: "空会话：无可识别的输入命令"}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "用户: %s\n集群: %s\n命名空间: %s\nPod: %s\n容器: %s\n会话时长: %d 秒\n开始时间: %s\n\n== 输入的命令序列（按时序）==\n",
		rec.Username, rec.Cluster, rec.Namespace, rec.Pod, rec.Container, rec.Duration, rec.StartedAt.Local().Format("2006-01-02 15:04"))
	// 未回车的末条程序级剔除（不送模型）：文字注记让模型「忽略」不等于排除，
	// 直接不进上下文才与「尚未按回车不计入」语义严格一致
	evalCmds := cmds
	if tailUnentered && len(evalCmds) > 0 {
		evalCmds = evalCmds[:len(evalCmds)-1]
	}
	for i, cmd := range evalCmds {
		fmt.Fprintf(&b, "%d. %s\n", i+1, cmd)
		if b.Len() > 24<<10 && i < len(cmds)-1 { // 评估比摘要更短（只要 JSON），24KB 足够
			break
		}
	}
	if blocked := castBlocked(raw); len(blocked) > 0 {
		b.WriteString("\n== 已被平台拦截、未执行的命令 ==\n")
		for i, cmd := range blocked {
			fmt.Fprintf(&b, "%d. %s\n", i+1, cmd)
		}
	}
	if tailUnentered {
		b.WriteString("\n（注：会话结束时正在输入的末条命令未按回车、可能未执行，已不计入上方序列）\n")
	}

	system := "你是容器终端操作的安全审计员。基于命令序列评估该会话的风险等级，只输出一行 JSON：" +
		`{"level":"low|medium|high","why":"一句话理由（中文，≤60字）"}` + "\n" +
		"评级标准——high：删除/覆盖数据、提权、改系统配置、拉取并执行远端脚本、绕过审计、横向移动痕迹；" +
		"medium：批量变更、写敏感路径、安装/卸载软件、curl 外联、反复试错未遂；" +
		"low：纯查看类（cat/ls/ps/env 等只读操作）或常规业务操作。" +
		"被拦截未执行的命令按 medium 起评（有主观故意）；「尚未按回车」的命令不计入。" +
		"终端回放提取可能含补全/方向键噪音，合理忽略。只输出 JSON，不要其他文字。"
	var out strings.Builder
	if err := ai.ChatStream(ctx, cfg, system, maskSecrets(b.String()), func(d string) { out.WriteString(d) }); err != nil {
		fmt.Println("[ai-risk] model error:", err)
		return riskResult{}
	}
	return parseRiskJSON(out.String())
}

// parseRiskJSON 从模型输出提取风险 JSON（容忍 markdown 代码块包裹/前后噪声）
func parseRiskJSON(out string) riskResult {
	s := strings.TrimSpace(out)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			s = s[i : j+1]
		}
	}
	var r struct {
		Level string `json:"level"`
		Why   string `json:"why"`
	}
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return riskResult{}
	}
	switch strings.ToLower(strings.TrimSpace(r.Level)) {
	case "high":
		return riskResult{"high", truncWhy(r.Why)}
	case "medium":
		return riskResult{"medium", truncWhy(r.Why)}
	case "low":
		return riskResult{"low", truncWhy(r.Why)}
	case "none":
		return riskResult{"none", truncWhy(r.Why)}
	}
	return riskResult{}
}

func truncWhy(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:120])
	}
	return s
}

// aiLogSystem 系统级 AI 用量留痕（UserID=0 = 平台自动触发）：自动风险评估不计任何用户配额，
// 但落用量记录可审计调用量（系统在 AiUsageLog 里 UserID=0 的行即自动评估次数）
func (h *H) aiLogSystem(feature, target string) {
	if len(target) > 255 {
		target = utf8Trunc(target, 255)
	}
	h.db.Create(&model.AiUsageLog{
		UserID: 0, Day: time.Now().Format("2006-01-02"),
		Feature: feature, Target: target, Status: "success",
	})
}

// aiLoadQuiet 同 aiLoad 但不写响应；仅判断可用性（后台评估无 HTTP 上下文）
func (h *H) aiLoadQuiet() (ai.Config, int, bool, error) {
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
	if h.settingGet("ai.enabled") != "true" {
		return cfg, quota, false, fmt.Errorf("AI 未启用")
	}
	if err := cfg.Validate(); err != nil {
		return cfg, quota, false, err
	}
	return cfg, quota, true, nil
}

// RecordingRiskReEval POST /api/v1/admin/exec-recordings/:id/risk-reeval — 手动重新评估单条录像风险。
// 管辖范围与回放同口径；模型未启用返回 503。
func (h *H) RecordingRiskReEval(c *gin.Context) {
	var rec model.ExecRecording
	if err := h.db.First(&rec, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "录像不存在"})
		return
	}
	all, clusterAll, nsOf := middleware.AdminScope(h.db, middleware.Role(c), middleware.UID(c))
	if !all && !middleware.ScopeCovers(rec.Cluster, rec.Namespace, clusterAll, nsOf) {
		c.JSON(http.StatusForbidden, gin.H{"message": "无权操作该录像（不在您管辖范围内）"})
		return
	}
	cfg, _, enabled, err := h.aiLoadQuiet()
	if err != nil || !enabled {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "AI 功能未启用或模型配置不完整，请在系统设置中配置"})
		return
	}
	result := h.evalRecordingRisk(context.Background(), cfg, &rec)
	if result.level == "" {
		c.JSON(http.StatusBadGateway, gin.H{"message": "评估失败：录像不可用或模型调用失败"})
		return
	}
	if uerr := h.db.Model(&rec).Updates(map[string]any{"risk_level": result.level, "risk_why": result.why}).Error; uerr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "评估结果写入失败: " + uerr.Error()})
		return
	}
	h.aiLog(middleware.UID(c), "risk-eval", fmt.Sprintf("录像 #%d（%s/%s）→ %s", rec.ID, rec.Cluster, rec.Pod, result.level), "success")
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "ai.recording", rec.Cluster, rec.Namespace, "pods", rec.Pod,
		fmt.Sprintf("AI 风险重评录像 #%d → %s", rec.ID, result.level), "success"))
	c.JSON(http.StatusOK, gin.H{"risk_level": result.level, "risk_why": result.why})
}

// RecordingRiskReEvalBatch POST /api/v1/admin/exec-recordings/risk-reeval-batch {ids: []}
// 批量重评（历史录像补评/模型更新后批量刷新）。管辖范围逐条校验，越权/不可评估的条目
// 单独计入结果不中断整批；模型调用串行（后台评估已并发于会话结束路径，此处批量是管理操作，
// 串行避免打爆网关）。同步执行但受 ctx 超时保护——千条级建议分页多次调用。
func (h *H) RecordingRiskReEvalBatch(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误：ids 必填"})
		return
	}
	if len(req.IDs) > 50 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "单批最多 50 条，请分批提交"})
		return
	}
	all, clusterAll, nsOf := middleware.AdminScope(h.db, middleware.Role(c), middleware.UID(c))
	cfg, _, enabled, err := h.aiLoadQuiet()
	if err != nil || !enabled {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "AI 功能未启用或模型配置不完整，请在系统设置中配置"})
		return
	}
	type itemResult struct {
		ID    uint   `json:"id"`
		Level string `json:"risk_level,omitempty"`
		Why   string `json:"risk_why,omitempty"`
		Err   string `json:"error,omitempty"`
	}
	ok, failed, skipped := 0, 0, 0
	results := make([]itemResult, 0, len(req.IDs))
	for _, id := range req.IDs {
		var rec model.ExecRecording
		if err := h.db.First(&rec, id).Error; err != nil {
			failed++
			results = append(results, itemResult{ID: id, Err: "录像不存在"})
			continue
		}
		if !all && !middleware.ScopeCovers(rec.Cluster, rec.Namespace, clusterAll, nsOf) {
			skipped++
			results = append(results, itemResult{ID: id, Err: "无权操作（不在管辖范围）"})
			continue
		}
		result := h.evalRecordingRisk(context.Background(), cfg, &rec)
		if result.level == "" {
			failed++
			results = append(results, itemResult{ID: id, Err: "评估失败（录像不可用/无可识别命令/模型调用失败）"})
			continue
		}
		if uerr := h.db.Model(&rec).Updates(map[string]any{"risk_level": result.level, "risk_why": result.why}).Error; uerr != nil {
			failed++
			results = append(results, itemResult{ID: id, Err: "结果写入失败"})
			continue
		}
		ok++
		results = append(results, itemResult{ID: id, Level: result.level, Why: result.why})
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "ai.recording", "", "", "pods", "",
		fmt.Sprintf("AI 风险批量重评 %d 条：成功 %d / 失败 %d / 跳过 %d", len(req.IDs), ok, failed, skipped), "success"))
	c.JSON(http.StatusOK, gin.H{"ok": ok, "failed": failed, "skipped": skipped, "results": results})
}
