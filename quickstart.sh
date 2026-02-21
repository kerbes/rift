#!/usr/bin/env bash
#
# quickstart.sh — single-script quickstart for Rift local development.
#
# Runs the full flow:
#   1. Build rift
#   2. Create k3d clusters
#   3. Start mock AWS server (background)
#   4. Run rift sync
#   5. Run tests
#   6. (Optional) Launch TUI
#
# On exit the mock server is stopped and clusters are torn down
# unless --no-teardown is passed.
#
# Usage:
#   ./quickstart.sh              # full run, teardown on exit
#   ./quickstart.sh --no-teardown  # keep clusters and mock server running
#   ./quickstart.sh --skip-ui     # skip TUI prompt (default in non-interactive)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MOCK_PID=""
NO_TEARDOWN=false
SKIP_UI=false

for arg in "$@"; do
  case "$arg" in
    --no-teardown) NO_TEARDOWN=true ;;
    --skip-ui)    SKIP_UI=true ;;
    *)            echo "unknown flag: $arg"; exit 1 ;;
  esac
done

# ── Cleanup ───────────────────────────────────────────────────────────────────

cleanup() {
  echo ""
  echo "=== cleanup ==="

  if [[ -n "$MOCK_PID" ]] && kill -0 "$MOCK_PID" 2>/dev/null; then
    echo "  stopping mock server (pid $MOCK_PID)..."
    kill "$MOCK_PID" 2>/dev/null || true
    wait "$MOCK_PID" 2>/dev/null || true
  fi

  if [[ "$NO_TEARDOWN" == true ]]; then
    echo "  --no-teardown: keeping k3d clusters"
  else
    echo "  tearing down k3d clusters..."
    "$SCRIPT_DIR/tools/k3d/teardown-clusters.sh"
  fi

  echo "=== done ==="
}

trap cleanup EXIT

# ── Prereq checks ────────────────────────────────────────────────────────────

echo "=== rift quickstart ==="
echo ""

echo "checking prerequisites..."
missing=()
for cmd in go docker k3d kubectl; do
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
echo "  all prerequisites found"
echo ""

# ── Step 1: Build ─────────────────────────────────────────────────────────────

echo "=== step 1: build ==="
go build -o "$SCRIPT_DIR/rift" ./cmd/rift
echo "  built ./rift"
echo ""

# ── Step 2: Create k3d clusters ──────────────────────────────────────────────

echo "=== step 2: create k3d clusters ==="
"$SCRIPT_DIR/tools/k3d/setup-clusters.sh"
echo ""

# ── Step 3: Start mock AWS server ────────────────────────────────────────────

echo "=== step 3: start mock AWS server ==="

# Kill any existing mock server on port 8080
if lsof -i :8080 -t &>/dev/null; then
  echo "  port 8080 in use, stopping existing process..."
  kill "$(lsof -i :8080 -t)" 2>/dev/null || true
  sleep 1
fi

go run ./tools/mockaws --topology ./tools/mockaws/topology.yaml &
MOCK_PID=$!
echo "  mock server started (pid $MOCK_PID)"

# Wait for mock server to be ready
echo "  waiting for mock server..."
for i in $(seq 1 30); do
  if curl -s -o /dev/null -w '' http://localhost:8080/ 2>/dev/null; then
    echo "  mock server is ready"
    break
  fi
  if ! kill -0 "$MOCK_PID" 2>/dev/null; then
    echo "ERROR: mock server exited unexpectedly" >&2
    MOCK_PID=""
    exit 1
  fi
  if [[ "$i" -eq 30 ]]; then
    echo "ERROR: mock server did not become ready in 30s" >&2
    exit 1
  fi
  sleep 1
done
echo ""

# ── Step 4: Run rift sync ────────────────────────────────────────────────────

echo "=== step 4: rift sync ==="
"$SCRIPT_DIR/rift" sync --config ./tools/mockaws/dev-config.yaml
echo ""

# ── Step 5: Run tests ────────────────────────────────────────────────────────

echo "=== step 5: run tests ==="
go test ./...
echo ""

# ── Step 6: Launch TUI (optional) ────────────────────────────────────────────

if [[ "$SKIP_UI" == true ]]; then
  echo "=== skipping TUI (--skip-ui) ==="
elif [[ ! -t 0 ]]; then
  echo "=== skipping TUI (non-interactive) ==="
else
  echo "=== step 6: launch TUI ==="
  echo "  press Enter to launch the TUI, or Ctrl-C to skip..."
  read -r
  "$SCRIPT_DIR/rift" ui
fi

echo ""
echo "=== quickstart complete ==="
