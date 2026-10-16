package cluster

import (
	"context"
	"fmt"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func metav1ListOptions() metav1.ListOptions { return metav1.ListOptions{} }

// TestStep 纳管测试分步结果（前端步骤条展示）
type TestStep struct {
	Name    string   `json:"name"`
	OK      bool     `json:"ok"`
	Message string   `json:"message"`
	Details []string `json:"details,omitempty"`
}

// KubeTestResult 导入 kubeconfig 的完整测试结果
type KubeTestResult struct {
	Steps          []TestStep `json:"steps"`
	AllOK          bool       `json:"all_ok"`
	Version        string     `json:"version,omitempty"`
	Servers        []string   `json:"servers,omitempty"`
	Contexts       []string   `json:"contexts,omitempty"`
	CurrentContext string     `json:"current_context,omitempty"`
	AuthType       string     `json:"auth_type,omitempty"`
}

// ListContexts 解析 kubeconfig 的 context 列表（多 context 文件让管理员选择）
func ListContexts(raw []byte) (contexts []string, current string, err error) {
	apiCfg, err := clientcmd.Load(raw)
	if err != nil {
		return nil, "", fmt.Errorf("解析 kubeconfig: %w", err)
	}
	for name := range apiCfg.Contexts {
		contexts = append(contexts, name)
	}
	return contexts, apiCfg.CurrentContext, nil
}

func detectAuthType(apiCfg *clientcmdapi.Config, contextName string) string {
	ctx := apiCfg.Contexts[contextName]
	if ctx == nil {
		return "unknown"
	}
	auth := apiCfg.AuthInfos[ctx.AuthInfo]
	switch {
	case auth == nil:
		return "none"
	case auth.Token != "" || auth.TokenFile != "":
		return "token"
	case len(auth.ClientCertificateData) > 0 || auth.ClientCertificate != "":
		return "clientcert"
	case auth.Username != "":
		return "basicauth"
	default:
		return "none"
	}
}

// TestKubeconfig 导入前测试：解析 → 网络证书 → 读权限 → 写权限（SelfSubjectAccessReview，零副作用）
func TestKubeconfig(raw []byte, contextName string) (*KubeTestResult, error) {
	apiCfg, err := clientcmd.Load(raw)
	if err != nil {
		return nil, fmt.Errorf("解析 kubeconfig: %w", err)
	}
	if contextName == "" {
		contextName = apiCfg.CurrentContext
	}
	res := &KubeTestResult{CurrentContext: contextName}
	for name := range apiCfg.Contexts {
		res.Contexts = append(res.Contexts, name)
	}
	res.AuthType = detectAuthType(apiCfg, contextName)

	servers, ctxName, err := kubeconfigServers(raw, contextName)
	if err != nil {
		return nil, err
	}
	res.Servers = servers

	e, err := buildEntry("test", raw, ctxName)
	if err != nil {
		// buildEntry 内部已尝试全部 server，失败信息补进步骤
		res.Steps = append(res.Steps,
			TestStep{Name: "网络与证书", OK: false, Message: err.Error()},
		)
		return res, nil
	}
	version, verr := e.Client.Discovery().ServerVersion()
	if verr != nil {
		res.Steps = append(res.Steps, TestStep{Name: "网络与证书", OK: false, Message: verr.Error() + "（检查网络/CA/证书）"})
		return res, nil
	}
	res.Version = version.GitVersion
	res.Steps = append(res.Steps, TestStep{Name: "网络与证书", OK: true, Message: version.GitVersion})

	// 读权限
	ctx2, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := e.Client.CoreV1().Namespaces().List(ctx2, metav1.ListOptions{Limit: 1}); err != nil {
		res.Steps = append(res.Steps, TestStep{Name: "读权限（list namespaces）", OK: false, Message: err.Error()})
		return res, nil
	}
	res.Steps = append(res.Steps, TestStep{Name: "读权限（list namespaces）", OK: true, Message: "允许"})

	// 写权限：SSAR 自检，不执行任何真实写操作
	checks := []authv1.ResourceAttributes{
		{Verb: "create", Group: "apps", Resource: "deployments"},
		{Verb: "update", Group: "", Resource: "pods"},
		{Verb: "delete", Group: "", Resource: "pods"},
	}
	writeOK, denied := true, ""
	for i := range checks {
		review := &authv1.SelfSubjectAccessReview{Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &checks[i]}}
		result, err := e.Client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx2, review, metav1.CreateOptions{})
		if err != nil {
			writeOK, denied = false, err.Error()
			break
		}
		if !result.Status.Allowed {
			writeOK = false
			denied = fmt.Sprintf("缺少 %s %s/%s", checks[i].Verb, checks[i].Group, checks[i].Resource)
			break
		}
	}
	if writeOK {
		res.Steps = append(res.Steps, TestStep{Name: "写权限（SelfSubjectAccessReview）", OK: true, Message: "允许（create/update/delete）"})
	} else {
		res.Steps = append(res.Steps, TestStep{Name: "写权限（SelfSubjectAccessReview）", OK: false, Message: denied + "（平台要求 kubeconfig 具读写权限）"})
	}

	for _, s := range res.Steps {
		if !s.OK {
			return res, nil
		}
	}
	res.AllOK = true
	return res, nil
}

// 编译期断言：确保 kubernetes 包被引用（buildEntry 返回类型依赖）
var _ = kubernetes.NewForConfig
