package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"kubeyard/internal/audit"
	"kubeyard/internal/cluster"
	"kubeyard/internal/cryptox"
	"kubeyard/internal/middleware"
	"kubeyard/internal/model"
)

var clusterNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,30}[a-z0-9])?$`)

// AdminListClusters GET /api/v1/admin/clusters — 管理视角全量（不含凭据）
func (h *H) AdminListClusters(c *gin.Context) {
	var clusters []model.Cluster
	h.db.Order("name ASC").Find(&clusters)
	c.JSON(http.StatusOK, clusters)
}

// ListClusters GET /api/v1/clusters — 按授权过滤
func (h *H) ListClusters(c *gin.Context) {
	c.JSON(http.StatusOK, middleware.AllowedClusters(h.db, middleware.Role(c), middleware.UID(c)))
}

type clusterReq struct {
	Name        string `json:"name"` // 仅 CreateCluster 必填（各接口单独校验）
	Description string `json:"description"`
	Kubeconfig  string `json:"kubeconfig"`
	ContextName string `json:"context_name"`
	APIServer   string `json:"api_server"`
	AuthType    string `json:"auth_type"`
}

// KubeconfigContexts POST /api/v1/admin/kubeconfig/contexts — 上传 kubeconfig 解析 context 列表
func (h *H) KubeconfigContexts(c *gin.Context) {
	var req clusterReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Kubeconfig == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请提供 kubeconfig 内容"})
		return
	}
	contexts, current, err := cluster.ListContexts([]byte(req.Kubeconfig))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"contexts": contexts, "current_context": current})
}

// KubeconfigTest POST /api/v1/admin/kubeconfig/test — 纳管前测试（不落库）
func (h *H) KubeconfigTest(c *gin.Context) {
	var req clusterReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Kubeconfig == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请提供 kubeconfig 内容"})
		return
	}
	res, err := cluster.TestKubeconfig([]byte(req.Kubeconfig), req.ContextName)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

// CreateCluster POST /api/v1/admin/clusters — 测试通过 → 加密入库 → 热加载
func (h *H) CreateCluster(c *gin.Context) {
	var req clusterReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请填写集群名称"})
		return
	}
	if !clusterNameRe.MatchString(req.Name) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "集群名称仅允许小写字母/数字/中划线（2-32位）"})
		return
	}
	var exist model.Cluster
	if err := h.db.Where("name = ?", req.Name).First(&exist).Error; err == nil {
		c.JSON(http.StatusConflict, gin.H{"message": "同名集群已存在"})
		return
	}
	res, err := cluster.TestKubeconfig([]byte(req.Kubeconfig), req.ContextName)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if !res.AllOK {
		c.JSON(http.StatusBadRequest, gin.H{"message": "kubeconfig 测试未通过，请查看步骤详情", "result": res})
		return
	}

	cipher, err := cryptox.Encrypt([]byte(req.Kubeconfig))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "凭据加密失败"})
		return
	}
	sum := sha256.Sum256([]byte(req.Kubeconfig))
	cl := model.Cluster{
		Name:        req.Name,
		Description: req.Description,
		APIServers:  firstOr(res.Servers, ""),
		Version:     res.Version,
		Status:      "Healthy",
	}
	if err := h.db.Create(&cl).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "保存集群失败: " + err.Error()})
		return
	}
	cred := model.ClusterCredential{
		ClusterID:   cl.ID,
		Type:        "kubeconfig",
		ContextName: res.CurrentContext,
		AuthType:    res.AuthType,
		CipherText:  cipher,
		Fingerprint: hex.EncodeToString(sum[:8]),
	}
	if err := h.db.Create(&cred).Error; err != nil {
		h.db.Delete(&cl)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "保存凭据失败"})
		return
	}
	if _, err := h.mgr.Add(cl.Name, []byte(req.Kubeconfig), res.CurrentContext); err != nil {
		h.db.Delete(&cl)
		h.db.Delete(&cred)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "集群客户端初始化失败"})
		return
	}
	go h.mgr.RefreshMeta(cl.Name)
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "cluster_create", cl.Name, "", "", "", "纳管集群 "+cl.Name, "success"))
	c.JSON(http.StatusOK, gin.H{"message": "ok", "cluster": cl, "result": res})
}

// UpdateCluster PUT /api/v1/admin/clusters/:cluster — 改描述 / 轮换 kubeconfig
func (h *H) UpdateCluster(c *gin.Context) {
	name := c.Param("cluster")
	var cl model.Cluster
	if err := h.db.Where("name = ?", name).First(&cl).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "集群不存在"})
		return
	}
	var req struct {
		Description *string `json:"description"` // 指针区分「未提供」与「清空」
		GrafanaURL  *string `json:"grafana_url"` // Pod 监控看板地址前缀（含 var-namespace 前的全部参数），空=未配置
		Kubeconfig  string  `json:"kubeconfig"`
		ContextName string  `json:"context_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	auditDetail := ""
	if req.Description != nil {
		if cl.Description != strings.TrimSpace(*req.Description) {
			auditDetail = fmt.Sprintf("描述: %q → %q", cl.Description, strings.TrimSpace(*req.Description))
		}
		cl.Description = strings.TrimSpace(*req.Description)
	}
	if req.GrafanaURL != nil {
		newURL := strings.TrimSpace(*req.GrafanaURL)
		// scheme 校验（纵深防御，前端同款）：仅接受 http(s)，防 javascript: 等注入到 window.open
		lower := strings.ToLower(newURL)
		if newURL != "" && !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			c.JSON(http.StatusBadRequest, gin.H{"message": "监控地址必须以 http:// 或 https:// 开头"})
			return
		}
		if cl.GrafanaURL != newURL {
			if auditDetail != "" {
				auditDetail += "；"
			}
			if newURL == "" {
				auditDetail += "监控地址: 已清除"
			} else {
				auditDetail += "监控地址: 已配置"
			}
		}
		cl.GrafanaURL = newURL
	}
	if req.Kubeconfig != "" {
		// 凭据轮换：重新测试 → 重新加密
		res, err := cluster.TestKubeconfig([]byte(req.Kubeconfig), req.ContextName)
		if err != nil || !res.AllOK {
			c.JSON(http.StatusBadRequest, gin.H{"message": "新 kubeconfig 测试未通过", "result": res})
			return
		}
		cipher, err := cryptox.Encrypt([]byte(req.Kubeconfig))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "凭据加密失败"})
			return
		}
		sum := sha256.Sum256([]byte(req.Kubeconfig))
		h.db.Model(&model.ClusterCredential{}).Where("cluster_id = ?", cl.ID).
			Updates(map[string]any{
				"cipher_text":  cipher,
				"context_name": res.CurrentContext,
				"auth_type":    res.AuthType,
				"fingerprint":  hex.EncodeToString(sum[:8]),
			})
		cl.APIServers = firstOr(res.Servers, cl.APIServers)
		cl.Version = res.Version
		if _, err := h.mgr.Add(cl.Name, []byte(req.Kubeconfig), res.CurrentContext); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "客户端重载失败"})
			return
		}
		auditDetail += "（已轮换 kubeconfig 凭据）"
	}
	h.db.Save(&cl)
	if auditDetail == "" {
		auditDetail = "无字段变化"
	}
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "cluster_update", cl.Name, "", "", "", auditDetail, "success"))
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// DeleteCluster DELETE /api/v1/admin/clusters/:cluster — 级联删凭据/授权 + 移除连接
func (h *H) DeleteCluster(c *gin.Context) {
	name := c.Param("cluster")
	var cl model.Cluster
	if err := h.db.Where("name = ?", name).First(&cl).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "集群不存在"})
		return
	}
	h.db.Where("cluster_id = ?", cl.ID).Delete(&model.ClusterCredential{})
	h.db.Where("cluster_id = ?", cl.ID).Delete(&model.UserGrant{})
	h.db.Where("cluster_id = ?", cl.ID).Delete(&model.GrantGroupRule{})
	h.db.Delete(&cl)
	h.mgr.Remove(name)
	syncAuthz(h.db)
	audit.Log(audit.Record(middleware.UID(c), middleware.Username(c), "cluster_delete", name, "", "", "",
		fmt.Sprintf("移除集群 %s（版本 %s，节点 %d），同时清除其直接授权与权限组规则", name, cl.Version, cl.NodeCount), "success"))
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// AdminTestCluster POST /api/v1/admin/clusters/:cluster/test — 对已纳管集群重新探测
func (h *H) AdminTestCluster(c *gin.Context) {
	h.testClusterByParam(c)
}

// TestCluster GET /api/v1/clusters/:cluster/test — 需对该集群有授权（此前任何登录用户可探测任意集群）
func (h *H) TestCluster(c *gin.Context) {
	if middleware.Role(c) != model.RoleSuperAdmin {
		name := c.Param("cluster")
		for _, cl := range middleware.AllowedClusters(h.db, middleware.Role(c), middleware.UID(c)) {
			if cl.Name == name {
				h.testClusterByParam(c)
				return
			}
		}
		c.JSON(http.StatusForbidden, gin.H{"message": "无该集群的访问权限"})
		return
	}
	h.testClusterByParam(c)
}

func (h *H) testClusterByParam(c *gin.Context) {
	name := c.Param("cluster")
	var cred model.ClusterCredential
	var cl model.Cluster
	if err := h.db.Where("name = ?", name).First(&cl).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "集群不存在"})
		return
	}
	if err := h.db.Where("cluster_id = ?", cl.ID).First(&cred).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "凭据不存在"})
		return
	}
	raw, err := cryptox.Decrypt(cred.CipherText)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "凭据解密失败（加密密钥变更?）"})
		return
	}
	res, err := cluster.TestKubeconfig(raw, cred.ContextName)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

func firstOr(list []string, def string) string {
	if len(list) > 0 {
		return list[0]
	}
	return def
}
