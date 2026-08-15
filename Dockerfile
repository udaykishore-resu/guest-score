# One image: the Go binary serves both the API and the built SPA, so there is
# no nginx sidecar, no CORS, and nothing to keep in sync between two services.

FROM node:22-alpine AS frontend
WORKDIR /fe
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.24-alpine AS backend
WORKDIR /be
# No third-party dependencies (see the constitution), so there is no
# `go mod download` step and no dependency layer to cache.
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/guest-score ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=backend /out/guest-score /app/guest-score
COPY --from=frontend /fe/dist /app/static

ENV ADDR=:8080 \
    STATIC_DIR=/app/static \
    DATA_PATH=/data/guest-score.json

EXPOSE 8080
VOLUME ["/data"]

USER nonroot:nonroot
ENTRYPOINT ["/app/guest-score"]
