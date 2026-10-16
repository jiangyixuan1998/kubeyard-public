package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"kubeyard/internal/middleware"
	"kubeyard/internal/model"
)

// ---------- 终端录像（exec 审计回放）----------

// ListExecRecordings GET /api/v1/admin/exec-recordings?user=&cluster=&namespace=&pod=&page=&page_size=
func (h *H) ListExecRecordings(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	q := h.db.Model(&model.ExecRecording{}).Omit("content") // 列表不加载录像大字段
	if v := c.Query("user"); v != "" {
		q = q.Where("username LIKE ?", "%"+v+"%")
	}
	if v := c.Query("cluster"); v != "" {
		q = q.Where("cluster = ?", v)
	}
	if v := c.Query("namespace"); v != "" {
		q = q.Where("namespace = ?", v)
	}
	if v := c.Query("pod"); v != "" {
		q = q.Where("pod LIKE ?", "%"+v+"%")
	}
	// 风险等级筛选：high/medium/low；none = 仅看未评估。
	// 未评估含两种存储形态：存量行 AutoMigrate 加列为 NULL、新行空串——都要命中
	if v := c.Query("risk"); v != "" {
		if v == "none" {
			q = q.Where("risk_level = 'none'") // 空会话（无可评命令）
		} else if v == "none2" {
			q = q.Where("risk_level = '' OR risk_level IS NULL") // 未评估（评估失败/AI 未启用）
		} else {
			q = q.Where("risk_level = ?", v)
		}
	}
	// 管理员管辖范围过滤：SuperAdmin 全量；集群管理员看整集群；命名空间管理员看授权命名空间
	all, clusterAll, nsOf := middleware.AdminScope(h.db, middleware.Role(c), middleware.UID(c))
	if !all {
		cond, args := middleware.ScopeSQL(clusterAll, nsOf)
		q = q.Where(cond, args...)
	}
	var total int64
	q.Count(&total)
	var items []model.ExecRecording
	q.Order("started_at DESC").Offset((page - 1) * size).Limit(size).Find(&items)
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total})
}

// GetRecordingCast GET /api/v1/admin/exec-recordings/:id/cast — 返回 asciicast v2 文本（终端回放数据）
func (h *H) GetRecordingCast(c *gin.Context) {
	var rec model.ExecRecording
	if err := h.db.First(&rec, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "录像不存在"})
		return
	}
	// 管辖范围校验：仅 SuperAdmin / 对应集群或命名空间管理员可播放
	all, clusterAll, nsOf := middleware.AdminScope(h.db, middleware.Role(c), middleware.UID(c))
	if !all && !middleware.ScopeCovers(rec.Cluster, rec.Namespace, clusterAll, nsOf) {
		c.JSON(http.StatusForbidden, gin.H{"message": "无权查看该录像（不在您管辖的集群/命名空间范围内）"})
		return
	}
	raw, rawErr := h.recordingRaw(&rec)
	if raw == "" {
		c.JSON(http.StatusNotFound, gin.H{"message": rawErr})
		return
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", padCastDuration(raw, rec.Duration))
}

// recordingRaw 读取录像原始 asciicast 文本：优先数据库 Content，回退本地文件（含防路径穿越）。
// 空串 + 错误说明（回放接口与 AI 摘要共用）
func (h *H) recordingRaw(rec *model.ExecRecording) (string, string) {
	if rec.Content != "" {
		return rec.Content, ""
	}
	// 防路径穿越：文件必须位于 data/recordings 之下
	root, err := filepath.Abs(filepath.Join(h.mgr.DataDir(), "recordings"))
	if err != nil {
		return "", err.Error()
	}
	abs, err := filepath.Abs(rec.FilePath)
	if err != nil || !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return "", "非法录像路径"
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", "录像文件不存在（可能已被清理）"
	}
	return string(b), ""
}

// castCommands 从 asciicast v2 里提取实际执行的命令序列。
//
// 输入流（"i"）是第一信源：逐字符缓冲、回车切分、退格删字。但只要出现转义序列
// 或控制字符，输入缓冲就与真实命令不再一致——而这恰是终端最高频的操作：
// ↑ 翻历史执行上一条时输入流里只有 ESC[A、命令全文只出现在 pty 回显输出里；
// Tab 补全的补全文本、方向键移动后编辑的结果同样只在输出流。
// 因此并行跟踪输出流（"o"）的「回显行」：过滤 ANSI 转义后尚未换行的部分即
// prompt + 正在输入命令的回显（回显逐字符实时发生，回车键的 \r\n 回显在 "i" 回车
// 事件之后才到，取值时序天然成立）。回车取命令的优先级：
//  1. 输入缓冲可信（无 ESC/控制字符污染）→ 用输入缓冲（干净、无 prompt）
//  2. 回显行非空 → 剥掉 prompt 前缀用回显（翻历史/补全/编辑的主路径）
//  3. 都不行 → 输入残片兜底（宁可有噪音不可漏命令——审计口径）
// 已知局限：pty 关闭回显的行（密码输入）本就不该进摘要；全屏程序（vim 等）的
// 回显行可能是屏幕碎片噪音，交给系统提示让模型合理忽略。
func castCommands(cast string) []string {
	cmds, _ := castCommandsEx(cast)
	return cmds
}

func castCommandsEx(cast string) ([]string, bool) {
	var cmds []string
	in := []rune{}   // 输入侧命令行缓冲（被污染后保留残片，供回显定位/最后兜底）
	inValid := true  // 输入缓冲是否可信（未受 ESC/控制字符污染）
	echo := []rune{} // 输出侧回显行（prompt + 命令回显），配合 echoCur 支持光标移动/覆盖重绘
	echoCur := 0     // 回显行光标位置（pty 对方向键编辑的回显是 \b 移光标 + 覆盖重绘，不是删除）
	escIn, escOut := 0, 0
	flush := func() {
		cmd := ""
		if inValid {
			cmd = strings.TrimSpace(string(in))
		}
		if cmd == "" {
			if strings.TrimSpace(string(echo)) != "" {
				cmd = stripPrompt(string(echo), strings.TrimSpace(string(in)))
			} else {
				cmd = strings.TrimSpace(string(in)) // 无回显的录像：残片兜底
			}
		}
		if cmd != "" {
			cmds = append(cmds, cmd)
		}
		in, inValid = in[:0], true
		echo, echoCur = echo[:0], 0
		// echo 一并清空：内容已消费。不清单靠「回车的 \r\n 回显」清行——
		// 断连发生在回显到达前时，尾部 flush 会把同一条命令重复计入
	}
	for _, ln := range strings.Split(cast, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		var ev []any
		if json.Unmarshal([]byte(ln), &ev) != nil || len(ev) < 3 {
			continue
		}
		typ, ok := ev[1].(string)
		if !ok {
			continue
		}
		data, ok := ev[2].(string)
		if !ok || (typ != "i" && typ != "o") {
			continue
		}
		// 高危拦截标记（拦截层 warn 经录像层写入）：该命令的回车被吞、并未执行——
		// 从已执行序列移除；警告文本也不是命令回显，整事件跳过回显行处理
		if typ == "o" && strings.Contains(data, blockMark) {
			if bc := blockedCmdOf(data); bc != "" && len(cmds) > 0 && cmds[len(cmds)-1] == bc {
				cmds = cmds[:len(cmds)-1]
			}
			echo, echoCur = echo[:0], 0
			continue
		}
		for _, r := range data {
			if typ == "i" {
				switch {
				case escIn > 0: // 转义序列（方向键/光标控制/补全）中：整段吞掉
					escStep(&escIn, r)
				case r == '\b' || r == 0x7f: // 退格
					if len(in) > 0 {
						in = in[:len(in)-1]
					}
				case r == '\r' || r == '\n': // 回车 → 一条命令
					flush()
				case r == 0x1b: // ESC：行缓冲不可信（标记而非清空，残片留给回显定位）
					escIn, inValid = 1, false
				case r < 0x20: // 其余控制字符（Tab 补全/Ctrl 组合键）→ 行缓冲不可信
					inValid = false
				default:
					in = append(in, r)
				}
				continue
			}
			// 输出流：按光标模型维护回显行
			switch {
			case escOut > 0: // ANSI 转义（prompt 颜色码/清行等）整段吞掉
				escStep(&escOut, r)
			case r == 0x1b: // ANSI 转义开始
				escOut = 1
			case r == '\n': // 行结束：命令输出行不属于回显，新行开始
				echo, echoCur = echo[:0], 0
			case r == '\r': // 回行首：后续覆盖重绘（进度条 / 方向键编辑的插入回显）
				echoCur = 0
			case r == '\b' || r == 0x7f: // 光标左移（pty 对 ← 的回显是 \b，不删除字符）
				if echoCur > 0 {
					echoCur--
				}
			case r == '\t':
				r = ' '
				fallthrough
			case r >= 0x20:
				if echoCur < len(echo) {
					echo[echoCur] = r // 覆盖
				} else {
					echo = append(echo, r)
				}
				echoCur++
				if len(echo) > 512 { // 上限防无换行的大量输出（如 yes）撑爆内存
					echo, echoCur = echo[:0], 0
				}
			}
		}
	}
	// 尾部未回车的输入（会话中途断连的场景）。是否追加了命令 = 是否存在
	// 「正在输入、未回车」的命令——只看最后一个输入事件是否含回车不可靠：
	// 命令执行完后再敲个空格（输入法残留/误触）就会误报未执行
	n := len(cmds)
	flush()
	return cmds, len(cmds) > n
}

// escStep 转义序列状态机推进一步：ESC [ ... 终止字节(0x40-0x7e) / ESC O x /
// ESC ] ...(BEL|ST)。与 ws/exec.go guardedSession 的过滤状态机同构
func escStep(esc *int, r rune) {
	switch *esc {
	case 1:
		if r == '[' || r == 'O' {
			*esc = 2
		} else if r == ']' {
			*esc = 3
		} else {
			*esc = 0
		}
	case 2:
		if r >= 0x40 && r <= 0x7e {
			*esc = 0
		}
	case 3:
		if r == 0x07 || r == 0x1b {
			*esc = 0
		}
	}
}

// promptTailRe 常见 shell 提示符尾部：user@host:cwd$ / [env]# / % / ➜ 等，
// prompt 以 $ # % > ❯ ➜ 之一收尾（后跟空白或行尾）。限长 80：prompt 异常长时
// 宁可不剥（整行交给模型）也不能把命令本身当 prompt 吞掉
var promptTailRe = regexp.MustCompile(`^.{0,80}?[$#%>❯➜](?:\s|$)`)

// stripPrompt 从回显行还原命令：优先按 prompt 尾部正则剥前缀；不匹配时用输入
// 残片在回显中的位置定位（Tab 补全等场景残片常是命令的子串）；都不行就整行
// 交给模型（系统提示已声明噪音容忍）。dirty 为空串表示无输入残片。
func stripPrompt(line, dirty string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	if m := promptTailRe.FindStringIndex(line); m != nil {
		return strings.TrimSpace(line[m[1]:])
	}
	if dirty != "" {
		if i := strings.LastIndex(line, dirty); i > 0 {
			return strings.TrimSpace(line[i:])
		}
	}
	return line
}

// blockMark 高危拦截层 warn 写入输出流的标记前缀（ws/exec.go），后接被拦命令原文
const blockMark = "已拦截高危命令（未执行）: "

// blockedCmdOf 从单个输出事件里取被拦截的命令原文（warn 单次 Write 整行落盘，
// 命令以颜色码复位/换行结尾）
func blockedCmdOf(data string) string {
	i := strings.Index(data, blockMark)
	if i < 0 {
		return ""
	}
	rest := data[i+len(blockMark):]
	if j := strings.IndexAny(rest, "\x1b\r\n"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// castBlocked 从录像输出流提取全部被高危拦截层吞掉的命令。这些命令没有真正
// 执行，摘要必须与已执行命令区分，否则把「尝试 rm -rf 被拦」误报成「执行了 rm -rf」
func castBlocked(cast string) []string {
	var blocked []string
	for _, ln := range strings.Split(cast, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || !strings.Contains(ln, "已拦截高危命令") {
			continue
		}
		var ev []any
		if json.Unmarshal([]byte(ln), &ev) != nil || len(ev) < 3 {
			continue
		}
		if typ, _ := ev[1].(string); typ != "o" {
			continue
		}
		if data, _ := ev[2].(string); data != "" {
			if s := blockedCmdOf(data); s != "" {
				blocked = append(blocked, s)
			}
		}
	}
	return blocked
}

// padCastDuration 旧录像录制结束时未补收尾事件，末尾静默期丢失导致回放总时长
// 短于列表时长；若 cast 最后一个事件的时间戳明显早于记录的会话时长，
// 追加一行同尺寸 resize 事件把时间线补齐（新录像已自带收尾事件，此函数原样返回）
func padCastDuration(raw string, duration int) []byte {
	if raw == "" || duration <= 0 || !strings.HasSuffix(raw, "\n") {
		return []byte(raw)
	}
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	lastT := -1.0
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		var ev []any
		if json.Unmarshal([]byte(lines[i]), &ev) == nil && len(ev) > 0 {
			if t, ok := ev[0].(float64); ok {
				lastT = t
			}
		}
		break
	}
	if lastT >= float64(duration)-1 {
		return []byte(raw)
	}
	return []byte(raw + fmt.Sprintf("[%.3f, \"r\", \"80x24\"]\n", float64(duration)))
}
