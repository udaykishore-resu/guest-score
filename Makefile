.PHONY: help dev dev-be dev-fe test lint build build-be build-fe run docker clean reseed

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

dev: ## Run backend (:8080) and frontend (:5173) together, with seeded demo data
	@echo "Guest Score → http://localhost:5173"
	@trap 'kill 0' EXIT; \
	(cd backend && go run ./cmd/server) & \
	(cd frontend && npm run dev) & \
	wait

dev-be: ## Run the backend only
	cd backend && go run ./cmd/server

dev-fe: ## Run the frontend only
	cd frontend && npm run dev

test: ## Run backend tests with the race detector and coverage
	cd backend && go test ./... -race -cover

lint: ## Vet the backend and type-check the frontend
	cd backend && go vet ./...
	cd frontend && npm run typecheck

build: build-fe build-be ## Build a single binary that serves the API and the SPA

build-fe:
	cd frontend && npm ci && npm run build

build-be:
	cd backend && CGO_ENABLED=0 go build -ldflags="-s -w" -o ../bin/guest-score ./cmd/server

run: build ## Build everything, then serve it from the one binary on :8080
	./bin/guest-score -static ./frontend/dist

reseed: ## Wipe stored data and regenerate the demo dataset
	cd backend && go run ./cmd/server -reseed

docker:
	docker build -t guest-score . && docker run --rm -p 8080:8080 guest-score

clean:
	rm -rf bin frontend/dist backend/data
