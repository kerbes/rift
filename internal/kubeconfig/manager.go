package kubeconfig

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/phenixrizen/rift/internal/config"
	"github.com/phenixrizen/rift/internal/state"
	"k8s.io/client-go/tools/clientcmd"
	api "k8s.io/client-go/tools/clientcmd/api"
)

type SyncResult struct {
	AddedContexts   int
	UpdatedContexts int
	RemovedContexts int
}

func Sync(path string, st state.State, appCfg config.Config, dryRun bool) (SyncResult, error) {
	cfg, err := loadConfig(path)
	if err != nil {
		return SyncResult{}, err
	}
	result := SyncResult{}

	desired := map[string]state.ClusterRecord{}
	for _, cluster := range st.Clusters {
		desired[cluster.KubeContext] = cluster
	}

	for ctxName := range cfg.Contexts {
		if strings.HasPrefix(ctxName, "rift-") {
			if _, ok := desired[ctxName]; !ok {
				delete(cfg.Contexts, ctxName)
				delete(cfg.Clusters, ctxName)
				delete(cfg.AuthInfos, ctxName)
				result.RemovedContexts++
			}
		}
	}

	names := make([]string, 0, len(desired))
	for name := range desired {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, ctxName := range names {
		cluster := desired[ctxName]
		caData := []byte(cluster.ClusterCertificateBase64)
		if decoded, err := base64.StdEncoding.DecodeString(cluster.ClusterCertificateBase64); err == nil {
			caData = decoded
		}
		desiredCluster := &api.Cluster{
			Server:                   cluster.ClusterEndpoint,
			CertificateAuthorityData: caData,
		}
		var desiredUser *api.AuthInfo
		if tok, ok := appCfg.DevEndpoints.KubeToken(cluster.ClusterEndpoint); ok {
			desiredUser = &api.AuthInfo{Token: tok}
		} else {
			desiredUser = &api.AuthInfo{
				Exec: &api.ExecConfig{
					APIVersion: "client.authentication.k8s.io/v1beta1",
					Command:    "aws",
					Args: []string{
						"eks",
						"get-token",
						"--profile",
						cluster.AWSProfile,
						"--cluster-name",
						cluster.ClusterName,
						"--region",
						cluster.Region,
					},
				},
			}
		}
		desiredContext := &api.Context{
			Cluster:  ctxName,
			AuthInfo: ctxName,
		}
		if cluster.Namespace != "" {
			desiredContext.Namespace = cluster.Namespace
		}

		_, clusterExisted := cfg.Clusters[ctxName]
		if !clusterExisted {
			result.AddedContexts++
		}
		if clusterExisted && (!clusterEqual(cfg.Clusters[ctxName], desiredCluster) || !userEqual(cfg.AuthInfos[ctxName], desiredUser) || !contextEqual(cfg.Contexts[ctxName], desiredContext)) {
			result.UpdatedContexts++
		}

		cfg.Clusters[ctxName] = desiredCluster
		cfg.AuthInfos[ctxName] = desiredUser
		cfg.Contexts[ctxName] = desiredContext
	}

	if cfg.CurrentContext != "" {
		if _, ok := cfg.Contexts[cfg.CurrentContext]; !ok {
			cfg.CurrentContext = ""
		}
	}
	if cfg.CurrentContext == "" && len(names) > 0 {
		cfg.CurrentContext = names[0]
	}

	if dryRun {
		return result, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return result, err
	}
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		return result, err
	}
	return result, nil
}

func loadConfig(path string) (*api.Config, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return api.NewConfig(), nil
		}
		return nil, err
	}
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return nil, err
	}
	if cfg.Clusters == nil {
		cfg.Clusters = map[string]*api.Cluster{}
	}
	if cfg.AuthInfos == nil {
		cfg.AuthInfos = map[string]*api.AuthInfo{}
	}
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]*api.Context{}
	}
	return cfg, nil
}

func clusterEqual(a, b *api.Cluster) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Server != b.Server {
		return false
	}
	return string(a.CertificateAuthorityData) == string(b.CertificateAuthorityData)
}

func userEqual(a, b *api.AuthInfo) bool {
	if a == nil || b == nil {
		return a == b
	}
	// If either side uses token auth, compare tokens.
	if a.Token != "" || b.Token != "" {
		return a.Token == b.Token
	}
	if a.Exec == nil || b.Exec == nil {
		return a.Exec == b.Exec
	}
	if a.Exec.Command != b.Exec.Command || len(a.Exec.Args) != len(b.Exec.Args) {
		return false
	}
	for i := range a.Exec.Args {
		if a.Exec.Args[i] != b.Exec.Args[i] {
			return false
		}
	}
	return true
}

func contextEqual(a, b *api.Context) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Cluster == b.Cluster && a.AuthInfo == b.AuthInfo && a.Namespace == b.Namespace
}
