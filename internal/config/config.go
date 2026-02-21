package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	configDirName  = ".config/rift"
	configFileName = "config.yaml"
	stateFileName  = "state.json"
)

var defaultRegions = []string{"us-east-1", "us-west-2"}

// DevEndpoints redirects Rift's AWS API calls to a local mock server.
//
// This is intended exclusively for local development and testing. When active,
// Rift will bypass the normal AWS SSO token cache and use the AccessToken
// field below instead of a real SSO session.
//
// To use, add a dev_endpoints block to your config.yaml:
//
//	dev_endpoints:
//	  sso_endpoint: http://localhost:8080
//	  eks_endpoint: http://localhost:8080
//	  access_token: dev-token
//
// Then start the mock server:
//
//	go run ./tools/mockaws --topology ./tools/mockaws/topology.yaml
//
// Leave this block out of config.yaml entirely for normal production use.
type DevEndpoints struct {
	// SSOEndpoint overrides the AWS SSO service base URL.
	// The mock server must implement the SSO REST API paths:
	//   GET /assignment/accounts
	//   GET /assignment/accounts/{id}/roles
	//   GET /federation/credentials
	SSOEndpoint string `yaml:"sso_endpoint,omitempty"`

	// EKSEndpoint overrides the AWS EKS service base URL.
	// The mock server must implement:
	//   GET /clusters
	//   GET /clusters/{name}
	EKSEndpoint string `yaml:"eks_endpoint,omitempty"`

	// AccessToken is sent as the SSO bearer token to the mock server,
	// replacing the token that would normally be read from ~/.aws/sso/cache.
	// Any non-empty string is valid; the mock server accepts all tokens.
	AccessToken string `yaml:"access_token,omitempty"`

	// KubeTokens maps cluster endpoint URLs to bearer tokens. When a cluster's
	// endpoint matches a key in this map, Rift writes a token-based AuthInfo
	// in kubeconfig instead of an exec-based (aws eks get-token) AuthInfo.
	// This allows k3d or other local clusters to work without real AWS credentials.
	KubeTokens map[string]string `yaml:"kube_tokens,omitempty"`
}

// IsActive returns true if dev endpoints have been configured, meaning Rift
// should target the local mock server instead of real AWS.
func (d *DevEndpoints) IsActive() bool {
	return d != nil && (d.SSOEndpoint != "" || d.EKSEndpoint != "")
}

// KubeToken returns the bearer token for a cluster endpoint, if one is
// configured. This is used in dev mode to bypass exec-based auth (aws eks
// get-token) and instead use a static token that works with local clusters.
func (d *DevEndpoints) KubeToken(endpoint string) (string, bool) {
	if d == nil || len(d.KubeTokens) == 0 {
		return "", false
	}
	tok, ok := d.KubeTokens[endpoint]
	return tok, ok
}

// Config holds all Rift runtime configuration loaded from config.yaml.
type Config struct {
	SSOStartURL        string            `yaml:"sso_start_url"`
	SSORegion          string            `yaml:"sso_region"`
	Regions            []string          `yaml:"regions"`
	NamespaceDefaults  map[string]string `yaml:"namespace_defaults"`
	DiscoverNamespaces bool              `yaml:"discover_namespaces"`

	// DevEndpoints optionally redirects AWS API calls to a local mock server.
	// Omit this field entirely for normal production use.
	DevEndpoints *DevEndpoints `yaml:"dev_endpoints,omitempty"`
}

func Default() Config {
	return Config{
		Regions:            append([]string(nil), defaultRegions...),
		NamespaceDefaults:  map[string]string{},
		DiscoverNamespaces: true,
	}
}

func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, configDirName, configFileName), nil
}

func DefaultStatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, configDirName, stateFileName), nil
}

func ResolvePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is empty")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return filepath.Abs(path)
}

func Load(path string) (Config, error) {
	cfg := Default()
	resolved, err := ResolvePath(path)
	if err != nil {
		return cfg, err
	}
	bytes, err := os.ReadFile(resolved)
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(bytes, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	resolved, err := ResolvePath(path)
	if err != nil {
		return err
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(resolved, data, 0o644); err != nil {
		return err
	}
	return nil
}

func (c *Config) Normalize() {
	if len(c.Regions) == 0 {
		c.Regions = append([]string(nil), defaultRegions...)
	}
	seen := map[string]struct{}{}
	regions := make([]string, 0, len(c.Regions))
	for _, region := range c.Regions {
		region = strings.TrimSpace(strings.ToLower(region))
		if region == "" {
			continue
		}
		if _, ok := seen[region]; ok {
			continue
		}
		seen[region] = struct{}{}
		regions = append(regions, region)
	}
	sort.Strings(regions)
	if len(regions) == 0 {
		regions = append([]string(nil), defaultRegions...)
	}
	c.Regions = regions
	if c.NamespaceDefaults == nil {
		c.NamespaceDefaults = map[string]string{}
	}
	normalized := make(map[string]string, len(c.NamespaceDefaults))
	for k, v := range c.NamespaceDefaults {
		key := strings.TrimSpace(strings.ToLower(k))
		if key == "" {
			continue
		}
		normalized[key] = strings.TrimSpace(v)
	}
	c.NamespaceDefaults = normalized
	c.SSOStartURL = strings.TrimSpace(c.SSOStartURL)
	c.SSORegion = strings.TrimSpace(strings.ToLower(c.SSORegion))
}

func (c Config) Validate() error {
	// In dev mode, SSO credentials come from the mock server config rather than
	// a real SSO session, so sso_start_url and sso_region are not required.
	if !c.DevEndpoints.IsActive() {
		if c.SSOStartURL == "" {
			return errors.New("config missing sso_start_url")
		}
		if c.SSORegion == "" {
			return errors.New("config missing sso_region")
		}
	}
	if len(c.Regions) == 0 {
		return errors.New("config missing regions")
	}
	return nil
}

func (c Config) NamespaceForEnv(env string) string {
	key := strings.ToLower(strings.TrimSpace(env))
	if key == "" {
		return ""
	}
	if value := strings.TrimSpace(c.NamespaceDefaults[key]); value != "" {
		return value
	}
	if key == "staging" {
		return strings.TrimSpace(c.NamespaceDefaults["stg"])
	}
	if key == "stg" {
		return strings.TrimSpace(c.NamespaceDefaults["staging"])
	}
	return ""
}
