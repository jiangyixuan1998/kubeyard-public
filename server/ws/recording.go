package ws

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/tools/remotecommand"
)

// ---------- 录制器：内存缓冲 + 周期落库（无 PVC 部署形态） ----------
//
// 旧实现：cast 写本地文件，会话结束一次性入库——Pod 无持久卷时重启 = 录制中的会话全部丢失
//（文件在 emptyDir 里被销毁，收尾代码也没机会执行）。
// 新实现：内容写内存缓冲，由外部按周期 UPSERT 到 exec_recordings.content——
// Pod 被杀最多丢最后一个周期（默认 30s），不再依赖 PVC 或本地文件。

// maxCastBytes 单条录像入库上限；超出（超长会话/大量输出）停止记录并截断标记，
// 避免撑爆 DB 报文（MEDIUMTEXT 上限 16MB，8MB 留余量）
const maxCastBytes = 8 << 20

type recordedSession struct {
	inner    *TerminalSession
	mu       sync.Mutex
	start    time.Time
	buf      strings.Builder // asciicast v2 全文（内存）
	bytes    int64           // 已写总字节数（size_bytes 展示用）
	overflow bool            // 超 maxCastBytes 后停止记录
	sizeW    int
	sizeH    int
	pendIn   []byte // 跨块被截断的多字节字符尾部（pty 流在任意字节处切块）
	pendOut  []byte
	onFlush  func(content string, sizeBytes int64, duration int, overflow bool) // 周期/最终落库回调
}

// newRecordedSession onFlush 由调用方注入（ws 层不依赖 DB）：
// 周期落库与最终收尾都通过它写库；nil = 不落库（仅内存，外部自行读取 snapshot）
func newRecordedSession(inner *TerminalSession, onFlush func(content string, sizeBytes int64, duration int, overflow bool)) *recordedSession {
	r := &recordedSession{inner: inner, start: time.Now(), sizeW: 80, sizeH: 24, onFlush: onFlush}
	header, _ := json.Marshal(map[string]any{
		"version":   2,
		"width":     80,
		"height":    24,
		"timestamp": time.Now().Unix(),
		"env":       map[string]string{"TERM": "xterm-256color", "SHELL": "/bin/sh"},
	})
	r.buf.Write(append(header, '\n'))
	return r
}

// writeEvent 写一行事件（调用方需持有 r.mu）
func (r *recordedSession) writeEvent(typ, data string) {
	if r.overflow {
		return
	}
	line, _ := json.Marshal([]any{time.Since(r.start).Seconds(), typ, data})
	line = append(line, '\n')
	if r.bytes+int64(len(line)) > maxCastBytes {
		// 超限截断：补截断标记事件后停止记录（回放可见提示；size_bytes 冻结在截断点）
		mark, _ := json.Marshal([]any{time.Since(r.start).Seconds(), "o", "\r\n\x1b[33m[录像达到大小上限，后续内容未记录]\x1b[0m\r\n"})
		r.buf.Write(append(mark, '\n'))
		r.overflow = true
		return
	}
	r.buf.Write(line)
	r.bytes += int64(len(line))
}

func (r *recordedSession) event(typ string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// pty 流式输出的块边界可能落在多字节字符（中文）中间：残缺字节直接
	// string() 进 JSON 会被替换成 U+FFFD（回放/AI 摘要永久乱码）。
	// 尾部不完整序列先扣留，下一块拼齐后一并落盘
	pend := &r.pendOut
	if typ == "i" {
		pend = &r.pendIn
	}
	buf := make([]byte, 0, len(*pend)+len(data))
	buf = append(buf, *pend...)
	buf = append(buf, data...)
	*pend = (*pend)[:0]
	if n := utf8Holdback(buf); n > 0 {
		*pend = append(*pend, buf[len(buf)-n:]...)
		buf = buf[:len(buf)-n]
	}
	if len(buf) > 0 {
		r.writeEvent(typ, string(buf))
	}
}

// Read/Write/Next 实现 io.ReadWriter + TerminalSizeQueue：透传给内层并在透传时录制事件
func (r *recordedSession) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	if n > 0 {
		r.event("i", p[:n])
	}
	return n, err
}

func (r *recordedSession) Write(p []byte) (int, error) {
	n, err := r.inner.Write(p)
	if n > 0 {
		r.event("o", p[:n])
	}
	return n, err
}

func (r *recordedSession) Next() *remotecommand.TerminalSize {
	sz := r.inner.Next()
	if sz != nil {
		r.mu.Lock()
		r.sizeW, r.sizeH = int(sz.Width), int(sz.Height)
		r.writeEventResize()
		r.mu.Unlock()
	}
	return sz
}

// utf8Holdback 块尾被截断的多字节 UTF-8 序列字节数（0 = 块自洽可整体落盘）。
// 从尾部向前找序列起始字节：起始字节 + 不完整的续字节 = 截断；续字节最多 3 个
func utf8Holdback(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-3; i-- {
		c := b[i]
		if c < 0x80 {
			return 0 // ASCII：尾部无残缺
		}
		if c&0xC0 != 0x80 { // 序列起始字节
			if utf8FullRune(b[i:]) {
				return 0 // 序列在块内完整（非法字节也视为完整，原样放行）
			}
			return len(b) - i
		}
	}
	// 尾部续字节 ≥4 个：非法 UTF-8 流（正常流不会出现），不扣留避免积压
	return 0
}

// utf8FullRune 提取自 unicode/utf8（避免与 strings 依赖混杂的间接引用）
func utf8FullRune(b []byte) bool {
	// 与 unicode/utf8.FullRune 等价的简化判断：起始字节确定长度，校验续字节
	n := len(b)
	if n == 0 {
		return false
	}
	c := b[0]
	var size int
	switch {
	case c < 0x80:
		return true
	case c&0xE0 == 0xC0:
		size = 2
	case c&0xF0 == 0xE0:
		size = 3
	case c&0xF8 == 0xF0:
		size = 4
	default:
		return true // 非法起始字节：按完整处理原样放行（与 utf8.FullRune 一致）
	}
	if n < size {
		return false
	}
	for _, cb := range b[1:size] {
		if cb&0xC0 != 0x80 {
			return true // 非法续字节：按完整处理
		}
	}
	return true
}

// SnapshotContent 当前录制内容（测试与读取场景用）
func (r *recordedSession) SnapshotContent() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

// Snapshot 当前录制内容快照（周期落库/收尾用）
func (r *recordedSession) Snapshot() (content string, sizeBytes int64, duration int, overflow bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String(), r.bytes, int(time.Since(r.start).Seconds()), r.overflow
}

// FlushPush 立即触发一次落库回调（外部周期计时器调用）
func (r *recordedSession) FlushPush() {
	if r.onFlush == nil {
		return
	}
	content, size, dur, ovf := r.Snapshot()
	r.onFlush(content, size, dur, ovf)
}

// Close 收尾：扣留的残缺尾部原样写入（与旧文件实现一致），补 resize 钉住时间线。
// 注意：与旧实现不同，这里不再删任何文件（无文件）；最终落库由调用方在 Close 后
// 调 FlushPush/直接读 Snapshot 完成
func (r *recordedSession) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pendIn) > 0 {
		r.writeEvent("i", string(r.pendIn))
		r.pendIn = nil
	}
	if len(r.pendOut) > 0 {
		r.writeEvent("o", string(r.pendOut))
		r.pendOut = nil
	}
	// 收尾补一个同尺寸 resize 事件，把时间线钉到会话真实墙钟时长：
	// 播放器总时长取 cast 最后一个事件的时间戳，末尾静默挂机若不补事件，
	// 回放进度条时长会明显短于录像列表里记录的会话时长
	if !r.overflow {
		r.writeEventResize()
	}
}

// writeEventResize 收尾 resize 事件（带真实尺寸，钉住回放时间线；超限截断后不再补）
func (r *recordedSession) writeEventResize() {
	line, _ := json.Marshal([]any{time.Since(r.start).Seconds(), "r", fmt.Sprintf("%dx%d", r.sizeW, r.sizeH)})
	r.buf.Write(append(line, '\n'))
	r.bytes += int64(len(line))
}
