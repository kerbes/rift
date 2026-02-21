package namespaces

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/phenixrizen/rift/internal/config"
	"github.com/phenixrizen/rift/internal/state"
	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type Result struct {
	Enabled         bool
	ClustersTried   int
	ClustersUpdated int
	Errors          int
}

type tokenResponse struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}

func Enrich(ctx context.Context, st *state.State, appCfg config.Config, logger *slog.Logger) (Result, error) {
	result := Result{Enabled: true}
	if st == nil || len(st.Clusters) == 0 {
		return result, nil
	}

	type outcome struct {
		idx        int
		namespaces []string
		err        error
	}

	outcomes := make([]outcome, 0, len(st.Clusters))
	var mu sync.Mutex

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(4)

	for idx, cluster := range st.Clusters {
		idx := idx
		cluster := cluster
		if strings.TrimSpace(cluster.ClusterEndpoint) == "" || strings.TrimSpace(cluster.ClusterName) == "" {
			continue
		}
		result.ClustersTried++
		g.Go(func() error {
			namespaces, err := fetchClusterNamespaces(gctx, cluster, appCfg)
			mu.Lock()
			outcomes = append(outcomes, outcome{idx: idx, namespaces: namespaces, err: err})
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, err
	}

	for _, item := range outcomes {
		if item.err != nil {
			result.Errors++
			if logger != nil {
				cluster := st.Clusters[item.idx]
				logger.Warn(
					"namespace discovery failed",
					"context", cluster.KubeContext,
					"cluster", cluster.ClusterName,
					"region", cluster.Region,
					"error", item.err,
				)
			}
			continue
		}
		merged := mergeNamespaces(st.Clusters[item.idx], item.namespaces)
		if !equalStringSets(st.Clusters[item.idx].Namespaces, merged) {
			st.Clusters[item.idx].Namespaces = merged
			result.ClustersUpdated++
		}
	}

	return result, nil
}

func fetchClusterNamespaces(ctx context.Context, cluster state.ClusterRecord, appCfg config.Config) ([]string, error) {
	var token string
	if tok, ok := appCfg.DevEndpoints.KubeToken(cluster.ClusterEndpoint); ok {
		token = tok
	} else {
		var err error
		token, err = fetchToken(ctx, cluster)
		if err != nil {
			return nil, err
		}
	}

	caData := []byte(cluster.ClusterCertificateBase64)
	if decoded, err := base64.StdEncoding.DecodeString(cluster.ClusterCertificateBase64); err == nil {
		caData = decoded
	}

	cfg := &rest.Config{
		Host:        cluster.ClusterEndpoint,
		BearerToken: token,
		TLSClientConfig: rest.TLSClientConfig{
			CAData: caData,
		},
		Timeout: 15 * time.Second,
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	out, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	namespaces := make([]string, 0, len(out.Items))
	for _, item := range out.Items {
		if name := strings.TrimSpace(item.Name); name != "" {
			namespaces = append(namespaces, name)
		}
	}
	sort.Strings(namespaces)
	return namespaces, nil
}

func fetchToken(ctx context.Context, cluster state.ClusterRecord) (string, error) {
	args := []string{
		"eks",
		"get-token",
		"--profile",
		cluster.AWSProfile,
		"--cluster-name",
		cluster.ClusterName,
		"--region",
		cluster.Region,
		"--output",
		"json",
	}
	cmd := exec.CommandContext(ctx, "aws", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if msg != "" {
			return "", fmt.Errorf("aws eks get-token: %s", msg)
		}
		return "", err
	}
	var parsed tokenResponse
	if err := json.Unmarshal(output, &parsed); err != nil {
		return "", err
	}
	token := strings.TrimSpace(parsed.Status.Token)
	if token == "" {
		return "", fmt.Errorf("empty token from aws eks get-token")
	}
	return token, nil
}

func mergeNamespaces(cluster state.ClusterRecord, discovered []string) []string {
	set := map[string]struct{}{}
	for _, ns := range cluster.Namespaces {
		ns = strings.TrimSpace(ns)
		if ns != "" {
			set[ns] = struct{}{}
		}
	}
	if ns := strings.TrimSpace(cluster.Namespace); ns != "" {
		set[ns] = struct{}{}
	}
	for _, ns := range discovered {
		ns = strings.TrimSpace(ns)
		if ns != "" {
			set[ns] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for ns := range set {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	left := append([]string(nil), a...)
	right := append([]string(nil), b...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
