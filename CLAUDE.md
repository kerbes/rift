# Rift

AWS IAM Identity Center (SSO) + EKS context orchestration CLI.

See [AGENTS.md](AGENTS.md) for full project docs, architecture, and development checklist.

## Commands

- Build: `make build` or `go build -o rift ./cmd/rift`
- Test: `make test` or `go test ./...`
- Lint: `make lint` (`go vet ./...`)
- Format: `make fmt` (uses `gofmt`)

## Before Completing Any Change

Run all three in order:
```
gofmt -w <touched files>
go test ./...
go build ./cmd/rift
```

## Code Style

- Explicit error returns — no panics in normal flow
- Wrap errors: `fmt.Errorf("context: %w", err)`
- Early returns on error
- Sorted, deterministic output
- `log/slog` for structured logging

## Safety Rules (Critical)

- Only manage resources prefixed `rift-` in `~/.aws/config` and `~/.kube/config`
- Never touch non-rift profiles, contexts, clusters, or users
- `--dry-run` must never write files

## Key Naming Patterns

- AWS profile: `rift-<env>-<account-slug>-<role-slug>`
- kube context: `rift-<env>-<account-slug>-<cluster-slug>`
- Env values: `prod`, `staging`, `dev`, `int`, `other`
- `stg` is displayed in the UI as alias for `staging` to avoid truncation

## Runtime Files

- Config: `~/.config/rift/config.yaml`
- State: `~/.config/rift/state.json`
- Managed: `~/.aws/config`, `~/.kube/config` (or first `KUBECONFIG` path)

## Known Gotchas

- Search filter state lives in `m.search.Value()` — clear that value and re-run `applyFilter()` when clearing filter
- Call `syncTableLayout()` before table update events to prevent cursor drift
- Keep both modern and legacy fallback paths in `auth` (AWS CLI behavior differs by version)
- Namespace discovery is best-effort — do not fail sync solely due to per-cluster namespace errors
- For wide bordered UI blocks: wrap content before applying border/style to avoid right-edge clipping
