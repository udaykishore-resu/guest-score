.PHONY: help dev dev-be dev-fe dev-scoring dev-split test test-int lint build build-be build-fe build-scoring run docker clean reseed proto sim stop preflight deps

# Every external dependency is optional. `make dev` needs nothing installed
# beyond Go and Node; it runs on the JSON file store with in-process scoring.
# To run against the real stack, start ../dev-stack and export its variables —
# see docs/PLATFORM.md.

# Override either to run beside something already using these ports:
#   make dev API_PORT=8091 WEB_PORT=5174
API_PORT ?= 8090
WEB_PORT ?= 5173

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

deps: frontend/node_modules ## Install frontend dependencies if they are missing

# A directory target with the lockfile as its prerequisite: npm ci runs on a
# fresh clone and whenever the lockfile changes, and is skipped otherwise. This
# is why `make dev` works immediately after `git clone` — it did not before, and
# the failure was an opaque "Cannot find package 'vite'".
frontend/node_modules: frontend/package-lock.json frontend/package.json
	@echo "installing frontend dependencies (this runs once)"
	cd frontend && npm ci
	@touch frontend/node_modules

preflight: ## Fail early and legibly if a port is already taken
	@for p in $(API_PORT) $(WEB_PORT); do \
		if command -v lsof >/dev/null 2>&1 && lsof -ti tcp:$$p >/dev/null 2>&1; then \
			echo ""; \
			echo "  Port $$p is already in use:"; \
			lsof -i tcp:$$p | sed 's/^/    /'; \
			echo ""; \
			echo "  Stop it with:   make stop"; \
			echo "  Or move ports:  make dev API_PORT=8091 WEB_PORT=5174"; \
			echo ""; \
			exit 1; \
		fi; \
	done

stop: ## Stop anything this project left listening on its ports
	@for p in $(API_PORT) $(WEB_PORT) 9090; do \
		pids=$$(lsof -ti tcp:$$p 2>/dev/null); \
		if [ -n "$$pids" ]; then \
			echo "stopping $$pids on port $$p"; \
			kill $$pids 2>/dev/null || true; \
		fi; \
	done; \
	echo "ports clear"

dev: preflight deps ## Run backend and frontend together, with seeded demo data
	@echo ""
	@echo "  Guest Score  → http://localhost:$(WEB_PORT)"
	@echo "  API          → http://localhost:$(API_PORT)/api/health"
	@echo "  GraphiQL     → http://localhost:$(API_PORT)/graphiql"
	@echo ""
	@trap 'kill 0' EXIT; \
	(cd backend && GS_ADDR=:$(API_PORT) go run ./cmd/server) & \
	(cd frontend && API_PORT=$(API_PORT) WEB_PORT=$(WEB_PORT) npm run dev) & \
	wait

dev-be: ## Run the backend only
	cd backend && GS_ADDR=:$(API_PORT) go run ./cmd/server

dev-fe: deps ## Run the frontend only
	cd frontend && API_PORT=$(API_PORT) WEB_PORT=$(WEB_PORT) npm run dev

dev-scoring: ## Run the gRPC scoring service on :9090
	cd backend && go run ./cmd/scoringd

dev-split: preflight ## Run the API against the gRPC scoring service, as the stack deploys it
	@trap 'kill 0' EXIT; \
	(cd backend && go run ./cmd/scoringd) & \
	sleep 2; \
	(cd backend && GS_ADDR=:$(API_PORT) GS_SCORING_GRPC=localhost:9090 go run ./cmd/server) & \
	wait

test: ## Run backend tests with the race detector and coverage
	cd backend && go test ./... -race -cover

test-int: ## Run the integration tests too; needs ../dev-stack up
	cd backend && GS_TEST_POSTGRES_DSN=postgres://guestscore:guestscore@localhost:5432/guestscore?sslmode=disable \
		GS_TEST_REDIS_ADDR=localhost:6379 \
		GS_TEST_ELASTIC_URL=http://localhost:9200 \
		go test ./... -race -count=1

lint: ## Vet the backend and type-check the frontend
	cd backend && go vet ./...
	cd frontend && npm run typecheck

proto: ## Regenerate the gRPC stubs from proto/scoring/v1/scoring.proto
	cd backend && protoc --proto_path=proto \
		--go_out=. --go_opt=module=github.com/udaykishore-resu/guest-score/backend \
		--go-grpc_out=. --go-grpc_opt=module=github.com/udaykishore-resu/guest-score/backend \
		proto/scoring/v1/scoring.proto
	@echo "regenerated internal/gen/scoringv1"

build: build-fe build-be build-scoring ## Build the API, the scoring service and the SPA

build-fe: deps
	cd frontend && npm run build

build-be:
	cd backend && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ../bin/guest-score ./cmd/server

build-scoring:
	cd backend && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ../bin/scoringd ./cmd/scoringd

run: build ## Build everything, then serve API and SPA from the one binary
	GS_ADDR=:$(API_PORT) ./bin/guest-score -static ./frontend/dist

sim: ## Publish a simulated property incident over MQTT; needs ../dev-stack up
	cd backend && go run ./cmd/propertysim -guest $(GUEST) -type incident -severity moderate

reseed: ## Wipe stored data and regenerate the demo dataset
	cd backend && go run ./cmd/server -reseed

docker: ## Build all three images locally
	docker build -t guest-score-api:dev -f Dockerfile .
	docker build -t guest-score-scoring:dev -f Dockerfile.scoring .
	docker build -t guest-score-web:dev -f Dockerfile.web .

clean: ## Remove build output and local data
	rm -rf bin frontend/dist backend/data
