// Package config reads the service's environment into one struct.
//
// The governing rule of this file: every external dependency is optional. An
// unset variable is not an error, it selects a fallback — the JSON file store,
// a no-op cache, in-process search, in-process scoring, no event ingest. That
// is what keeps `go run ./cmd/server` working with no infrastructure at all,
// keeps the tests runnable in CI without containers, and makes the whole stack
// additive rather than a precondition.
//
// The second rule: resolution is reported, never guessed at. Summary() prints
// exactly which implementation each dependency resolved to, so the one thing a
// reader of the boot log cannot be confused about is whether they are looking
// at real infrastructure or a fallback.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	Addr     string
	LogLevel string
	Seed     bool

	// IdentityKey is the HMAC key for document hashes. Rotating it orphans
	// every stored hash, so it is configuration, never a literal in code.
	IdentityKey       []byte
	IdentityKeyIsDev  bool

	Postgres PostgresConfig
	DataPath string // file store; used only when Postgres is not configured

	Redis    RedisConfig
	Elastic  ElasticConfig
	MQTT     MQTTConfig
	Scoring  ScoringConfig
	GraphQL  GraphQLConfig
}

// PostgresConfig is empty-DSN-means-disabled.
type PostgresConfig struct {
	DSN      string
	MaxConns int32
	Migrate  bool
}

func (c PostgresConfig) Enabled() bool { return c.DSN != "" }

// RedisConfig is empty-Addr-means-disabled.
type RedisConfig struct {
	Addr     string
	Password string
	DB       int
	TTL      time.Duration
}

func (c RedisConfig) Enabled() bool { return c.Addr != "" }

// ElasticConfig is empty-URL-means-disabled.
type ElasticConfig struct {
	URL      string
	Index    string
	Username string
	Password string
}

func (c ElasticConfig) Enabled() bool { return c.URL != "" }

// MQTTConfig is empty-URL-means-disabled.
type MQTTConfig struct {
	URL      string
	ClientID string
	Topic    string
	Username string
	Password string
}

func (c MQTTConfig) Enabled() bool { return c.URL != "" }

// ScoringConfig selects between the gRPC scoring service and linking the pure
// function directly. Empty target means in-process.
type ScoringConfig struct {
	GRPCTarget string
	Timeout    time.Duration
}

func (c ScoringConfig) Remote() bool { return c.GRPCTarget != "" }

// GraphQLConfig toggles the GraphQL surface. GraphiQL ships the explorer UI and
// belongs off in production: it is an unauthenticated introspection console.
type GraphQLConfig struct {
	Enabled  bool
	GraphiQL bool
}

// devIdentityKey matches the api package's development default. Sharing the
// literal would couple the packages; duplicating it with this comment is the
// lesser evil, and the mismatch would be caught immediately because every
// stored hash would stop resolving.
const devIdentityKey = "guest-score-development-identity-key"

// Load reads the environment. It never fails on a missing optional value; the
// only error it can return is a malformed one, because silently falling back
// after someone deliberately set a variable would hide their typo.
func Load() (Config, error) {
	c := Config{
		Addr:     env("GS_ADDR", ":8090"),
		LogLevel: env("GS_LOG_LEVEL", "info"),
		DataPath: env("GS_DATA_PATH", "./data/store.json"),
	}

	var err error
	if c.Seed, err = envBool("GS_SEED", true); err != nil {
		return c, err
	}

	key := os.Getenv("GS_IDENTITY_KEY")
	if key == "" {
		key = devIdentityKey
		c.IdentityKeyIsDev = true
	}
	c.IdentityKey = []byte(key)

	c.Postgres.DSN = os.Getenv("GS_POSTGRES_DSN")
	if c.Postgres.MaxConns, err = envInt32("GS_POSTGRES_MAX_CONNS", 10); err != nil {
		return c, err
	}
	if c.Postgres.Migrate, err = envBool("GS_MIGRATE", true); err != nil {
		return c, err
	}

	c.Redis.Addr = os.Getenv("GS_REDIS_ADDR")
	c.Redis.Password = os.Getenv("GS_REDIS_PASSWORD")
	if c.Redis.DB, err = envInt("GS_REDIS_DB", 0); err != nil {
		return c, err
	}
	if c.Redis.TTL, err = envDuration("GS_CACHE_TTL", 60*time.Second); err != nil {
		return c, err
	}

	c.Elastic.URL = strings.TrimRight(os.Getenv("GS_ELASTIC_URL"), "/")
	c.Elastic.Index = env("GS_ELASTIC_INDEX", "guest-score-guests")
	c.Elastic.Username = os.Getenv("GS_ELASTIC_USERNAME")
	c.Elastic.Password = os.Getenv("GS_ELASTIC_PASSWORD")

	c.MQTT.URL = os.Getenv("GS_MQTT_URL")
	c.MQTT.ClientID = env("GS_MQTT_CLIENT_ID", "guest-score-ingest")
	c.MQTT.Topic = env("GS_MQTT_TOPIC", "guestscore/+/events")
	c.MQTT.Username = os.Getenv("GS_MQTT_USERNAME")
	c.MQTT.Password = os.Getenv("GS_MQTT_PASSWORD")

	c.Scoring.GRPCTarget = os.Getenv("GS_SCORING_GRPC")
	if c.Scoring.Timeout, err = envDuration("GS_SCORING_TIMEOUT", 3*time.Second); err != nil {
		return c, err
	}

	if c.GraphQL.Enabled, err = envBool("GS_GRAPHQL", true); err != nil {
		return c, err
	}
	if c.GraphQL.GraphiQL, err = envBool("GS_GRAPHIQL", true); err != nil {
		return c, err
	}

	return c, nil
}

// Summary returns one key-value pair per dependency naming the implementation
// that was selected. main logs these at boot; between them they answer every
// "why is it behaving like that?" question the stack can raise.
func (c Config) Summary() []any {
	pick := func(on bool, yes, no string) string {
		if on {
			return yes
		}
		return no
	}
	return []any{
		"addr", c.Addr,
		"store", pick(c.Postgres.Enabled(), "postgres", "file:"+c.DataPath),
		"cache", pick(c.Redis.Enabled(), "redis:"+c.Redis.Addr, "disabled"),
		"search", pick(c.Elastic.Enabled(), "elasticsearch:"+c.Elastic.URL, "in-process"),
		"events", pick(c.MQTT.Enabled(), "mqtt:"+c.MQTT.URL, "disabled"),
		"scoring", pick(c.Scoring.Remote(), "grpc:"+c.Scoring.GRPCTarget, "in-process"),
		"graphql", pick(c.GraphQL.Enabled, pick(c.GraphQL.GraphiQL, "enabled+graphiql", "enabled"), "disabled"),
	}
}

// Warnings lists configuration that is safe for a demo and unsafe in
// production. Returning them rather than logging here keeps the package free of
// a logger dependency and lets main decide the severity.
func (c Config) Warnings() []string {
	var w []string
	if c.IdentityKeyIsDev {
		w = append(w, "GS_IDENTITY_KEY is unset, using the development key. "+
			"Every identity hash is derived from it, so setting it later orphans "+
			"all stored documents. In production it belongs in a KMS, not an env var.")
	}
	if c.GraphQL.GraphiQL {
		w = append(w, "GraphiQL is enabled. It is an unauthenticated introspection "+
			"console; set GS_GRAPHIQL=false outside development.")
	}
	if c.Elastic.Enabled() && c.Elastic.Username == "" {
		w = append(w, "Elasticsearch is configured without credentials. That is the "+
			"local single-node posture only; enable xpack.security in any shared deployment.")
	}
	if c.MQTT.Enabled() && c.MQTT.Username == "" {
		w = append(w, "MQTT is configured without credentials, so the broker is "+
			"accepting anonymous publishers. Any client that can reach it can file "+
			"an incident against a guest.")
	}
	return w
}

// --- env helpers -------------------------------------------------------------

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("%s=%q: want a boolean: %w", key, v, err)
	}
	return b, nil
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def, fmt.Errorf("%s=%q: want an integer: %w", key, v, err)
	}
	return n, nil
}

func envInt32(key string, def int32) (int32, error) {
	n, err := envInt(key, int(def))
	return int32(n), err
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def, fmt.Errorf("%s=%q: want a duration like 60s or 15m: %w", key, v, err)
	}
	return d, nil
}
