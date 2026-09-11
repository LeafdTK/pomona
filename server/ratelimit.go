package main

import (
	"sync"
	"time"
)

// A small rate limiter for the doors that face the internet: sign-in codes,
// pairing, unlocking. Sliding window per key, in memory, forgotten on
// restart, which is fine for what it guards.

type Limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func NewLimiter() *Limiter { return &Limiter{hits: map[string][]time.Time{}} }

// Allow reports whether key may act again, given at most n actions per
// window, and records the action if so.
func (l *Limiter) Allow(key string, n int, window time.Duration) bool {
	return l.allowAt(key, n, window, time.Now())
}

func (l *Limiter) allowAt(key string, n int, window time.Duration, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-window)
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= n {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	// Keys nobody has touched for a while are dropped, so a long-running
	// server does not remember every address that ever tried.
	if len(l.hits) > 10_000 {
		for k, ts := range l.hits {
			if len(ts) == 0 || !ts[len(ts)-1].After(cutoff) {
				delete(l.hits, k)
			}
		}
	}
	return true
}
