#!/usr/bin/env bash
#
# setup-clusters.sh — creates k3d clusters for local Rift testing.
#
# This script:
#   1. Creates 3 k3d clusters (rift-dev, rift-staging, rift-prod) with fixed API ports.
#   2. Creates sample namespaces (app, monitoring, logging) in each cluster.
#   3. Creates a ServiceAccount with cluster-admin privileges and extracts a long-lived token.
#   4. Generates tools/mockaws/topology.yaml with real endpoints, CA certs, and ports.
#   5. Generates tools/mockaws/dev-config.yaml with the kube_tokens map.
#   6. Copies dev-config.yaml to ~/.config/rift/config.yaml.
#
# Prerequisites: k3d, kubectl, docker
#
# Usage:
#   ./tools/k3d/setup-clusters.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
TOPOLOGY_FILE="$PROJECT_ROOT/tools/mockaws/topology.yaml"
DEV_CONFIG_FILE="$PROJECT_ROOT/tools/mockaws/dev-config.yaml"
RIFT_CONFIG_DIR="$HOME/.config/rift"

# Cluster definitions — parallel arrays (bash 3 compatible)
CLUSTER_NAMES=(rift-dev rift-staging rift-prod)
CLUSTER_PORTS=(16443 16444 16445)
CLUSTER_ACCOUNT_IDS=("111111111111" "222222222222" "333333333333")
CLUSTER_ACCOUNT_NAMES=(dev staging production)

SAMPLE_NAMESPACES=(app monitoring logging)

# Lookup helpers by index
get_port()        { echo "${CLUSTER_PORTS[$1]}"; }
get_account_id()  { echo "${CLUSTER_ACCOUNT_IDS[$1]}"; }
get_account_name(){ echo "${CLUSTER_ACCOUNT_NAMES[$1]}"; }

# ── Prereq checks ────────────────────────────────────────────────────────────

check_prereqs() {
  local missing=()
  for cmd in k3d kubectl docker; do
    if ! command -v "$cmd" &>/dev/null; then
      missing+=("$cmd")
    fi
  done
  if [[ ${#missing[@]} -gt 0 ]]; then
    echo "ERROR: missing prerequisites: ${missing[*]}" >&2
    exit 1
  fi
  if ! docker info &>/dev/null; then
    echo "ERROR: docker daemon is not running" >&2
    exit 1
  fi
}

# ── Cluster creation ─────────────────────────────────────────────────────────

create_cluster() {
  local name=$1 port=$2
  if k3d cluster list -o json 2>/dev/null | grep -q "\"$name\""; then
    echo "  cluster $name already exists, deleting..."
    k3d cluster delete "$name" 2>/dev/null || true
  fi
  echo "  creating cluster $name on port $port..."
  k3d cluster create "$name" \
    --api-port "127.0.0.1:${port}" \
    --no-lb \
    --wait \
    --timeout 120s
}

# ── Namespace creation ───────────────────────────────────────────────────────

create_namespaces() {
  local name=$1
  kubectl config use-context "k3d-${name}" >/dev/null
  for ns in "${SAMPLE_NAMESPACES[@]}"; do
    kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
    # Deploy a small pod so k9s has something to show
    kubectl apply -n "$ns" -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: demo-${ns}
  labels:
    app: demo
    component: ${ns}
spec:
  containers:
    - name: worker
      image: busybox:1.36
      command: ["sleep", "infinity"]
      resources:
        limits:
          memory: "16Mi"
          cpu: "10m"
EOF
  done
}

# ── ServiceAccount + token extraction ────────────────────────────────────────

setup_service_account() {
  local name=$1
  kubectl config use-context "k3d-${name}" >/dev/null

  # Create ServiceAccount
  kubectl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: rift-admin
  namespace: kube-system
EOF

  # Create ClusterRoleBinding
  kubectl apply -f - >/dev/null <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: rift-admin-binding
subjects:
  - kind: ServiceAccount
    name: rift-admin
    namespace: kube-system
roleRef:
  kind: ClusterRole
  name: cluster-admin
  apiGroup: rbac.authorization.k8s.io
EOF

  # Create a long-lived token secret
  kubectl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: rift-admin-token
  namespace: kube-system
  annotations:
    kubernetes.io/service-account.name: rift-admin
type: kubernetes.io/service-account-token
EOF

  # Wait for the token to be populated
  local attempt
  for attempt in $(seq 1 30); do
    local token
    token=$(kubectl get secret rift-admin-token -n kube-system -o jsonpath='{.data.token}' 2>/dev/null || true)
    if [[ -n "$token" ]]; then
      return
    fi
    sleep 1
  done
  echo "ERROR: timed out waiting for token in cluster $name" >&2
  exit 1
}

get_token() {
  local name=$1
  kubectl config use-context "k3d-${name}" >/dev/null
  kubectl get secret rift-admin-token -n kube-system -o jsonpath='{.data.token}' | base64 -d
}

get_ca_cert() {
  local name=$1
  kubectl config use-context "k3d-${name}" >/dev/null
  kubectl get secret rift-admin-token -n kube-system -o jsonpath='{.data.ca\.crt}'
}

# ── Config generation ────────────────────────────────────────────────────────

generate_topology() {
  echo "generating $TOPOLOGY_FILE..."

  cat > "$TOPOLOGY_FILE" <<'HEADER'
# topology.yaml — defines the fake AWS accounts, roles, and EKS clusters
# served by the mockaws development server.
#
# Auto-generated by tools/k3d/setup-clusters.sh — do not edit manually.
#
# Start the server with:
#   go run ./tools/mockaws --topology ./tools/mockaws/topology.yaml

accounts:
  - id: "111111111111"
    name: dev
    roles:
      - name: DeveloperAccess
      - name: ReadOnlyAccess

  - id: "222222222222"
    name: staging
    roles:
      - name: DeveloperAccess
      - name: ReadOnlyAccess

  - id: "333333333333"
    name: production
    roles:
      - name: ReadOnlyAccess

clusters:
HEADER

  local idx
  for idx in 0 1 2; do
    local name="${CLUSTER_NAMES[$idx]}"
    local port="${CLUSTER_PORTS[$idx]}"
    local account_id="${CLUSTER_ACCOUNT_IDS[$idx]}"
    local env_name="${CLUSTER_ACCOUNT_NAMES[$idx]}"
    local ca_cert
    ca_cert=$(get_ca_cert "$name")

    # 3 clusters per account
    local i
    for i in 1 2 3; do
      local cluster_name="${env_name}-us-east-1-cluster-${i}"
      cat >> "$TOPOLOGY_FILE" <<EOF
  - account_id: "${account_id}"
    name: ${cluster_name}
    region: us-east-1
    endpoint: https://127.0.0.1:${port}
    certificate_base64: ${ca_cert}
EOF
    done
  done
}

generate_dev_config() {
  echo "generating $DEV_CONFIG_FILE..."

  cat > "$DEV_CONFIG_FILE" <<'HEADER'
# dev-config.yaml — Rift configuration for local development with mockaws + k3d.
#
# Auto-generated by tools/k3d/setup-clusters.sh — do not edit manually.
#
# First start the mock server in a separate terminal:
#   go run ./tools/mockaws --topology ./tools/mockaws/topology.yaml
#
# Then run Rift normally:
#   rift sync --config ./tools/mockaws/dev-config.yaml

sso_start_url: http://localhost:8080
sso_region: us-east-1

regions:
  - us-east-1

discover_namespaces: true

dev_endpoints:
  sso_endpoint: http://localhost:8080
  eks_endpoint: http://localhost:8080
  access_token: dev-token
  kube_tokens:
HEADER

  local idx
  for idx in 0 1 2; do
    local name="${CLUSTER_NAMES[$idx]}"
    local port="${CLUSTER_PORTS[$idx]}"
    local token
    token=$(get_token "$name")
    echo "    \"https://127.0.0.1:${port}\": \"${token}\"" >> "$DEV_CONFIG_FILE"
  done
}

install_config() {
  echo "installing config to $RIFT_CONFIG_DIR/config.yaml..."
  mkdir -p "$RIFT_CONFIG_DIR"
  cp "$DEV_CONFIG_FILE" "$RIFT_CONFIG_DIR/config.yaml"
}

# ── Main ─────────────────────────────────────────────────────────────────────

main() {
  echo "=== Rift k3d cluster setup ==="
  echo ""

  check_prereqs

  echo "creating clusters..."
  local idx
  for idx in 0 1 2; do
    create_cluster "${CLUSTER_NAMES[$idx]}" "${CLUSTER_PORTS[$idx]}"
  done

  echo ""
  echo "creating namespaces..."
  for idx in 0 1 2; do
    local name="${CLUSTER_NAMES[$idx]}"
    echo "  $name: ${SAMPLE_NAMESPACES[*]}"
    create_namespaces "$name"
  done

  echo ""
  echo "setting up service accounts..."
  for idx in 0 1 2; do
    local name="${CLUSTER_NAMES[$idx]}"
    echo "  $name"
    setup_service_account "$name"
  done

  echo ""
  generate_topology
  generate_dev_config
  install_config

  echo ""
  echo "=== setup complete ==="
  echo ""
  echo "clusters:"
  for idx in 0 1 2; do
    echo "  ${CLUSTER_NAMES[$idx]} -> https://127.0.0.1:${CLUSTER_PORTS[$idx]}"
  done
  echo ""
  echo "next steps:"
  echo "  1. make mock       (in a separate terminal)"
  echo "  2. make dev-sync"
  echo "  3. ./rift ui       (then press 'k' for k9s)"
}

main "$@"
