package main

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CORS middleware adds security headers
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

type ipBucket struct {
	count    int
	windowEnd time.Time
}

// RateLimit implements a per-IP sliding-window rate limiter.
func RateLimit(next http.Handler, maxRequests int) http.Handler {
	var mu sync.Mutex
	buckets := make(map[string]*ipBucket)
	window := time.Minute

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			mu.Lock()
			now := time.Now()
			for ip, b := range buckets {
				if now.After(b.windowEnd) {
					delete(buckets, ip)
				}
			}
			mu.Unlock()
		}
	}()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		ip := clientIP(r)

		mu.Lock()
		now := time.Now()
		b, ok := buckets[ip]
		if !ok || now.After(b.windowEnd) {
			b = &ipBucket{count: 0, windowEnd: now.Add(window)}
			buckets[ip] = b
		}
		b.count++
		allowed := b.count <= maxRequests
		mu.Unlock()

		if !allowed {
			http.Error(w, "请求过于频繁", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP resolves the client identity for rate limiting. Forwarded headers
// are honored only when the peer is a loopback/private address (i.e. the
// request came through our own reverse proxy); direct public connections use
// RemoteAddr so attackers cannot rotate spoofed X-Forwarded-For values to
// bypass the rate limiter.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	if peer := net.ParseIP(host); peer != nil && (peer.IsLoopback() || peer.IsPrivate()) {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			parts := strings.SplitN(fwd, ",", 2)
			if v := strings.TrimSpace(parts[0]); v != "" {
				return v
			}
		}
		if real := r.Header.Get("X-Real-IP"); real != "" {
			return strings.TrimSpace(real)
		}
	}
	return host
}
