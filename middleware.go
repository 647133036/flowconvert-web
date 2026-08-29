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
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https:; connect-src 'self'; font-src 'self'; frame-ancestors 'none'")
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

// maxAPIBody caps the total size of API request bodies. It covers the
// largest legitimate upload (50MB file + multipart field overhead) while
// preventing oversized bodies from being buffered to disk by
// ParseMultipartForm (whose maxMemory argument does not cap total size).
const maxAPIBody = 64 << 20

// BodyLimit wraps API request bodies with http.MaxBytesReader so oversized
// requests are rejected before any handler buffers them.
func BodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxAPIBody)
		}
		next.ServeHTTP(w, r)
	})
}

type ipBucket struct {
	count     int
	windowEnd time.Time
}

const maxBuckets = 10000

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
			if len(buckets) >= maxBuckets {
				for k, v := range buckets {
					if now.After(v.windowEnd) {
						delete(buckets, k)
					}
				}
				if len(buckets) >= maxBuckets {
					mu.Unlock()
					http.Error(w, "服务器繁忙，请稍后重试", http.StatusServiceUnavailable)
					return
				}
			}
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

// clientIP resolves the client identity for rate limiting.
//
// When the request arrives via a loopback peer (reverse proxy on the
// same host), the X-Forwarded-For chain is inspected. We take the
// LEFTMOST (original client) entry, but only if it is a valid public IP
// — private/loopback addresses in XFF are ignored to prevent spoofing.
// Direct connections use RemoteAddr.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil || !(peer.IsLoopback() || peer.IsPrivate()) {
		return host
	}
	// Behind a proxy; try X-Forwarded-For then X-Real-IP.
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		for _, part := range strings.Split(fwd, ",") {
			ip := net.ParseIP(strings.TrimSpace(part))
			if ip != nil && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsUnspecified() {
				return ip.String()
			}
		}
	}
	if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
		if ip := net.ParseIP(real); ip != nil {
			return ip.String()
		}
	}
	return host
}
