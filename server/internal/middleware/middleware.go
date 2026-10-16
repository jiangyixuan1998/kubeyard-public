package middleware

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"kubeyard/internal/auth"
	"kubeyard/internal/authz"
	"kubeyard/internal/database"
	"kubeyard/internal/model"

	"gorm.io/gorm"
)

// CORS 允许来源：allowOrigins 为空或 "*" 反射任意请求 Origin（内网/开发前后端分离的默认行为）；
// 配置逗号分隔列表则仅白名单内 Origin 放行，其余不回 CORS 头（浏览器按同源策略拦截）
func CORS(allowOrigins string) gin.HandlerFunc {
	allowed := map[string]bool{}
	if s := strings.TrimSpace(allowOrigins); s != "" && s != "*" {
		for _, o := range strings.Split(s, ",") {
			if o = strings.TrimSpace(o); o != "" {
				allowed[o] = true
			}
		}
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && (len(allowed) == 0 || allowed[origin]) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type")
			c.Header("Access-Control-Allow-Credentials", "true")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// JWTAuth REST 鉴权：Bearer access token + 吊销检查
func JWTAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "未登录"})
			return
		}
		claims, err := auth.Parse(strings.TrimPrefix(h, "Bearer "))
		if err != nil || claims.Type != auth.TokenAccess {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "登录已失效，请重新登录"})
			return
		}
		if auth.TokenBlocked(database.DB, claims) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "登录已失效，请重新登录"})
			return
		}
		c.Set("uid", claims.UserID)
		c.Set("username", claims.Username)
		c.Set("role", claims.Role)
		c.Set("jti", claims.ID)
		c.Next()
	}
}

func RequireSuperAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetString("role") != model.RoleSuperAdmin {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"message": "需要平台管理员权限"})
			return
		}
		c.Next()
	}
}

// 请求上下文快捷取值
func UID(c *gin.Context) uint        { return c.GetUint("uid") }
func Username(c *gin.Context) string { return c.GetString("username") }
func Role(c *gin.Context) string     { return c.GetString("role") }

// EffectiveGrants 用户生效授权 = 直接授权 + 所属权限组规则（并集）；带到期时间的授权过期后自动失效。
// 个人账号到期后授权整体失效（登录不拦但看不到任何资源）；服务账号到期被登录/刷新拦截，此处不会出现有效会话
func EffectiveGrants(db *gorm.DB, userID uint) []model.UserGrant {
	var u model.User
	if err := db.First(&u, userID).Error; err == nil && u.Expired() {
		return nil
	}
	var out []model.UserGrant
	db.Where("user_id = ? AND (expires_at IS NULL OR expires_at > ?)", userID, time.Now()).Find(&out)
	var mbs []model.UserGroup
	// 成员关系到期 = 失去该组全部权限（时限绑在用户入组上，不绑规则，规则可长期复用）
	db.Where("user_id = ? AND (expires_at IS NULL OR expires_at > ?)", userID, time.Now()).Find(&mbs)
	if len(mbs) == 0 {
		return out
	}
	ids := make([]uint, 0, len(mbs))
	for _, m := range mbs {
		ids = append(ids, m.GroupID)
	}
	var rules []model.GrantGroupRule
	db.Where("group_id IN ?", ids).Find(&rules)
	for _, r := range rules {
		out = append(out, model.UserGrant{
			ClusterID: r.ClusterID,
			Namespace: r.Namespace,
			Resource:  r.Resource,
			Verb:      r.Verb,
			Actions:   r.Actions,
		})
	}
	return out
}

// AdminScope 管理员数据可见范围（审计日志 / 终端录像）：
// SuperAdmin 全量；否则仅统计"资源=* 且 动词=*"的管理员级规则——
// 命名空间为 * 的规则 = 该集群管理员（整集群可见），具体命名空间 = 命名空间管理员（仅这些命名空间可见）。
// 返回 all=是否全量；clusterAll=管辖整集群名集合；nsOf=集群名→管辖命名空间集合。
func AdminScope(db *gorm.DB, role string, userID uint) (all bool, clusterAll map[string]bool, nsOf map[string]map[string]bool) {
	clusterAll = map[string]bool{}
	nsOf = map[string]map[string]bool{}
	if role == model.RoleSuperAdmin {
		return true, clusterAll, nsOf
	}
	grants := EffectiveGrants(db, userID)
	var cls []model.Cluster
	db.Find(&cls)
	names := map[uint]string{}
	for _, cl := range cls {
		names[cl.ID] = cl.Name
	}
	for _, g := range grants {
		res := g.Resource
		if res == "" {
			res = "*"
		}
		verb := g.Verb
		if verb == "" {
			verb = "*"
		}
		if res != "*" || verb != "*" {
			continue // 仅管理员级规则解锁审计/录像查看
		}
		name := names[g.ClusterID]
		if name == "" {
			continue
		}
		ns := g.Namespace
		if ns == "" {
			ns = "*"
		}
		for _, n := range strings.Split(ns, ",") {
			n = strings.TrimSpace(n)
			if n == "*" {
				clusterAll[name] = true
				continue
			}
			if n == "" {
				continue
			}
			if nsOf[name] == nil {
				nsOf[name] = map[string]bool{}
			}
			nsOf[name][n] = true
		}
	}
	return false, clusterAll, nsOf
}

// ScopeSQL 依据 AdminScope 结果生成 (cluster, namespace) 维度的 SQL 过滤条件与参数；
// 无任何管辖范围时返回恒假条件（1=0）。调用方只对非 all 的情况使用。
func ScopeSQL(clusterAll map[string]bool, nsOf map[string]map[string]bool) (string, []any) {
	var conds []string
	var args []any
	if len(clusterAll) > 0 {
		names := make([]string, 0, len(clusterAll))
		for n := range clusterAll {
			names = append(names, n)
		}
		conds = append(conds, "cluster IN ?")
		args = append(args, names)
	}
	for cl, nss := range nsOf {
		list := make([]string, 0, len(nss))
		for n := range nss {
			list = append(list, n)
		}
		conds = append(conds, "(cluster = ? AND namespace IN ?)")
		args = append(args, cl, list)
	}
	if len(conds) == 0 {
		return "1 = 0", nil
	}
	return strings.Join(conds, " OR "), args
}

// ScopeCovers 判断某 (cluster, namespace) 是否落在管辖范围内
func ScopeCovers(cluster, namespace string, clusterAll map[string]bool, nsOf map[string]map[string]bool) bool {
	if clusterAll[cluster] {
		return true
	}
	if ns, ok := nsOf[cluster]; ok && ns[namespace] {
		return true
	}
	return false
}

// Allowed 五元组鉴权：SuperAdmin 短路；生效授权（直授+权限组）匹配 namespace/resource/verb（支持 "*" 与逗号列表）
func Allowed(db *gorm.DB, role string, userID uint, clusterName, namespace, verb, resource string) bool {
	if role == model.RoleSuperAdmin {
		return true
	}
	legacy := legacyAllowed(db, role, userID, clusterName, namespace, verb, resource)
	detail := fmt.Sprintf("user=%d cluster=%s namespace=%q verb=%s resource=%s", userID, clusterName, namespace, verb, resource)
	return authz.CompareAllowed("authorize", detail, legacy, func() (bool, error) {
		return authz.Authorize(db, userID, clusterName, namespace, verb, resource)
	})
}

func legacyAllowed(db *gorm.DB, role string, userID uint, clusterName, namespace, verb, resource string) bool {
	if role == model.RoleSuperAdmin {
		return true
	}
	var cl model.Cluster
	if err := db.Where("name = ?", clusterName).First(&cl).Error; err != nil {
		return false
	}
	for _, g := range EffectiveGrants(db, userID) {
		if g.ClusterID != cl.ID {
			continue
		}
		if !entryMatch(g.Namespace, namespace) {
			continue
		}
		if !entryMatch(g.Resource, resource) {
			continue
		}
		if !entryMatch(g.Verb, verb) {
			continue
		}
		return true
	}
	return false
}

// entryMatch granted 支持 "*" / 空串(视为*) / 逗号分隔列表；want 为 "*" 时仅 "*" 命配；
// want 为空串表示「ns 维度不适用」（集群级资源，如 nodes/namespaces/storageclasses）：
// 仅 ns=* 或 ns 为空的授权命中——具体命名空间的授权不应放行集群级资源操作
// （外部评审核实：此前任意 ns 授权均命中，仅授 ent3 的用户可列出全集群节点）。
// namespaced 资源若请求不带 ns，后续 apiserver 路径（缺 namespace 段）会拒绝，不构成越权。
func entryMatch(granted, want string) bool {
	granted = strings.TrimSpace(granted)
	if granted == "*" || granted == "" {
		return true
	}
	if want == "" {
		return false // 集群级资源：需要 ns=* 的授权（具体 ns 授权不放行）
	}
	if want == "*" {
		return false
	}
	for _, s := range strings.Split(granted, ",") {
		if strings.TrimSpace(s) == want {
			return true
		}
	}
	return false
}

// AllowedClusters 用户可见的集群列表（SuperAdmin 全部；含权限组授予的集群）
func AllowedClusters(db *gorm.DB, role string, userID uint) []model.Cluster {
	var all []model.Cluster
	if role == model.RoleSuperAdmin {
		db.Order("name ASC").Find(&all)
		return all
	}
	seen := map[uint]bool{}
	var ids []uint
	for _, g := range EffectiveGrants(db, userID) {
		if !seen[g.ClusterID] {
			seen[g.ClusterID] = true
			ids = append(ids, g.ClusterID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	db.Where("id IN ?", ids).Order("name ASC").Find(&all)
	return all
}

// VisibleNamespaces 用户在某集群可见的命名空间（含权限组）；all=true 表示全部可见（支持逗号多选授权）
func VisibleNamespaces(db *gorm.DB, role string, userID uint, clusterName string) (list []string, all bool) {
	if role == model.RoleSuperAdmin {
		return nil, true
	}
	var cl model.Cluster
	if err := db.Where("name = ?", clusterName).First(&cl).Error; err != nil {
		return nil, false
	}
	return VisibleNSFromGrants(EffectiveGrants(db, userID), cl.ID)
}

// VisibleNSFromGrants 与 VisibleNamespaces 同语义，但复用调用方已查询的授权列表
// （总览页一次 EffectiveGrants 后按集群/资源多次复用，避免逐项查库）
func VisibleNSFromGrants(grants []model.UserGrant, clusterID uint) (list []string, all bool) {
	seen := map[string]bool{}
	for _, g := range grants {
		if g.ClusterID != clusterID {
			continue
		}
		for _, part := range strings.Split(g.Namespace, ",") {
			part = strings.TrimSpace(part)
			if part == "" || part == "*" {
				return nil, true
			}
			if !seen[part] {
				seen[part] = true
				list = append(list, part)
			}
		}
	}
	return list, false
}

// ListableNamespaces 用户可对某资源执行 list 的命名空间集合（资源级 list 鉴权用）：
// all=true 表示跨全命名空间（存在 ns=* 的 list 授权）；ok=false 表示对该资源无任何 list 授权。
func ListableNamespaces(db *gorm.DB, role string, userID uint, clusterName, resource string) (nss []string, all, ok bool) {
	if role == model.RoleSuperAdmin {
		return nil, true, true
	}
	var cl model.Cluster
	if err := db.Where("name = ?", clusterName).First(&cl).Error; err != nil {
		return nil, false, false
	}
	return ListableNSFromGrants(EffectiveGrants(db, userID), cl.ID, resource)
}

// ListableNSFromGrants 与 ListableNamespaces 同语义，但复用调用方已查询的授权列表
func ListableNSFromGrants(grants []model.UserGrant, clusterID uint, resource string) (nss []string, all, ok bool) {
	seen := map[string]bool{}
	for _, g := range grants {
		if g.ClusterID != clusterID {
			continue
		}
		if !entryMatch(g.Resource, resource) || !entryMatch(g.Verb, "list") {
			continue
		}
		for _, part := range strings.Split(g.Namespace, ",") {
			part = strings.TrimSpace(part)
			if part == "" || part == "*" {
				return nil, true, true
			}
			if !seen[part] {
				seen[part] = true
				nss = append(nss, part)
			}
		}
	}
	return nss, false, len(nss) > 0
}

// HasListGrantInCluster 授权列表中是否存在「该集群 + 该资源」的 list 授权（ns 任意）；
// ListCRs 发现可见性过滤用——未授权的 CR 不对用户暴露。
func HasListGrantInCluster(grants []model.UserGrant, clusterID uint, resource string) bool {
	for _, g := range grants {
		if g.ClusterID == clusterID && entryMatch(g.Resource, resource) && entryMatch(g.Verb, "list") {
			return true
		}
	}
	return false
}
