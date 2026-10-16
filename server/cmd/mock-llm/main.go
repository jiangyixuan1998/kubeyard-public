// mock-llm 本地假模型服务（仅开发测试用，不进生产镜像）：
//   go run ./cmd/mock-llm
// 然后在 系统设置 → AI 模型 里配置：协议 OpenAI 兼容 / 地址 http://localhost:18080 / 模型名见下。
//
// 模型名即行为开关：
//   mock            默认：把最后一条 user 消息原样流式回显——用于人工核对后端发给模型的
//                   上下文（权限裁剪是否正确、脱敏是否生效、上下文组装是否完整）
//   mock-nonstream  忽略 stream 参数回普通 JSON——验证非流式兜底路径
//   mock-empty      流式打开但零增量正常结束——验证「空回复显式报错」兜底
//   mock-reasoning  先回 reasoning_content 再回正文——验证思考型模型透传与分隔头
//   mock-slow       每 delta 间隔 300ms——验证前端流式渲染与超时
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type msg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type reqBody struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []msg  `json:"messages"`
}

func lastUser(b reqBody) string {
	for i := len(b.Messages) - 1; i >= 0; i-- {
		if b.Messages[i].Role == "user" {
			return b.Messages[i].Content
		}
	}
	return ""
}

func sseFrame(w http.ResponseWriter, flusher http.Flusher, content, reasoning string) {
	d := map[string]any{"content": content}
	if reasoning != "" {
		d["reasoning_content"] = reasoning
	}
	chunk := map[string]any{
		"id": "mock", "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": "mock",
		"choices": []any{map[string]any{"index": 0, "delta": d, "finish_reason": nil}},
	}
	b, _ := json.Marshal(chunk)
	fmt.Fprintf(w, "data: %s\n\n", b)
	flusher.Flush()
}

func handle(w http.ResponseWriter, r *http.Request) {
	var b reqBody
	json.NewDecoder(r.Body).Decode(&b)
	w.Header().Set("Content-Type", "application/json")

	// 非流式兜底路径
	if !b.Stream || b.Model == "mock-nonstream" {
		text := lastUser(b)
		if b.Model == "mock-nonstream" {
			text = "【非流式回复】" + text
		}
		resp := map[string]any{
			"id": "mock", "object": "chat.completion", "created": time.Now().Unix(), "model": b.Model,
			"choices": []any{map[string]any{
				"index": 0,
				"message": map[string]string{
					"role": "assistant", "content": text,
					"reasoning_content": "",
				},
				"finish_reason": "stop",
			}},
		}
		w.Write([]byte("data: " + mustJSON(resp) + "\n\ndata: [DONE]\n\n"))
		// 注意：虽然 Content-Type 是 application/json，但按 SSE data: 格式包了 body——
		// 真实网关的非法行为模拟（后端会因 Content-Type 走整体 JSON 解析而失败）。
		// 用 mock-nonstream 验证标准非流式路径即可，此分支仅保底不炸。
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher := w.(http.Flusher)

	switch b.Model {
	case "mock-empty":
		fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{
			"choices": []any{map[string]any{"delta": map[string]string{}}},
		}))
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	case "mock-reasoning":
		for _, seg := range []string{"先分析", "一下输入", "再作答。"} {
			sseFrame(w, flusher, "", seg)
			time.Sleep(120 * time.Millisecond)
		}
		text := "【回答】" + firstLine(lastUser(b))
		emitText(w, flusher, text)
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	case "mock-slow":
		text := lastUser(b)
		for len(text) > 0 {
			n := 40
			if len(text) < n {
				n = len(text)
			}
			// 按字节切不安全，按 rune 切
			runes := []rune(text)
			if len(runes) > 20 {
				n = len(string(runes[:20]))
			}
			sseFrame(w, flusher, text[:n], "")
			text = text[n:]
			time.Sleep(300 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}

	// 默认 mock：回显最后一条 user 消息（上下文核对用）
	emitText(w, flusher, lastUser(b))
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func emitText(w http.ResponseWriter, flusher http.Flusher, text string) {
	for len(text) > 0 {
		runes := []rune(text)
		n := len(runes)
		if n > 24 {
			n = 24
		}
		sseFrame(w, flusher, string(runes[:n]), "")
		text = string(runes[n:])
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func main() {
	http.HandleFunc("/v1/chat/completions", handle)
	fmt.Println("mock-llm listening on http://localhost:18080  (models: mock / mock-nonstream / mock-empty / mock-reasoning / mock-slow)")
	http.ListenAndServe("127.0.0.1:18080", nil)
}
