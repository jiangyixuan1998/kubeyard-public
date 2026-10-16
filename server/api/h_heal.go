package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kubeyard/internal/model"

	"gorm.io/gorm"
)

// ---------- 录像僵尸行自愈（服务启动时执行一次） ----------
//
// 背景：服务重启/进程被杀时正在录制的终端会话，收尾代码（内容入库 + ended_at 更新）从未执行——
// 库里留下 ended_at NULL 的僵尸行。三类处理：
//  1. content 非空：内容已在库（早期行为或已回填），只补 ended_at
//  2. content 空、本地 cast 文件在：读文件入库 + 补 ended_at（内容找回）
//  3. content 空、文件也丢（容器路径迁移/已清理）：内容彻底丢失——标 lost 等级，
//     前端显示「内容丢失」；AI 评估/原始记录/回放对其明确报错而非笼统 404

// riskLostLevel 内容丢失等级（区别于未评估/空会话）
const riskLostLevel = "lost"

// HealRecordings 启动自愈：幂等，只处理 ended_at IS NULL 的行。
// 0.71 后录制改为内存+周期落库（不再写本地文件），此函数主要处理历史遗留的文件版僵尸行；
// 新实现下 Pod 重启最多丢最后一个周期（30s），周期落库已写入 DB，正常不再产生僵尸行
func HealRecordings(db *gorm.DB, dataDir string) {
	// 活跃租约阈值：录像行的 content 由所属副本每 30s 刷新一次，但表上没有「最后刷新时间」列——
	// 用行元数据不行，改判据：多副本部署时其他副本正在录的会话也会 ended_at NULL。
	// 解法：HealRecordings 只处理 updated_at 早于 90s（>3 个周期未刷新）的行——
	// 活跃会话的周期写库每 30s 必然触碰行（GORM Updates 会更新 updated_at），90s 无触碰 = 失活
	var rows []model.ExecRecording
	if err := db.Where("ended_at IS NULL AND updated_at < ?", time.Now().Add(-90*time.Second)).Find(&rows).Error; err != nil {
		fmt.Println("[heal] 查询僵尸录像失败:", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	now := time.Now()
	// 条件更新守卫：查询与更新之间录制副本可能成功刷新（updated_at 变新）——
	// 所有更新都必须再次带上 ended_at IS NULL AND updated_at < cutoff 并检查 RowsAffected，
	// 已被刷新（其他副本还在录）的行跳过，杜绝把活跃录像标结束/标丢失/覆盖内容
	reclaimGuard := "ended_at IS NULL AND updated_at < ?"
	cutoff := now.Add(-90 * time.Second)
	recovered, marked, closed, raced := 0, 0, 0, 0
	for _, r := range rows {
		if r.Content != "" {
			// 1. 内容已在库：补结束时间（duration 已是收尾前写的近似值则保留，没有按 0 处理不猜）
			ended := now
			if r.Duration > 0 {
				if t := r.StartedAt.Add(time.Duration(r.Duration) * time.Second); t.After(r.StartedAt) && t.Before(now) {
					ended = t
				}
			}
			res := db.Model(&r).Where(reclaimGuard, cutoff).Update("ended_at", ended)
			if res.Error != nil {
				fmt.Println("[heal] 补结束时间写入失败:", res.Error)
			} else if res.RowsAffected > 0 {
				closed++
			} else {
				raced++ // 已被录制副本刷新：跳过
			}
			continue
		}
		// 2/3. content 空：尝试读本地文件（超 8MB 上限的不入库，保留文件走 FilePath 回放）
		if raw, absPath, ok := readCastSafe(dataDir, r.FilePath); ok && len(raw) > 0 {
			if len(raw) <= 8<<20 {
				// 先条件更新入库——成功才删本地文件：DB 写失败时文件是唯一副本，删了不可恢复
				res := db.Model(&r).Where(reclaimGuard, cutoff).Updates(map[string]any{"content": raw, "ended_at": now, "file_path": ""})
				if res.Error != nil {
					fmt.Println("[heal] 录像内容找回写入失败（保留本地文件）:", res.Error)
					continue
				}
				if res.RowsAffected == 0 {
					raced++ // 已被录制副本刷新：跳过，文件保留
					continue
				}
				_ = os.Remove(absPath) // 入库成功且确认归属后才删文件
				recovered++
			} else {
				res := db.Model(&r).Where(reclaimGuard, cutoff).Update("ended_at", now)
				if res.Error != nil {
					fmt.Println("[heal] 超限录像补结束时间写入失败:", res.Error)
				} else if res.RowsAffected > 0 {
					recovered++
				} else {
					raced++ // 已被录制副本刷新：跳过
				}
			}
			continue
		}
		// 3. 文件也丢：标 lost（risk_level 覆盖；why 说明原因，不再出现在「未评估」里诱导重评）
		res := db.Model(&r).Where(reclaimGuard, cutoff).Updates(map[string]any{
			"ended_at":   now,
			"risk_level": riskLostLevel,
			"risk_why":   "内容丢失（服务重启时录像中断且本地文件已不存在），无法评估",
		})
		if res.Error != nil {
			fmt.Println("[heal] 标记内容丢失写入失败:", res.Error)
		} else if res.RowsAffected > 0 {
			marked++
		} else {
			raced++ // 已被录制副本刷新：跳过
		}
	}
	fmt.Printf("[heal] 录像自愈完成：%d 条补结束时间、%d 条从本地文件找回内容、%d 条标记内容丢失、%d 条已被录制副本刷新跳过（共 %d 条候选）\n",
		closed, recovered, marked, raced, len(rows))
}

// readCastSafe 安全读取 cast 文件：路径必须位于 data/recordings 之下（防穿越），文件存在且非空。
// 返回 (内容, 已校验的绝对路径, 是否成功)——绝对路径供调用方删除文件
func readCastSafe(dataDir, fp string) (string, string, bool) {
	if fp == "" {
		return "", "", false
	}
	root, err := filepath.Abs(filepath.Join(dataDir, "recordings"))
	if err != nil {
		return "", "", false
	}
	// 容器部署写的绝对路径（/app/...）在本机不存在——filepath.Abs 对不存在路径也能算，
	// 前缀校验自然挡掉；Windows 反斜杠存量路径统一转换
	fp = strings.ReplaceAll(fp, "\\", string(os.PathSeparator))
	abs, err := filepath.Abs(fp)
	if err != nil || !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return "", "", false
	}
	b, err := os.ReadFile(abs)
	if err != nil || len(b) == 0 {
		return "", "", false
	}
	return string(b), abs, true
}
