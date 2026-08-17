# Guest Score API.
#
# This image is the API service only. The SPA is built separately by
# Dockerfile.web, because in the platform stack nginx serves it and terminates
# the ingress. `make run` in this repo still produces the single binary that
# serves both, which is the right shape for a one-container deploy — the two
# layouts share the same binary and differ only in whether STATIC_DIR is set.

FROM golang:1.24-alpine AS build
WORKDIR /src

# Dependencies first so a source-only change reuses the module layer. Since the
# integration adapters landed, this repo does have dependencies — the core still
# does not, but pgx, go-redis, paho, grpc and graphql-go are real downloads and
# worth caching.
COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./
# Trimpath keeps build-machine paths out of panics; the ldflags drop the symbol
# table and DWARF, which is most of the binary size.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/guest-score ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/guest-score /app/guest-score

ENV GS_ADDR=:8090 \
    GS_DATA_PATH=/data/store.json

EXPOSE 8090
VOLUME ["/data"]

# Distroless has no shell and no curl, so the binary probes itself. Compose and
# Kubernetes both point at this.
HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/app/guest-score", "-health", "http://127.0.0.1:8090/api/health"]

USER nonroot:nonroot
ENTRYPOINT ["/app/guest-score"]
