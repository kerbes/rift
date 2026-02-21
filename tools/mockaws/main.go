// mockaws is a lightweight HTTP server that emulates the AWS SSO and EKS APIs
// used by Rift during local development and testing.
//
// It reads a topology YAML file that defines fake AWS accounts, roles, and EKS
// clusters, then serves the exact API shapes the AWS SDK expects — so Rift can
// run `discover` without a real AWS account or SSO session.
//
// Usage:
//
//	go run ./tools/mockaws --topology ./tools/mockaws/topology.yaml
//
// Then point your dev config.yaml at it:
//
//	dev_endpoints:
//	  sso_endpoint: http://localhost:8080
//	  eks_endpoint: http://localhost:8080
//	  access_token: dev-token
//
// # API surface emulated
//
// SSO:
//   - GET /assignment/accounts                       → ListAccounts
//   - GET /assignment/accounts/{id}/roles            → ListAccountRoles
//   - GET /federation/credentials                    → GetRoleCredentials
//
// EKS:
//   - GET /clusters                                  → ListClusters
//   - GET /clusters/{name}                           → DescribeCluster
//
// # Region handling
//
// Real EKS endpoints are regional (e.g. eks.us-east-1.amazonaws.com). Because
// the mock serves all regions from a single port, configure only one region in
// your dev config.yaml to avoid duplicate cluster entries:
//
//	regions:
//	  - us-east-1
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// -----------------------------------------------------------------------
// Topology definition — mirrors the structure of topology.yaml
// -----------------------------------------------------------------------

// Topology is the top-level structure of the topology YAML file.
type Topology struct {
	Accounts []AccountDef `yaml:"accounts"`
	Clusters []ClusterDef `yaml:"clusters"`
}

// AccountDef defines a fake AWS account and the IAM roles accessible within it.
type AccountDef struct {
	ID    string    `yaml:"id"`
	Name  string    `yaml:"name"`
	Roles []RoleDef `yaml:"roles"`
}

// RoleDef defines an IAM role within an account.
type RoleDef struct {
	Name string `yaml:"name"`
}

// ClusterDef defines a fake EKS cluster returned by the mock.
type ClusterDef struct {
	// AccountID ties this cluster to a specific account for ARN generation.
	AccountID string `yaml:"account_id"`
	// Name is the EKS cluster name (must be unique within a region).
	Name string `yaml:"name"`
	// Region is the AWS region this cluster lives in.
	// Note: because the mock serves all regions from one port, you should
	// configure only one region in your Rift dev config.yaml.
	Region string `yaml:"region"`
	// Endpoint is the Kubernetes API server URL returned in DescribeCluster.
	// For local testing this can point at your local k3s/minikube/kind cluster.
	Endpoint string `yaml:"endpoint"`
	// CertificateBase64 is the base64-encoded cluster CA certificate.
	// Leave empty to use a placeholder value.
	CertificateBase64 string `yaml:"certificate_base64,omitempty"`
}

// -----------------------------------------------------------------------
// AWS API response shapes (only the fields Rift actually reads)
// -----------------------------------------------------------------------

type listAccountsResponse struct {
	AccountList []accountInfo `json:"accountList"`
	NextToken   *string       `json:"nextToken"`
}

type accountInfo struct {
	AccountID   string `json:"accountId"`
	AccountName string `json:"accountName"`
	EmailAddress string `json:"emailAddress"`
}

type listAccountRolesResponse struct {
	RoleList  []roleInfo `json:"roleList"`
	NextToken *string    `json:"nextToken"`
}

type roleInfo struct {
	AccountID string `json:"accountId"`
	RoleName  string `json:"roleName"`
}

type getRoleCredentialsResponse struct {
	RoleCredentials roleCredentials `json:"roleCredentials"`
}

type roleCredentials struct {
	AccessKeyId     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken"`
	Expiration      int64  `json:"expiration"` // Unix ms
}

type listClustersResponse struct {
	Clusters  []string `json:"clusters"`
	NextToken *string  `json:"nextToken"`
}

type describeClusterResponse struct {
	Cluster clusterDetail `json:"cluster"`
}

type clusterDetail struct {
	Name                 string              `json:"name"`
	Arn                  string              `json:"arn"`
	Endpoint             string              `json:"endpoint"`
	CertificateAuthority certificateAuthority `json:"certificateAuthority"`
}

type certificateAuthority struct {
	Data string `json:"data"`
}

// -----------------------------------------------------------------------
// Server
// -----------------------------------------------------------------------

type server struct {
	topology Topology
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s %s", r.Method, r.URL.Path)

	path := r.URL.Path
	w.Header().Set("Content-Type", "application/json")

	switch {
	// SSO: ListAccounts
	case r.Method == http.MethodGet && path == "/assignment/accounts":
		s.handleListAccounts(w, r)

	// SSO: ListAccountRoles — path: /assignment/roles?account_id={accountId}
	case r.Method == http.MethodGet && path == "/assignment/roles":
		s.handleListAccountRoles(w, r)

	// SSO: GetRoleCredentials
	case r.Method == http.MethodGet && path == "/federation/credentials":
		s.handleGetRoleCredentials(w, r)

	// EKS: DescribeCluster — path: /clusters/{name}  (must come before ListClusters)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/clusters/"):
		s.handleDescribeCluster(w, r)

	// EKS: ListClusters
	case r.Method == http.MethodGet && path == "/clusters":
		s.handleListClusters(w, r)

	default:
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	}
}

// handleListAccounts returns all accounts defined in the topology.
func (s *server) handleListAccounts(w http.ResponseWriter, _ *http.Request) {
	resp := listAccountsResponse{}
	for _, a := range s.topology.Accounts {
		resp.AccountList = append(resp.AccountList, accountInfo{
			AccountID:   a.ID,
			AccountName: a.Name,
			EmailAddress: fmt.Sprintf("%s@example.com", strings.ToLower(strings.ReplaceAll(a.Name, " ", "-"))),
		})
	}
	writeJSON(w, resp)
}

// handleListAccountRoles returns the roles for the account ID in the query string.
func (s *server) handleListAccountRoles(w http.ResponseWriter, r *http.Request) {
	// Query: /assignment/roles?account_id={accountId}
	accountID := r.URL.Query().Get("account_id")
	if accountID == "" {
		http.Error(w, `{"message":"bad request: missing account_id"}`, http.StatusBadRequest)
		return
	}

	resp := listAccountRolesResponse{}
	for _, a := range s.topology.Accounts {
		if a.ID != accountID {
			continue
		}
		for _, role := range a.Roles {
			resp.RoleList = append(resp.RoleList, roleInfo{
				AccountID: a.ID,
				RoleName:  role.Name,
			})
		}
	}
	writeJSON(w, resp)
}

// handleGetRoleCredentials returns static fake credentials for any account/role.
// The credentials are intentionally non-functional against real AWS.
func (s *server) handleGetRoleCredentials(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	accountID := q.Get("account_id")
	roleName := q.Get("role_name")

	resp := getRoleCredentialsResponse{
		RoleCredentials: roleCredentials{
			// Credentials are deliberately fake and only valid against the mock server.
			AccessKeyId:     fmt.Sprintf("AKIAIOSFODNN7MOCK%s", strings.ToUpper(accountID[:4])),
			SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYMOCKSECRETKEY",
			SessionToken:    fmt.Sprintf("mock-session-token-account-%s-role-%s", accountID, roleName),
			Expiration:      time.Now().Add(8 * time.Hour).UnixMilli(),
		},
	}
	writeJSON(w, resp)
}

// handleListClusters returns the names of all clusters in the topology.
// Because the mock serves all regions from one port, all clusters are returned
// for every request regardless of which region the client thinks it is targeting.
func (s *server) handleListClusters(w http.ResponseWriter, _ *http.Request) {
	resp := listClustersResponse{}
	for _, c := range s.topology.Clusters {
		resp.Clusters = append(resp.Clusters, c.Name)
	}
	writeJSON(w, resp)
}

// handleDescribeCluster returns full details for a single cluster by name.
func (s *server) handleDescribeCluster(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/clusters/")
	if name == "" {
		http.Error(w, `{"message":"cluster name required"}`, http.StatusBadRequest)
		return
	}

	for _, c := range s.topology.Clusters {
		if c.Name != name {
			continue
		}
		cert := c.CertificateBase64
		if cert == "" {
			// Placeholder cert — enough to satisfy the SDK's response parsing.
			cert = "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUJJVEFOQUJBZ0lRQ2pMNVhTOXBCNkJHZGZXeXZoQm1EakFLQmdncWhrM09QUVFEQWpBQU1CNFhEVEl3Ck1EY3dNakF3TURBd01GWVhEVEl3TURjd01qQXdNREF3TUZZd0FEQ0JJVEFOQUJBZ0lRQ2pMNVhTOXBCNkJHZGZXeXYyQkJBQUFBQT09Ci0tLS0tRU5EIENFUlRJRklDQVRFLS0tLS0K"
		}
		arn := fmt.Sprintf("arn:aws:eks:%s:%s:cluster/%s", c.Region, c.AccountID, c.Name)
		resp := describeClusterResponse{
			Cluster: clusterDetail{
				Name:     c.Name,
				Arn:      arn,
				Endpoint: c.Endpoint,
				CertificateAuthority: certificateAuthority{
					Data: cert,
				},
			},
		}
		writeJSON(w, resp)
		return
	}

	http.Error(w, fmt.Sprintf(`{"message":"cluster %q not found"}`, name), http.StatusNotFound)
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("error encoding response: %v", err)
	}
}

func loadTopology(path string) (Topology, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Topology{}, fmt.Errorf("read topology file: %w", err)
	}
	var t Topology
	if err := yaml.Unmarshal(data, &t); err != nil {
		return Topology{}, fmt.Errorf("parse topology file: %w", err)
	}
	return t, nil
}

// -----------------------------------------------------------------------
// Entry point
// -----------------------------------------------------------------------

func main() {
	topologyPath := flag.String("topology", "tools/mockaws/topology.yaml", "path to topology YAML file")
	addr := flag.String("addr", ":8080", "address to listen on")
	flag.Parse()

	topology, err := loadTopology(*topologyPath)
	if err != nil {
		log.Fatalf("failed to load topology: %v", err)
	}

	log.Printf("mockaws: loaded %d accounts, %d clusters", len(topology.Accounts), len(topology.Clusters))

	accountCount := 0
	for _, a := range topology.Accounts {
		accountCount++
		log.Printf("  account %s (%s): %d role(s)", a.Name, a.ID, len(a.Roles))
	}
	for _, c := range topology.Clusters {
		log.Printf("  cluster %s in %s (account %s)", c.Name, c.Region, c.AccountID)
	}

	srv := &server{topology: topology}
	log.Printf("mockaws: listening on %s", *addr)
	if err := http.ListenAndServe(*addr, srv); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
