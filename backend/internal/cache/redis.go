package cache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis is the shared cache. It exists so a second API replica does not warm a
// cold cache of its own, and so a restart does not.
type Redis struct {
	client *redis.Client
	log    *slog.Logger

	// namespace prefixes every key. Guest Score and MarketMate share one Redis
	// in the dev stack, separated by logical DB index; the namespace is the
	// second line of defence, and the one that survives someone consolidating
	// onto a single DB later.
	namespace string
}

// RedisOptions configures the client.
type RedisOptions struct {
	Addr      string
	Password  string
	DB        int
	Namespace string
	Log       *slog.Logger
}

// NewRedis connects and verifies the connection before returning.
//
// It fails rather than degrading, because reaching here means the operator
// explicitly set GS_REDIS_ADDR. Silently falling back to Nop after someone
// asked for Redis would hide a typo in a hostname until the day they wonder why
// the cache never seems to help. main decides whether that error is fatal.
func NewRedis(ctx context.Context, opts RedisOptions) (*Redis, error) {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Namespace == "" {
		opts.Namespace = "gs"
	}

	client := redis.NewClient(&redis.Options{
		Addr:         opts.Addr,
		Password:     opts.Password,
		DB:           opts.DB,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,

		// A cache must not become the bottleneck it was added to remove. Short
		// timeouts turn a struggling Redis into a stream of misses, which the
		// service already handles, instead of a stream of slow requests.
		PoolSize:     10,
		MinIdleConns: 2,
	})

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis %s: %w", opts.Addr, err)
	}

	return &Redis{client: client, log: opts.Log, namespace: opts.Namespace}, nil
}

func (r *Redis) key(k string) string { return r.namespace + ":" + k }

func (r *Redis) Get(ctx context.Context, key string) ([]byte, error) {
	b, err := r.client.Get(ctx, r.key(key)).Bytes()
	switch {
	case errors.Is(err, redis.Nil):
		return nil, ErrMiss
	case err != nil:
		// A broken cache is a miss. The alternative — surfacing the error — would
		// turn a Redis outage into an API outage, which is exactly backwards for
		// a component whose entire purpose is to be optional.
		r.log.Warn("cache read failed, treating as a miss", "key", key, "err", err)
		return nil, ErrMiss
	}
	return b, nil
}

func (r *Redis) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	if ttl <= 0 {
		// A cache entry with no expiry is a database row with extra steps, and
		// this cache holds derived values that a missed invalidation would
		// otherwise strand forever.
		ttl = time.Minute
	}
	if err := r.client.Set(ctx, r.key(key), val, ttl).Err(); err != nil {
		r.log.Warn("cache write failed", "key", key, "err", err)
	}
	return nil
}

func (r *Redis) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	full := make([]string, len(keys))
	for i, k := range keys {
		full[i] = r.key(k)
	}
	if err := r.client.Del(ctx, full...).Err(); err != nil {
		// Unlike a failed read, a failed invalidation is genuinely dangerous:
		// it leaves a stale score visible. Surface it so the caller can decide,
		// and so it shows up in logs rather than as a mysterious wrong number.
		return fmt.Errorf("cache delete: %w", err)
	}
	return nil
}

// DeletePrefix removes every key under a prefix using SCAN.
//
// SCAN rather than KEYS: KEYS is O(n) over the whole keyspace and blocks the
// single-threaded server for the duration, which on a shared Redis means it
// blocks MarketMate too. SCAN is incremental and cooperative, at the cost of
// only guaranteeing keys present for the whole iteration are returned — fine
// here, since a key added mid-scan is by definition newer than the write that
// triggered the invalidation.
func (r *Redis) DeletePrefix(ctx context.Context, prefix string) error {
	pattern := r.key(prefix) + "*"
	var cursor uint64
	for {
		keys, next, err := r.client.Scan(ctx, cursor, pattern, 256).Result()
		if err != nil {
			return fmt.Errorf("cache scan %q: %w", prefix, err)
		}
		if len(keys) > 0 {
			if err := r.client.Del(ctx, keys...).Err(); err != nil {
				return fmt.Errorf("cache delete %q: %w", prefix, err)
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

func (r *Redis) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

func (r *Redis) Close() error { return r.client.Close() }
