// Package cache fronts the expensive part of a directory read.
//
// Scoring is pure and fast, but a directory page recomputes it for every guest
// from their full review history, and /api/stats does it for the entire
// population on every request. That is the read amplification worth caching —
// not the store, the derivation.
//
// The interface is deliberately narrow (get, set, delete by prefix) because a
// cache that can do more invites callers to treat it as a database. Anything in
// here can vanish at any moment and the service must still be correct.
package cache

import (
	"context"
	"errors"
	"time"
)

// ErrMiss is returned by Get when the key is absent. It is a normal outcome,
// not a failure, and callers must treat it as one.
var ErrMiss = errors.New("cache miss")

// Cache is the contract. Implementations must be safe for concurrent use.
//
// Every method takes a context because the Redis implementation makes a network
// call, and a cache lookup must never outlive the request that wanted it: a
// slow cache has to degrade into a miss, not into a slow response.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) error

	// DeletePrefix invalidates a whole family of keys. Writing one review
	// invalidates that guest's score, the directory page, and the population
	// stats — enumerating those keys at every call site would guarantee one is
	// eventually forgotten.
	DeletePrefix(ctx context.Context, prefix string) error

	// Ping reports whether the backend is reachable, for /api/health.
	Ping(ctx context.Context) error

	Close() error
}

// Nop is the cache used when GS_REDIS_ADDR is unset: every read misses, every
// write succeeds and is discarded.
//
// This is the reason no call site needs a nil check. A disabled cache and a
// cold cache are the same situation, and the correctness of the service already
// depends on handling a cold one.
type Nop struct{}

func (Nop) Get(context.Context, string) ([]byte, error)              { return nil, ErrMiss }
func (Nop) Set(context.Context, string, []byte, time.Duration) error { return nil }
func (Nop) Delete(context.Context, ...string) error                  { return nil }
func (Nop) DeletePrefix(context.Context, string) error               { return nil }
func (Nop) Ping(context.Context) error                               { return nil }
func (Nop) Close() error                                             { return nil }

// Key namespaces. Grouped here rather than scattered so DeletePrefix and the
// key builders cannot drift apart.
const (
	PrefixScore     = "gs:score:"     // + guest id
	PrefixDirectory = "gs:directory:" // + query fingerprint
	PrefixStats     = "gs:stats:"
	PrefixSearch    = "gs:search:" // + query fingerprint
)

// ScoreKey is the cache key for one guest's computed score.
func ScoreKey(guestID string) string { return PrefixScore + guestID }

// InvalidateGuest drops everything a write about one guest could have
// invalidated.
//
// It is deliberately blunt: the directory and stats are aggregates over all
// guests, so a single new review changes both, and computing which cached
// pages contain a given guest would cost more than recomputing them. Blunt
// invalidation is also the failure mode you want — over-invalidating costs a
// recompute, under-invalidating serves a wrong score.
func InvalidateGuest(ctx context.Context, c Cache, guestID string) error {
	if err := c.Delete(ctx, ScoreKey(guestID)); err != nil {
		return err
	}
	if err := c.DeletePrefix(ctx, PrefixDirectory); err != nil {
		return err
	}
	if err := c.DeletePrefix(ctx, PrefixSearch); err != nil {
		return err
	}
	return c.DeletePrefix(ctx, PrefixStats)
}
