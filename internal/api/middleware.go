package api

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// csp is SECURITY S4, verbatim. No 'unsafe-inline' or 'unsafe-eval'.
const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// maxBody is the default request body limit (SECURITY S5).
const maxBody = 1 << 20

// securityHeaders sets S4 headers on every response, including errors.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/auth" || r.URL.Path == "/metrics" {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// hostCheck rejects any Host other than 127.0.0.1:<port> or localhost:<port>,
// which defeats DNS rebinding (SECURITY S3).
func hostCheck(port func() int, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHost(r.Host, port()) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func allowedHost(host string, port int) bool {
	p := ":" + strconv.Itoa(port)
	return host == "127.0.0.1"+p || host == "localhost"+p
}

// originCheck requires a same-origin Origin header on every non-GET request
// and every WebSocket upgrade (SECURITY S3). A missing Origin is rejected.
func originCheck(port func() int, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if needsOrigin(r) && !allowedOrigin(r.Header.Get("Origin"), port()) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func needsOrigin(r *http.Request) bool {
	return !isSafeMethod(r.Method) || strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead
}

func allowedOrigin(origin string, port int) bool {
	p := ":" + strconv.Itoa(port)
	return origin == "http://127.0.0.1"+p || origin == "http://localhost"+p
}

// bodyLimit caps request bodies at maxBody (SECURITY S5).
func bodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > maxBody {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

// rateLimit applies one token bucket to all state-changing requests. Hearth
// has a single user, so a global bucket is enough (SECURITY S5).
func rateLimit(b *bucket, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isSafeMethod(r.Method) && !b.allow() {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type bucket struct {
	mu     sync.Mutex
	rate   float64 // tokens per second
	burst  float64
	tokens float64
	last   time.Time
	now    func() time.Time
}

func newBucket(rate, burst float64) *bucket {
	return &bucket{rate: rate, burst: burst, tokens: burst, now: time.Now}
}

func (b *bucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if !b.last.IsZero() {
		b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
