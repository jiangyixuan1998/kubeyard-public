package api

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"

	"kubeyard/internal/audit"
	"kubeyard/internal/middleware"
	"kubeyard/internal/model"
)

// ---------- 管理页 Excel 导出（用户管理 / 权限组）----------
// 全量快照导出，供安全合规盘点与权限回顾；不含任何敏感数据（密码哈希、令牌不导出）。

func excelTime(v *time.Time) string {
	if v == nil || v.Year() < 2000 { // 存量零值时间当空处理
		return "-"
	}
	return v.Local().Format("2006-01-02 15:04")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// expiryText 到期时间列展示：永久 / 日期 / 已过期
func expiryText(v *time.Time) string {
	if v == nil {
		return "永久"
	}
	if v.Before(time.Now()) {
		return "已过期"
	}
	return excelTime(v)
}

// permState 授权/成员关系状态（导出快照时刻）：永久 / 有效 / 已过期
func permState(v *time.Time, now time.Time) string {
	if v == nil {
		return "永久"
	}
	if v.Before(now) {
		return "已过期"
	}
	return "有效"
}

// exportFile 统一的响应封装：文件名带日期；build 组装工作簿内容
func exportFile(c *gin.Context, base string, build func(*excelize.File) error) {
	f := excelize.NewFile()
	defer f.Close()
	if err := build(f); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "导出失败: " + err.Error()})
		return
	}
	f.DeleteSheet("Sheet1") // NewFile 自带的空默认表，业务 sheet 建好后移除
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.xlsx"`, base))
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	if err := f.Write(c.Writer); err != nil {
		// 响应头已发出，无法再改状态码，仅记录
		log.Printf("[export] 写入响应失败: %v", err)
	}
}

// newSheet 建表并写表头（数据从第 2 行起；默认 Sheet1 在 exportFile 收尾前由调用方删除）
func newSheet(f *excelize.File, name string, headers []string, widths []float64) error {
	if _, err := f.NewSheet(name); err != nil {
		return err
	}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		if err := f.SetCellValue(name, cell, h); err != nil {
			return err
		}
	}
	// SetColWidth 要的是列名（A/B/C…），不是单元格名（A1/B1…）
	for i, w := range widths {
		col := colLetter(i + 1)
		if err := f.SetColWidth(name, col, col, w); err != nil {
			return err
		}
	}
	s, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true},
		Fill: excelize.Fill{Type: "pattern", Color: []string{"#E8EDF5"}, Pattern: 1},
	})
	if err != nil {
		return nil // 样式失败不阻断导出
	}
	return f.SetCellStyle(name, "A1", cellName(len(headers), 1), s)
}

// colLetter 列号转列字母（1→A，27→AA）
func colLetter(n int) string {
	s := ""
	for n > 0 {
		n--
		s = string(rune('A'+n%26)) + s
		n /= 26
	}
	return s
}

func cellName(col, row int) string {
	name, _ := excelize.CoordinatesToCellName(col, row)
	return name
}

// addRows 逐行写入（下标从 0 起，即数据从第 2 行开始）
func addRows(f *excelize.File, sheet string, rows [][]any) error {
	for i, row := range rows {
		for j, v := range row {
			if v == nil {
				continue
			}
			if err := f.SetCellValue(sheet, cellName(j+1, i+2), v); err != nil {
				return err
			}
		}
	}
	return nil
}

// ruleTexts 五元组规则文本化（导出用，与审计/前端展示同语义）
func ruleTexts(cluster, ns, res, verb, actions string) []string {
	return []string{cluster, orStr(ns, "*"), orStr(res, "*"), orStr(verb, "*"), orStr(actions, "*")}
}

// clusterNameOf 集群名展示（导出用）：集群已删除的存量授权回退 #id（与审计 grantDiffDetail 同语义）
func clusterNameOf(names map[uint]string, id uint) string {
	if n := names[id]; n != "" {
		return n
	}
	return fmt.Sprintf("#%d", id)
}

// ---------- 用户导出 ----------

// ExportUsers GET /api/v1/admin/users/export — 全量用户 + 每人权限明细
// （直接授权 + 权限组并集，与「权限」抽屉同口径；含已过期条目并标记，便于盘点存量）
func (h *H) ExportUsers(c *gin.Context) {
	var users []model.User
	h.db.Order("id ASC").Find(&users)
	var cls []model.Cluster
	h.db.Find(&cls)
	names := map[uint]string{}
	for _, cl := range cls {
		names[cl.ID] = cl.Name
	}

	// 全平台授权数据一次拉齐，内存组装（避免逐用户查询）
	var grants []model.UserGrant
	h.db.Find(&grants)
	var mbs []model.UserGroup
	h.db.Find(&mbs)
	var rules []model.GrantGroupRule
	h.db.Find(&rules)
	var groups []model.GrantGroup
	h.db.Find(&groups)

	gname := map[uint]string{}
	for _, g := range groups {
		gname[g.ID] = g.Name
	}
	grantsByUser := map[uint][]model.UserGrant{}
	for _, g := range grants {
		grantsByUser[g.UserID] = append(grantsByUser[g.UserID], g)
	}
	mbsByUser := map[uint][]model.UserGroup{}
	for _, m := range mbs {
		mbsByUser[m.UserID] = append(mbsByUser[m.UserID], m)
	}
	rulesByGroup := map[uint][]model.GrantGroupRule{}
	for _, r := range rules {
		rulesByGroup[r.GroupID] = append(rulesByGroup[r.GroupID], r)
	}

	frozen := time.Now() // 过期判定统一取快照时刻，保证单次导出口径一致

	exportFile(c, fmt.Sprintf("kubeyard-users-%s", time.Now().Format("20060102")), func(f *excelize.File) error {
		if err := newSheet(f, "用户列表",
			[]string{"ID", "用户名", "姓名", "邮箱", "团队", "账号类型", "来源", "平台角色", "状态", "账号有效期", "最近登录", "创建时间"},
			[]float64{6, 14, 14, 24, 16, 18, 8, 13, 8, 18, 18, 18}); err != nil {
			return err
		}
		userRows := make([][]any, 0, len(users))
		for _, u := range users {
			at := "个人（SSO/LDAP 登录）"
			if u.AccountType == "service" {
				at = "服务（本地密码）"
			}
			userRows = append(userRows, []any{
				u.ID, u.Username, orDash(u.DisplayName), orDash(u.Email), orDash(u.Team), at, u.Source, u.Role,
				statusLabel(u.Status), expiryText(u.ExpiresAt), excelTime(u.LastLoginAt), excelTime(&u.CreatedAt),
			})
		}
		if err := addRows(f, "用户列表", userRows); err != nil {
			return err
		}

		if err := newSheet(f, "权限明细",
			[]string{"用户名", "姓名", "集群", "命名空间", "资源", "动词", "可修改字段", "来源", "授权 / 入组时间", "到期时间", "状态"},
			[]float64{14, 14, 12, 18, 20, 16, 16, 18, 18, 18, 10}); err != nil {
			return err
		}
		permRows := [][]any{}
		for _, u := range users {
			if u.Role == model.RoleSuperAdmin {
				permRows = append(permRows, []any{u.Username, orDash(u.DisplayName), "全部集群", "*", "*", "*", "*", "平台管理员", "-", "-", "-"})
				continue
			}
			// 直接授权（含已过期并标记）
			for _, g := range grantsByUser[u.ID] {
				cells := ruleTexts(clusterNameOf(names, g.ClusterID), g.Namespace, g.Resource, g.Verb, g.Actions)
				permRows = append(permRows, []any{u.Username, orDash(u.DisplayName), cells[0], cells[1], cells[2], cells[3], cells[4],
					"直接授权", excelTime(&g.CreatedAt), expiryText(g.ExpiresAt), permState(g.ExpiresAt, frozen)})
			}
			// 权限组规则（到期 = 成员关系时限）
			for _, m := range mbsByUser[u.ID] {
				for _, r := range rulesByGroup[m.GroupID] {
					cells := ruleTexts(clusterNameOf(names, r.ClusterID), r.Namespace, r.Resource, r.Verb, r.Actions)
					src := "权限组"
					if n := gname[m.GroupID]; n != "" {
						src = "权限组·" + n
					}
					permRows = append(permRows, []any{u.Username, orDash(u.DisplayName), cells[0], cells[1], cells[2], cells[3], cells[4],
						src, excelTime(&m.CreatedAt), expiryText(m.ExpiresAt), permState(m.ExpiresAt, frozen)})
				}
			}
		}
		return addRows(f, "权限明细", permRows)
	})
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "users_export", "", "", "", "",
		fmt.Sprintf("导出用户管理数据（%d 个账号）", len(users)), "success"))
}

// ---------- 权限组导出 ----------

// ExportGroups GET /api/v1/admin/groups/export — 权限组 + 成员 + 规则三表
func (h *H) ExportGroups(c *gin.Context) {
	var groups []model.GrantGroup
	h.db.Order("id ASC").Find(&groups)
	var cls []model.Cluster
	h.db.Find(&cls)
	names := map[uint]string{}
	for _, cl := range cls {
		names[cl.ID] = cl.Name
	}
	var users []model.User
	h.db.Find(&users)
	uname := map[uint]string{}
	udisplay := map[uint]string{}
	for _, u := range users {
		uname[u.ID] = u.Username
		udisplay[u.ID] = u.DisplayName
	}

	var rules []model.GrantGroupRule
	h.db.Find(&rules)
	var mbs []model.UserGroup
	h.db.Find(&mbs)
	rulesByGroup := map[uint][]model.GrantGroupRule{}
	for _, r := range rules {
		rulesByGroup[r.GroupID] = append(rulesByGroup[r.GroupID], r)
	}
	mbsByGroup := map[uint][]model.UserGroup{}
	for _, m := range mbs {
		mbsByGroup[m.GroupID] = append(mbsByGroup[m.GroupID], m)
	}

	frozen := time.Now()

	exportFile(c, fmt.Sprintf("kubeyard-groups-%s", time.Now().Format("20060102")), func(f *excelize.File) error {
		if err := newSheet(f, "权限组",
			[]string{"ID", "组名", "描述", "规则数", "成员数", "创建时间"},
			[]float64{6, 24, 32, 8, 8, 18}); err != nil {
			return err
		}
		groupRows := make([][]any, 0, len(groups))
		for _, g := range groups {
			groupRows = append(groupRows, []any{
				g.ID, g.Name, orDash(g.Description), len(rulesByGroup[g.ID]), len(mbsByGroup[g.ID]), excelTime(&g.CreatedAt),
			})
		}
		if err := addRows(f, "权限组", groupRows); err != nil {
			return err
		}

		if err := newSheet(f, "组成员",
			[]string{"组名", "用户名", "姓名", "入组时间", "成员到期", "状态"},
			[]float64{24, 14, 14, 18, 18, 10}); err != nil {
			return err
		}
		memberRows := [][]any{}
		for _, g := range groups {
			for _, m := range mbsByGroup[g.ID] {
				memberRows = append(memberRows, []any{
					g.Name, orDash(uname[m.UserID]), orDash(udisplay[m.UserID]),
					excelTime(&m.CreatedAt), expiryText(m.ExpiresAt), permState(m.ExpiresAt, frozen),
				})
			}
		}
		if err := addRows(f, "组成员", memberRows); err != nil {
			return err
		}

		if err := newSheet(f, "组规则",
			[]string{"组名", "集群", "命名空间", "资源", "动词", "可修改字段"},
			[]float64{24, 12, 18, 20, 16, 16}); err != nil {
			return err
		}
		ruleRows := [][]any{}
		for _, g := range groups {
			for _, r := range rulesByGroup[g.ID] {
				cells := ruleTexts(clusterNameOf(names, r.ClusterID), r.Namespace, r.Resource, r.Verb, r.Actions)
				ruleRows = append(ruleRows, []any{g.Name, cells[0], cells[1], cells[2], cells[3], cells[4]})
			}
		}
		return addRows(f, "组规则", ruleRows)
	})
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "groups_export", "", "", "", "",
		fmt.Sprintf("导出权限组数据（%d 个组）", len(groups)), "success"))
}
