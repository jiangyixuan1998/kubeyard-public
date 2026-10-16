package api

import (
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"kubeyard/internal/authz"
	"kubeyard/internal/model"
)

// ListAccessProfiles GET /api/v1/admin/authz/profiles
func (h *H) ListAccessProfiles(c *gin.Context) {
	var items []model.AccessProfile
	q := h.db.Model(&model.AccessProfile{})
	if kw := c.Query("q"); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR resources LIKE ? OR verbs LIKE ? OR actions LIKE ?", like, like, like, like)
	}
	if err := q.Order("id ASC").Find(&items).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

// ListAccessBindings GET /api/v1/admin/authz/bindings
func (h *H) ListAccessBindings(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	q := h.db.Model(&model.AccessBinding{})
	if v := c.Query("profile_id"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			q = q.Where("profile_id = ?", id)
		}
	}
	switch subject := c.Query("subject"); subject {
	case "user":
		q = q.Where("user_id > 0")
	case "group":
		q = q.Where("group_id > 0")
	}
	if v := c.Query("subject_id"); v != "" {
		if id, err := strconv.ParseUint(v, 10, 64); err == nil {
			q = q.Where("(user_id = ? OR group_id = ?)", id, id)
		}
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取失败"})
		return
	}
	var rows []model.AccessBinding
	if err := q.Order("id ASC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取失败"})
		return
	}

	var profiles []model.AccessProfile
	h.db.Find(&profiles)
	profileName := map[uint]string{}
	for _, p := range profiles {
		profileName[p.ID] = p.Name
	}
	var clusters []model.Cluster
	h.db.Find(&clusters)
	clusterName := map[uint]string{}
	for _, cl := range clusters {
		clusterName[cl.ID] = cl.Name
	}
	var users []model.User
	h.db.Find(&users)
	userName := map[uint]string{}
	for _, u := range users {
		userName[u.ID] = u.Username
	}
	var groups []model.GrantGroup
	h.db.Find(&groups)
	groupName := map[uint]string{}
	for _, g := range groups {
		groupName[g.ID] = g.Name
	}

	type bindingView struct {
		model.AccessBinding
		ProfileName string `json:"profile_name"`
		Cluster     string `json:"cluster"`
		Subject     string `json:"subject"`
		SubjectName string `json:"subject_name"`
	}
	items := make([]bindingView, 0, len(rows))
	for _, row := range rows {
		view := bindingView{
			AccessBinding: row,
			ProfileName:   profileName[row.ProfileID],
			Cluster:       clusterName[row.ClusterID],
			Subject:       "group",
			SubjectName:   groupName[row.GroupID],
		}
		if row.UserID != 0 {
			view.Subject = "user"
			view.SubjectName = userName[row.UserID]
		}
		items = append(items, view)
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total})
}

// GetAuthzStats GET /api/v1/admin/authz/stats
func (h *H) GetAuthzStats(c *gin.Context) {
	stats := authz.GetStats()
	var profileCount, bindingCount int64
	h.db.Model(&model.AccessProfile{}).Count(&profileCount)
	h.db.Model(&model.AccessBinding{}).Count(&bindingCount)
	c.JSON(http.StatusOK, gin.H{
		"profiles": profileCount,
		"bindings": bindingCount,
		"stats":    stats,
	})
}

func syncAuthz(db *gorm.DB) {
	if err := authz.SyncFromLegacy(db); err != nil {
		log.Printf("[WARN] 权限引擎影子表同步失败: %v", err)
	}
}
