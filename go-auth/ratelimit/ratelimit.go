// Package ratelimit bounds how often one key -- an email address, say --
// may do something, in memory.
//
// In memory is enough for a single replica, which is what the apps using it run;
// a second replica would give each attacker that many more attempts, not
// unlimited ones.
package ratelimit

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Limiter allows up to Limit events per key in any sliding Window.
type Limiter struct {
	limit   int
	window  time.Duration
	maxKeys int
	now     func() time.Time

	name     string
	logger   *slog.Logger
	observer Observer

	mu        sync.Mutex
	hits      map[string][]time.Time
	lastSweep time.Time
	// overflow is the one bucket every new key shares while the table is
	// full. It lives outside hits, so it always has room.
	overflow []time.Time
	// warnedFullAt is when the last "table full" warning went out; one
	// per window is enough to see a flood without being one.
	warnedFullAt time.Time
}

// Config configures a Limiter. MaxKeys bounds memory: the keys are
// whatever a caller sends, so without a bound a flood of distinct ones
// would grow the map for as long as the flood lasted.
type Config struct {
	Limit   int
	Window  time.Duration
	MaxKeys int
	// Name says which limit a log line is about.
	Name string
	// Logger gets a warning when the key table is full; nil discards it.
	Logger slog.Handler
	// Observer is told of every refusal and every attempt counted in the
	// overflow bucket, for metrics; nil tells nobody.
	Observer Observer
	// Now is for tests; nil means time.Now.
	Now func() time.Time
}

// Observer is what a Limiter reports to, by its Name.
type Observer interface {
	RateLimitRefused(limiter string)
	RateLimitOverflowed(limiter string)
}

type nopObserver struct{}

func (nopObserver) RateLimitRefused(string)    {}
func (nopObserver) RateLimitOverflowed(string) {}

// New panics on a limit, window or key bound that is not positive: each
// would quietly switch limiting off -- a zero window forgets every attempt
// at once -- and the configuration is the program's own, so a bad one is a
// bug to find at startup.
func New(cfg *Config) *Limiter {
	if cfg.Limit <= 0 || cfg.Window <= 0 || cfg.MaxKeys <= 0 {
		panic(fmt.Sprintf("ratelimit: limit, window and max keys must be positive, got %d, %s, %d", cfg.Limit, cfg.Window, cfg.MaxKeys))
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	logHandler := cfg.Logger
	if logHandler == nil {
		logHandler = slog.DiscardHandler
	}
	observer := cfg.Observer
	if observer == nil {
		observer = nopObserver{}
	}
	return &Limiter{
		limit:    cfg.Limit,
		window:   cfg.Window,
		maxKeys:  cfg.MaxKeys,
		now:      now,
		name:     cfg.Name,
		logger:   slog.New(logHandler),
		observer: observer,
		hits:     make(map[string][]time.Time),
	}
}

// Allow records an attempt for key and reports whether it is within the
// limit. A refused attempt is not recorded, so a caller who backs off
// regains attempts as their earlier ones age out, however long they kept
// trying.
//
// Once MaxKeys distinct keys are live, every new key shares one overflow
// bucket with the same limit, until the flood's keys age out. Failing open
// would switch the limit off for whoever caused the flood; refusing new
// keys outright would lock out every newcomer for as long as the flood
// lasted plus a window. The overflow bucket bounds what a flood gains at
// one key's allowance, and keys already live are unaffected either way.
// The overflow's attempts aren't attributed to a key, so a newcomer that
// got in through it starts afresh once it has a key of its own: in the
// window the table drains, it may get up to twice its limit.
func (l *Limiter) Allow(key string) bool {
	ok, overflowed := l.allow(key)
	// Told after the lock is released, so an observer never holds up
	// every other caller, and may even call back in.
	if overflowed {
		l.observer.RateLimitOverflowed(l.name)
	}
	if !ok {
		l.observer.RateLimitRefused(l.name)
	}
	return ok
}

// allow is Allow under the lock, also reporting whether the attempt went
// to the overflow bucket.
func (l *Limiter) allow(key string) (ok, overflowed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	cutoff := now.Add(-l.window)
	if now.Sub(l.lastSweep) >= l.window {
		l.sweep(cutoff)
		l.lastSweep = now
	}

	if _, live := l.hits[key]; !live && len(l.hits) >= l.maxKeys {
		l.warnFull(now)
		l.overflow, ok = l.record(trim(l.overflow, cutoff), now)
		return ok, true
	}
	l.hits[key], ok = l.record(trim(l.hits[key], cutoff), now)
	return ok, false
}

// record adds an attempt at now to recent unless it is already at the
// limit, returning the bucket to keep and whether the attempt is allowed.
func (l *Limiter) record(recent []time.Time, now time.Time) ([]time.Time, bool) {
	if len(recent) >= l.limit {
		return recent, false
	}
	return append(recent, now), true
}

func (l *Limiter) warnFull(now time.Time) {
	if now.Sub(l.warnedFullAt) < l.window {
		return
	}
	l.warnedFullAt = now
	l.logger.Warn("rate limiter key table full; new clients share one overflow bucket until it drains",
		"limiter", l.name, "max_keys", l.maxKeys, "window", l.window)
}

func (l *Limiter) sweep(cutoff time.Time) {
	for key, times := range l.hits {
		if len(trim(times, cutoff)) == 0 {
			delete(l.hits, key)
		}
	}
}

// trim drops the attempts at or before cutoff. times is in order, oldest
// first, since attempts are only ever appended.
func trim(times []time.Time, cutoff time.Time) []time.Time {
	for i, t := range times {
		if t.After(cutoff) {
			return times[i:]
		}
	}
	return nil
}
