package fbhttp

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Bound password-hashing work independently of attacker-controlled body sizes.
var passwordWork = make(chan struct{}, 4)
var authRate = struct {
	sync.Mutex
	peers map[string]authRateEntry
}{peers: make(map[string]authRateEntry)}

type authRateEntry struct {
	tokens float64
	last   time.Time
}

func allowAuthentication(peer string, now time.Time) bool {
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	authRate.Lock()
	defer authRate.Unlock()
	entry, exists := authRate.peers[peer]
	if !exists {
		if len(authRate.peers) >= 4096 {
			for key, item := range authRate.peers {
				if now.Sub(item.last) > time.Minute {
					delete(authRate.peers, key)
				}
			}
			if len(authRate.peers) >= 4096 {
				return false
			}
		}
		entry = authRateEntry{tokens: 20, last: now}
	}
	entry.tokens = min(20, entry.tokens+now.Sub(entry.last).Seconds())
	entry.last = now
	allowed := entry.tokens >= 1
	if allowed {
		entry.tokens--
	}
	authRate.peers[peer] = entry
	return allowed
}

func limitAuthentication(fn handleFunc) handleFunc {
	return func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		if !allowAuthentication(r.RemoteAddr, time.Now()) {
			w.Header().Set("Retry-After", "1")
			return http.StatusTooManyRequests, nil
		}
		select {
		case passwordWork <- struct{}{}:
			defer func() { <-passwordWork }()
		default:
			w.Header().Set("Retry-After", "1")
			return http.StatusTooManyRequests, nil
		}
		return fn(w, r, d)
	}
}
