package api

import (
	"strings"
	"testing"
)

// ---------- 脱敏（安全边界：漏一条就是敏感信息进模型） ----------

// TestUtf8Trunc 按字节上限截断必须切在 UTF-8 字符边界：中文/emoji 被切在中间
// 会成为非法字节，进 JSON（模型请求/接口响应/入库）即成 U+FFFD 乱码。
// n ≥ len 时也要修正尾部自身残缺的序列（LimitReader 按字节读流的场合）
func TestUtf8Trunc(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"短串原样", "abc", 100, "abc"},
		{"空串", "", 10, ""},
		{"n 为 0", "abc", 0, ""},
		{"n 为负", "abc", -1, ""},
		{"ASCII 精确截断", "abcdef", 3, "abc"},
		{"切点在中文中间（缺2字节）", "abc测试", 4, "abc"},
		{"切点在中文中间（缺1字节）", "abc测试", 5, "abc"},
		{"切点恰在中文边界", "abc测试", 6, "abc测"},
		{"中文长串", "一二三四五", 6, "一二"}, // 6 = 3+3
		{"emoji 切在中间", "a\U0001F600b", 3, "a"}, // emoji 4 字节，切点 3 落在中间
		{"尾部自身残缺（LimitReader 场景）", "abc" + string("测"[:2]), 100, "abc"},
		{"尾部残缺连续回退", "ab" + string("测"[:1]), 100, "ab"},
		{"真实 U+FFFD 字符保留", "ab�cd", 100, "ab�cd"},
	}
	for _, c := range cases {
		if got := utf8Trunc(c.in, c.n); got != c.want {
			t.Errorf("%s: utf8Trunc(%q, %d) = %q, 期望 %q", c.name, c.in, c.n, got, c.want)
		}
	}
}

// TestMaskSecrets 表驱动回归：覆盖两类形态——行首键值（YAML/事件消息）与内联形态
// （日志里的 Authorization 头 / Bearer / sk- 密钥 / k=v 参数），并确认正常内容不被误伤。
func TestMaskSecrets(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		// 行首键值形态（YAML / describe 输出）
		{"YAML password", "  password: myP@ssw0rd", "  password: ******"},
		{"YAML api_key 带引号", `  api_key: "abc123"`, `  api_key: ******`},
		{"Secret data 块 base64", "  DB_PASSWORD: c3VwZXI=", "  DB_PASSWORD: ******"},
		{"tokenType 不含关键词", "  token_type: Bearer", "  token_type: Bearer"},
		// 内联形态（日志行）
		{"Authorization 头", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.sig==", "Authorization: ******"},
		{"裸 Bearer", "auth ok Bearer eyJhbGciOiJIUzI1NiJ9", "auth ok Bearer ******"},
		{"sk 密钥", "using sk-abcdef0123456789abcdef now", "using sk-****** now"},
		{"k=v 参数", "connected token=abc123&user=root", "connected token=******&user=root"},
		{"JDBC 连接串", "jdbc:mysql://db:3306/app?password=p%40ss&useSSL=false", "jdbc:mysql://db:3306/app?password=******&useSSL=false"},
		// 正常内容不误伤
		{"镜像地址", "image: nginx:1.27", "image: nginx:1.27"},
		{"副本数", "replicas: 3", "replicas: 3"},
		{"事件消息", "Back-off restarting failed container", "Back-off restarting failed container"},
		{"任务名含 sk 前缀不误伤", "task-scheduler-verylongword", "task-scheduler-verylongword"},
		{"幂等（已脱敏内容再过一遍不变）", "password: ******", "password: ******"},
	}
	for _, c := range cases {
		if got := maskSecrets(c.in); got != c.want {
			t.Errorf("%s:\n  in  = %q\n  got = %q\n  want= %q", c.name, c.in, got, c.want)
		}
	}
}

// TestMaskSecretsMultiLine 组合场景：一段模拟日志（含多种泄密点 + 噪音行）整体过一遍
func TestMaskSecretsMultiLine(t *testing.T) {
	in := "starting app v1.2\n" +
		"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig\n" +
		"db connect jdbc:mysql://db:3306/app?password=s3cret&useSSL=false\n" +
		"ready on :8080\n"
	out := maskSecrets(in)
	if strings.Contains(out, "eyJhbGci") || strings.Contains(out, "s3cret") {
		t.Fatalf("敏感值未被脱敏:\n%s", out)
	}
	if !strings.Contains(out, "starting app v1.2") || !strings.Contains(out, "ready on :8080") {
		t.Fatalf("正常内容被误伤:\n%s", out)
	}
}

// ---------- 终端录像命令提取（AI 审计摘要的输入，噪音清理错会误导审计结论） ----------

// TestCastCommands 从 asciicast v2 提取命令序列：回车切分、退格修正、尾部未回车行。
// 输入缓冲可信时优先用输入侧（干净、无 prompt），echo 事件不干扰
func TestCastCommands(t *testing.T) {
	cast := `{"version":2,"width":80,"height":24}` + "\n" +
		`[0.5, "o", "$ "]` + "\n" +
		`[0.6, "i", "l"]` + "\n" +
		`[0.7, "i", "s"]` + "\n" +
		`[0.8, "i", " -l"]` + "\n" +
		`[0.9, "i", "\r"]` + "\n" +
		`[1.0, "o", "file1\r\nfile2\r\n"]` + "\n" +
		`[1.5, "i", "e"]` + "\n" +
		`[1.6, "i", "ch"]` + "\n" +
		`[1.7, "i", "o he"]` + "\n" + // echo he
		`[1.8, "i", "llo"]` + "\n" +
		`[1.9, "i", "\r"]` + "\n" +
		`[2.0, "i", "ca"]` + "\n" +
		`[2.1, "i", "\u007f"]` + "\n" + // 退格删掉 a，再输入 at
		`[2.2, "i", "at /etc/pa"]` + "\n" +
		`[2.3, "i", "sswd"]` + "\n"
	cmds := castCommands(cast)
	want := []string{"ls -l", "echo hello", "cat /etc/passwd"}
	if len(cmds) != len(want) {
		t.Fatalf("期望 %d 条命令 %v, 实际 %d 条 %v", len(want), want, len(cmds), cmds)
	}
	for i := range want {
		if cmds[i] != want[i] {
			t.Errorf("命令[%d] = %q, 期望 %q", i, cmds[i], want[i])
		}
	}
}

// TestCastCommandsHistoryRecall 真实翻历史场景（此前整条命令丢失的主因）：
// ↑ 键输入流里只有 ESC[A，命令全文在 pty 回显输出里（含 OSC 标题与 \r+ESC[K 重绘噪音）
func TestCastCommandsHistoryRecall(t *testing.T) {
	cast := `[0.1, "o", "\u001b]0;root@host\u0007root@host:/# "]` + "\n" +
		`[0.2, "i", "\u001b[A"]` + "\n" + // ↑ 翻历史
		`[0.3, "o", "\r\u001b[Kdocker restart api"]` + "\n" + // readline 重绘：清行 + 命令全文
		`[0.4, "i", "\r"]` + "\n" + // 回车执行
		`[0.5, "o", "api\r\nroot@host:/# "]` + "\n"
	cmds := castCommands(cast)
	if len(cmds) != 1 || cmds[0] != "docker restart api" {
		t.Fatalf("期望 [docker restart api]，实际 %v", cmds)
	}
}

// TestCastCommandsTabCompletion Tab 补全场景：输入缓冲在 Tab 处作废（"kubec"+"get pods"
// 拼不出真命令），命令全文靠回显行（打字回显 + 补全回显）剥 prompt 还原
func TestCastCommandsTabCompletion(t *testing.T) {
	cast := `[0.1, "o", "root@host:/# "]` + "\n" +
		`[0.2, "o", "kubec"]` + "\n" + // 打字回显
		`[0.3, "i", "kubec"]` + "\n" +
		`[0.4, "i", "\t"]` + "\n" + // Tab 补全
		`[0.5, "o", "tl get pods"]` + "\n" + // pty 回显补全结果
		`[0.6, "i", "\r"]` + "\n"
	cmds := castCommands(cast)
	if len(cmds) != 1 || cmds[0] != "kubectl get pods" {
		t.Fatalf("期望 [kubectl get pods]，实际 %v", cmds)
	}
}

// TestCastCommandsNoEchoFallback 无回显的录像（异常/早期数据）：退回输入残片兜底——
// 宁可有噪音不可漏命令（审计口径）
func TestCastCommandsNoEchoFallback(t *testing.T) {
	cast := `[0.1, "i", "ca"]` + "\n" +
		`[0.2, "i", "\u001b[A"]` + "\n" + // ↑（无输出事件）
		`[0.3, "i", "ls\r"]` + "\n"
	cmds := castCommands(cast)
	if len(cmds) != 1 || cmds[0] != "cals" {
		t.Fatalf("期望残片兜底 [cals]，实际 %v", cmds)
	}
}

// TestCastCommandsTrailingLine 未按回车的尾部输入也计入（会话中途断连的场景）
func TestCastCommandsTrailingLine(t *testing.T) {
	cmds := castCommands(`[0.1, "i", "rm -rf "]` + "\n")
	if len(cmds) != 1 || cmds[0] != "rm -rf" {
		t.Fatalf("期望 [rm -rf]（尾部未回车行），实际 %v", cmds)
	}
}

// TestCastCommandsArrowEdit 方向键编辑场景（真实录像 #69 提取出的缺陷）：用户敲
// "echo2" 后按 ←（pty 回显 \b 光标左移），再插入空格（回显 " 2\b" 覆盖重绘），
// 实际执行 "echo 2"。回显行必须按光标模型跟踪（\b 是移光标不是删字符），栈式
// 行缓冲会把命令还原成残缺的 "echo"
func TestCastCommandsArrowEdit(t *testing.T) {
	cast := `[0.1, "o", "root@host:/# "]` + "\n" +
		`[0.2, "i", "echo2"]` + "\n" +
		`[0.3, "o", "echo2"]` + "\n" +
		`[0.4, "i", "\u001b[D"]` + "\n" + // ← 左移一格（光标到 2 前）
		`[0.5, "o", "\b"]` + "\n" + // pty 回显：光标左移（不删除）
		`[0.6, "i", " "]` + "\n" + // 插入空格
		`[0.7, "o", " 2\b"]` + "\n" + // 插入模式的覆盖重绘
		`[0.8, "i", "\r"]` + "\n"
	cmds := castCommands(cast)
	if len(cmds) != 1 || cmds[0] != "echo 2" {
		t.Fatalf("期望 [echo 2]，实际 %v", cmds)
	}
}

// TestCastTailUnentered 「未回车」判定：只看最后一个输入事件是否含回车不可靠——
// 命令正常执行后再敲个空格（输入法残留/误触）就误报（真实案例：末条 echo 已执行
// 却被摘要标注未执行）。正确判据 = 尾部 flush 是否实际追加了一条命令
func TestCastTailUnentered(t *testing.T) {
	// 正常执行后仅多敲了空格：不误标
	spaces := `[0.1, "o", "/app # "]` + "\n" +
		`[0.2, "i", "echo hi"]` + "\n" +
		`[0.3, "i", "\r"]` + "\n" +
		`[0.4, "o", "echo hi\r\nhi\r\n/app # "]` + "\n" +
		`[0.5, "i", "  "]` + "\n" // 断连前只敲了空格
	cmds, tail := castCommandsEx(spaces)
	if len(cmds) != 1 || cmds[0] != "echo hi" || tail {
		t.Fatalf("期望 [echo hi] 且不误标未回车，实际 %v tail=%v", cmds, tail)
	}
	// 真正未回车：断连瞬间正在输入
	typing := `[0.1, "i", "rm -rf /"]` + "\n" +
		`[0.2, "i", "\u001b"]` + "\n" + // ESC 后会话结束
		`[0.3, "r", "80x24"]` + "\n"
	cmds, tail = castCommandsEx(typing)
	if len(cmds) != 1 || cmds[0] != "rm -rf /" || !tail {
		t.Fatalf("期望 [rm -rf /] 且标记未回车，实际 %v tail=%v", cmds, tail)
	}
	// 全部正常回车收尾
	cmds, tail = castCommandsEx(`[0.1, "i", "ls\r"]` + "\n")
	if len(cmds) != 1 || cmds[0] != "ls" || tail {
		t.Fatalf("期望 [ls] 且不误标，实际 %v tail=%v", cmds, tail)
	}
}

// TestCastBlocked 高危拦截层吞掉的命令从输出流标记行提取：摘要须与已执行命令区分
func TestCastBlocked(t *testing.T) {
	cast := `[0.1, "o", "root@host:/# "]` + "\n" +
		`[0.2, "i", "rm -rf /\r"]` + "\n" +
		`[0.3, "o", "\r\n\u001b[1;31m⚠ 已拦截高危命令（未执行）: rm -rf /\u001b[0m\r\n"]` + "\n" +
		`[0.4, "o", "（该命令已被平台策略拦截；如需重新输入请按 Ctrl-C 丢弃本行）\r\n"]` + "\n"
	blocked := castBlocked(cast)
	if len(blocked) != 1 || blocked[0] != "rm -rf /" {
		t.Fatalf("期望 [rm -rf /]，实际 %v", blocked)
	}
	// 被拦命令不应同时出现在已执行序列里
	cmds := castCommands(cast)
	if len(cmds) != 0 {
		t.Fatalf("被拦截的命令不应计入已执行序列，实际 %v", cmds)
	}
}
