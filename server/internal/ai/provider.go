package ai

// AI 能力调用层：严格只读（诊断/解释/生成草稿），模型调用一律由后端代理，
// API Key 不出后端。三种协议同一套流式接口：
//   - openai   兼容协议（DeepSeek / 通义 compatible-mode / vLLM / Ollama / 内部网关等绝大多数）
//   - anthropic Claude 官方协议
//   - azure    Azure OpenAI（部署式 URL + api-key 头）
// 配置存 Setting 表（ai.* 键），由 api 层读出注入，本包不依赖数据库。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config 模型接入配置（api 层从 Setting 表读出组装）
type Config struct {
	Provider  string // openai / anthropic / azure
	BaseURL   string // 填到 /v1 为止，平台拼 /chat/completions（openai）；anthropic/azure 各自拼法
	Model     string
	APIKey    string
	MaxTokens int // 单次回复上限，0 用默认
}

// Validate 配置完整性（保存时校验）
func (c *Config) Validate() error {
	switch c.Provider {
	case "openai", "anthropic", "azure":
	default:
		return errors.New("协议仅支持 openai（兼容）/ anthropic / azure")
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("服务地址不能为空")
	}
	if strings.TrimSpace(c.Model) == "" {
		return errors.New("模型名不能为空")
	}
	if c.Provider != "azure" && strings.TrimSpace(c.APIKey) == "" {
		return errors.New("API Key 不能为空")
	}
	return nil
}

func (c *Config) maxTokens() int {
	if c.MaxTokens > 0 {
		return c.MaxTokens
	}
	return 4096 // 思考型模型（DeepSeek-R1/Qwen3 等）思考段也计入，2048 容易被耗尽只剩思考没正文
}

// ChatStream 流式对话：system + user 单轮（诊断/解释/生成/巡检/审查用）
func ChatStream(ctx context.Context, cfg Config, system, user string, onDelta func(string)) error {
	return ChatMsgs(ctx, cfg, system, []Message{{Role: "user", Content: user}}, onDelta)
}

// Message 多轮对话消息（openai 风格 role：user / assistant）
type Message struct {
	Role    string
	Content string
}

// ChatMsgs 多轮流式对话：system + 任意 user/assistant 序列，逐段回调 onDelta。
// 整体限 3 分钟（生成类回答较长，但挂起必须能退出）。
// 兜底：全程一个增量都没收到却"正常结束"（网关不支持流式/推理模型 content 为空等），
// 必须显式报错——否则前端流式页面会永远停在等待态。
func ChatMsgs(ctx context.Context, cfg Config, system string, msgs []Message, onDelta func(string)) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	emitted := false
	wrapped := func(d string) {
		emitted = true
		onDelta(d)
	}
	var err error
	switch cfg.Provider {
	case "anthropic":
		err = anthropicMsgs(ctx, cfg, system, msgs, wrapped)
	case "azure":
		err = openaiMsgs(ctx, cfg, azureURL(cfg), map[string]string{"api-key": cfg.APIKey}, false, system, msgs, wrapped)
	default:
		// 常见写法只填到域名/端口（如 http://llm.internal:8000 或 https://api.deepseek.com），
		// 自动补 /v1；已带 /v1（vLLM/网关标准前缀）则原样
		base := strings.TrimRight(cfg.BaseURL, "/")
		if !strings.HasSuffix(base, "/v1") {
			base += "/v1"
		}
		err = openaiMsgs(ctx, cfg, base+"/chat/completions",
			map[string]string{"Authorization": "Bearer " + cfg.APIKey}, true, system, msgs, wrapped)
	}
	if err == nil && !emitted {
		return errors.New("模型未返回任何内容（网关可能不支持流式/请求被网关挂起断开，或单次回复上限过小被截断）")
	}
	return err
}

// azureURL Azure OpenAI 部署式地址：{resource}/openai/deployments/{model}/chat/completions?api-version=...
func azureURL(cfg Config) string {
	return strings.TrimRight(cfg.BaseURL, "/") + "/openai/deployments/" + cfg.Model + "/chat/completions?api-version=2024-02-01"
}

// openaiMsgs OpenAI 兼容 / Azure 共用：请求体与 SSE 帧同构，仅 URL、鉴权头、是否带 model 字段不同
func openaiMsgs(ctx context.Context, cfg Config, url string, headers map[string]string, withModel bool, system string, msgs []Message, onDelta func(string)) error {
	ms := make([]map[string]string, 0, len(msgs)+1)
	ms = append(ms, map[string]string{"role": "system", "content": system})
	for _, m := range msgs {
		ms = append(ms, map[string]string{"role": m.Role, "content": m.Content})
	}
	body := map[string]any{
		"messages":    ms,
		"stream":      true,
		"max_tokens":  cfg.maxTokens(),
		"temperature": 0.3, // 诊断/解释要稳，压低随机性
	}
	if withModel {
		body["model"] = cfg.Model
	}
	thinking := false // 已透出思考分隔头
	content := false  // 已出现正文
	err := ssePost(ctx, url, headers, body, func(data []byte, onDelta func(string)) error {
		var frame struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"` // 部分部署用这个字段名
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			return nil // 忽略无法解析的帧（部分网关会夹带非标准事件）
		}
		if frame.Error != nil {
			return errors.New(frame.Error.Message)
		}
		if len(frame.Choices) == 0 {
			return nil
		}
		d := frame.Choices[0].Delta
		// 推理模型（DeepSeek-R1 / Qwen3 等内部部署常见）：思考过程走 reasoning_content，
		// 流式透出让用户看得到进度（不再干等）；正文出现时加分隔头
		if th := d.ReasoningContent + d.Reasoning; th != "" {
			if !thinking {
				onDelta("—— 思考过程 ——\n")
				thinking = true
			}
			onDelta(th)
		}
		if d.Content != "" {
			if thinking && !content {
				onDelta("\n\n—— 回答 ——\n")
			}
			content = true
			onDelta(d.Content)
		}
		return nil
	}, func(data []byte, onDelta func(string)) error {
		// 非流式兜底：网关忽略 stream 参数时回普通 chat completion JSON
		var whole struct {
			Choices []struct {
				Message struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"message"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &whole); err != nil {
			return fmt.Errorf("无法解析模型响应: %w", err)
		}
		if whole.Error != nil {
			return errors.New(whole.Error.Message)
		}
		if len(whole.Choices) == 0 {
			return nil
		}
		m := whole.Choices[0].Message
		if m.Content != "" {
			onDelta(m.Content)
			return nil
		}
		if m.ReasoningContent != "" {
			onDelta("—— 思考过程（正文为空，多为推理模型把单次回复上限耗尽，可在系统设置调大）——\n" + m.ReasoningContent)
		}
		return nil
	}, onDelta)
	if err != nil {
		return err
	}
	if thinking && !content {
		// 思考流完了正文没来：基本是推理模型把 max_tokens 全花在思考上被截断
		onDelta("\n\n（模型只输出了思考过程、正文为空——多为「单次回复上限」被思考耗尽，可在系统设置调大后重试）")
	}
	return nil
}

// anthropicMsgs Claude 官方 /v1/messages 协议（多轮）
func anthropicMsgs(ctx context.Context, cfg Config, system string, msgs []Message, onDelta func(string)) error {
	ms := make([]map[string]string, 0, len(msgs))
	for _, m := range msgs {
		ms = append(ms, map[string]string{"role": m.Role, "content": m.Content})
	}
	body := map[string]any{
		"model":      cfg.Model,
		"max_tokens": cfg.maxTokens(),
		"system":     system,
		"messages":   ms,
		"stream":     true,
	}
	headers := map[string]string{
		"x-api-key":         cfg.APIKey,
		"anthropic-version": "2023-06-01",
	}
	return ssePost(ctx, strings.TrimRight(cfg.BaseURL, "/")+"/v1/messages", headers, body, func(data []byte, onDelta func(string)) error {
		var frame struct {
			Type  string `json:"type"`
			Delta struct {
				Text string `json:"text"`
			} `json:"delta"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			return nil
		}
		if frame.Error != nil {
			return errors.New(frame.Error.Message)
		}
		if frame.Type == "content_block_delta" && frame.Delta.Text != "" {
			onDelta(frame.Delta.Text)
		}
		return nil
	}, func(data []byte, onDelta func(string)) error {
		// 非流式兜底：响应为整体 JSON（content 是文本块数组）
		var whole struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &whole); err != nil {
			return fmt.Errorf("无法解析模型响应: %w", err)
		}
		if whole.Error != nil {
			return errors.New(whole.Error.Message)
		}
		for _, blk := range whole.Content {
			if blk.Type == "text" && blk.Text != "" {
				onDelta(blk.Text)
			}
		}
		return nil
	}, onDelta)
}

// ssePost 三协议共用的 SSE 读取骨架：POST JSON → 逐 data: 帧回调 parseFrame → [DONE] 结束。
// parseWhole 是非流式兜底：部分内网网关忽略 stream 参数直接回普通 JSON（Content-Type 不是
// text/event-stream），此时整体读取按协议各自的非流式结构解析。
func ssePost(ctx context.Context, url string, headers map[string]string, body map[string]any,
	parseFrame func(data []byte, onDelta func(string)) error,
	parseWhole func(data []byte, onDelta func(string)) error,
	onDelta func(string)) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// 专用客户端：显式整体超时（覆盖 DefaultClient 的无限等待）。网关偶发 60s 挂起断流时，
	// 这里 90s 兜底退出并返回可读错误，而非等对端先断
	client := &http.Client{Timeout: 200 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("连接模型服务失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		// LimitReader 可能切在多字节字符中间：去掉残缺字节，否则错误信息进
		// JSON 响应时变成 U+FFFD 乱码
		msg := strings.ToValidUTF8(strings.TrimSpace(string(b)), "")
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("模型服务返回 %s: %s", resp.Status, msg)
	}

	// 非流式响应兜底（上限 8MB，正常回答远小于此）
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if rerr != nil {
			return fmt.Errorf("读取模型响应失败: %w", rerr)
		}
		return parseWhole(b, onDelta)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			if data == "[DONE]" {
				return nil
			}
			continue
		}
		if err := parseFrame([]byte(data), onDelta); err != nil {
			return err
		}
	}
	return scanner.Err()
}
