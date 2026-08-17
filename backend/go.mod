module github.com/udaykishore-resu/guest-score/backend

go 1.24

// Constitution Principle II said "zero runtime dependencies", and for the core
// it still holds: domain, scoring, store (FileStore) and api import nothing but
// the standard library, and the whole service still runs with `go run` and no
// infrastructure at all.
//
// The dependencies below buy capabilities that cannot honestly be hand-rolled,
// and each is confined to one adapter package behind an interface:
//
//   pgx        internal/store/postgres  - the Postgres wire protocol
//   go-redis   internal/cache           - RESP, pooling, cluster-safe SCAN
//   paho.mqtt  internal/events          - MQTT 3.1.1 with QoS 1 redelivery
//   grpc+proto internal/scoringsvc      - the versioned scoring contract
//   graphql-go internal/graphqlapi      - schema execution and validation
//
// Deliberately NOT added: an Elasticsearch client. internal/search talks to the
// REST API with net/http, because the official client is an enormous generated
// surface and this needs four calls. See the comment at the top of elastic.go.
//
// Every one of these is optional at runtime. Unset the matching environment
// variable and the adapter is replaced by an in-process fallback, which is what
// keeps the tests container-free and the demo one command.
require (
	github.com/eclipse/paho.mqtt.golang v1.5.0
	github.com/graphql-go/graphql v0.8.1
	github.com/graphql-go/handler v0.2.4
	github.com/jackc/pgx/v5 v5.7.2
	github.com/redis/go-redis/v9 v9.7.0
	google.golang.org/grpc v1.67.1
	google.golang.org/protobuf v1.35.2
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/crypto v0.31.0 // indirect
	golang.org/x/net v0.32.0 // indirect
	golang.org/x/sync v0.10.0 // indirect
	golang.org/x/sys v0.28.0 // indirect
	golang.org/x/text v0.21.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20241206012308-a4fef0638583 // indirect
)
