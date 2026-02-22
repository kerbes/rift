BINARY ?= rift

.PHONY: build test lint fmt dev-clusters dev-teardown mock dev-sync dev

build:
	go build -o $(BINARY) ./cmd/rift

test:
	go test ./...

lint:
	go vet ./...

fmt:
	gofmt -w $$(rg --files -g '*.go')

# ── Local dev targets ────────────────────────────────────────────────────────

dev-clusters:
	./tools/k3d/setup-clusters.sh

dev-teardown:
	./tools/k3d/teardown-clusters.sh

mock:
	@PORT=8080; \
	EXISTING_PID=$$(lsof -ti :$$PORT 2>/dev/null); \
	if [ -n "$$EXISTING_PID" ]; then \
		echo "mockaws is already running on port $$PORT (PID $$EXISTING_PID)."; \
		printf "Kill it and restart? [y/N] "; \
		read answer; \
		case "$$answer" in \
			[Yy]*) kill "$$EXISTING_PID"; sleep 1 ;; \
			*) echo "Leaving existing instance running."; exit 0 ;; \
		esac; \
	fi; \
	go run ./tools/mockaws --topology ./tools/mockaws/topology.yaml

dev-sync: build
	./$(BINARY) sync --config ./tools/mockaws/dev-config.yaml

dev: build dev-clusters mock-bg dev-sync test
	@echo ""
	@echo "=== dev environment ready ==="
	@echo "  mock server running in background (kill with: make mock-stop)"
	@echo "  run: ./$(BINARY) ui"

mock-bg:
	@PORT=8080; \
	EXISTING_PID=$$(lsof -ti :$$PORT 2>/dev/null); \
	if [ -n "$$EXISTING_PID" ]; then \
		echo "  stopping existing mock server (PID $$EXISTING_PID)..."; \
		kill "$$EXISTING_PID" 2>/dev/null || true; \
		sleep 1; \
	fi
	@echo "  starting mock server in background..."
	@go run ./tools/mockaws --topology ./tools/mockaws/topology.yaml &
	@for i in $$(seq 1 30); do \
		if curl -s -o /dev/null http://localhost:8080/ 2>/dev/null; then \
			echo "  mock server is ready"; \
			break; \
		fi; \
		if [ "$$i" -eq 30 ]; then \
			echo "ERROR: mock server did not start in 30s" >&2; \
			exit 1; \
		fi; \
		sleep 1; \
	done

mock-stop:
	@PID=$$(lsof -ti :8080 2>/dev/null); \
	if [ -n "$$PID" ]; then \
		kill "$$PID" && echo "stopped mock server (PID $$PID)"; \
	else \
		echo "no mock server running on port 8080"; \
	fi
