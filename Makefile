.PHONY: help dev dev-be dev-fe dev-scoring test test-int lint build build-be build-fe build-scoring run docker clean reseed proto sim graphql-schema

# Every external dependency is optional. `make dev` needs nothing installed
# beyond Go and Node; it runs on the JSON file store with in-process scoring.
# To run against the real stack, start ../dev-stack and export its variables —
# see docs/PLATFORM.md.

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

dev: ## Run backend (:8090) and frontend (:5173) together, with seeded demo data
	@echo "Guest Score → http://localhost:5173   GraphiQL → http://localhost:8090/graphiql"
	@trap 'kill 0' EXIT; \
	(cd backend && GS_ADDR=:8090 go run ./cmd/server) & \
	(cd frontend && npm run dev) & \
	wait

dev-be: ## Run the backend only
	cd backend && GS_ADDR=:8090 go run ./cmd/server

dev-fe: ## Run the frontend only
	cd frontend && npm run dev

dev-scoring: ## Run the gRPC scoring service on :9090
	cd backend && go run ./cmd/scoringd

dev-split: ## Run the API against the gRPC scoring service, as the stack deploys it
	@trap 'kill 0' EXIT; \
	(cd backend && go run ./cmd/scoringd) & \
	sleep 2; \
	(cd backend && GS_ADDR=:8090 GS_SCORING_GRPC=localhost:9090 go run ./cmd/server) & \
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

build-fe:
	cd frontend && npm ci && npm run build

build-be:
	cd backend && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ../bin/guest-score ./cmd/server

build-scoring:
	cd backend && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ../bin/scoringd ./cmd/scoringd

run: build ## Build everything, then serve API and SPA from the one binary on :8090
	GS_ADDR=:8090 ./bin/guest-score -static ./frontend/dist

sim: ## Publish a simulated property incident over MQTT; needs ../dev-stack up
	cd backend && go run ./cmd/propertysim -guest $(GUEST) -type incident -severity moderate

reseed: ## Wipe stored data and regenerate the demo dataset
	cd backend && go run ./cmd/server -reseed

docker: ## Build all three images locally
	docker build -t guest-score-api:dev -f Dockerfile .
	docker build -t guest-score-scoring:dev -f Dockerfile.scoring .
	docker build -t guest-score-web:dev -f Dockerfile.web .

clean:
	rm -rf bin frontend/dist backend/data
