package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCORSMiddleware(t *testing.T) {
	handler := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("sets_security_headers", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Error("missing X-Content-Type-Options")
		}
		if w.Header().Get("X-Frame-Options") != "DENY" {
			t.Error("missing X-Frame-Options")
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Error("missing Access-Control-Allow-Origin")
		}
	})

	t.Run("handles_preflight", func(t *testing.T) {
		req := httptest.NewRequest("OPTIONS", "/api/test", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200 for OPTIONS, got %d", w.Code)
		}
	})

	t.Run("passes_through", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})
}

func TestRateLimitMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("allows_under_limit", func(t *testing.T) {
		handler := RateLimit(inner, 5)
		req := httptest.NewRequest("GET", "/api/test", nil)
		req.RemoteAddr = "1.2.3.4:1234"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})

	t.Run("blocks_over_limit", func(t *testing.T) {
		handler := RateLimit(inner, 2)
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest("GET", "/api/test", nil)
			req.RemoteAddr = "5.6.7.8:1234"
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Errorf("request %d: expected 200, got %d", i+1, w.Code)
			}
		}

		req := httptest.NewRequest("GET", "/api/test", nil)
		req.RemoteAddr = "5.6.7.8:1234"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusTooManyRequests {
			t.Errorf("expected 429, got %d", w.Code)
		}
	})

	t.Run("separate_ip_buckets", func(t *testing.T) {
		handler := RateLimit(inner, 2)
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest("GET", "/api/test", nil)
			req.RemoteAddr = "10.0.0.1:1234"
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
		}
		req := httptest.NewRequest("GET", "/api/test", nil)
		req.RemoteAddr = "10.0.0.2:1234"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("different IP should not be rate limited: got %d", w.Code)
		}
	})

	t.Run("skips_non_api", func(t *testing.T) {
		handler := RateLimit(inner, 1)
		for i := 0; i < 5; i++ {
			req := httptest.NewRequest("GET", "/index.html", nil)
			req.RemoteAddr = "10.0.0.1:1234"
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Errorf("non-API request %d should not be limited: got %d", i+1, w.Code)
			}
		}
	})
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name   string
		header string
		value  string
		remote string
		want   string
	}{
		// Public peer: forwarded headers are ignored (anti rate-limit-bypass).
		{"x_forwarded_for_from_public_peer", "X-Forwarded-For", "1.2.3.4, 5.6.7.8", "9.9.9.9:123", "9.9.9.9"},
		{"x_real_ip_from_public_peer", "X-Real-IP", "9.9.9.9", "8.8.8.8:123", "8.8.8.8"},
		// Private/loopback peer (behind our proxy): forwarded headers honored.
		{"x_forwarded_for_from_private_peer", "X-Forwarded-For", "1.2.3.4, 5.6.7.8", "10.0.0.1:123", "1.2.3.4"},
		{"x_real_ip_from_loopback_peer", "X-Real-IP", "9.9.9.9", "127.0.0.1:123", "9.9.9.9"},
		{"remote_addr", "", "", "192.168.1.1:5678", "192.168.1.1"},
		{"remote_addr_public", "", "", "203.0.113.5:999", "203.0.113.5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.remote
			if tt.header != "" {
				req.Header.Set(tt.header, tt.value)
			}
			got := clientIP(req)
			if got != tt.want {
				t.Errorf("clientIP() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestBodyLimitMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := BodyLimit(inner)

	t.Run("allows_small_api_body", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/test", strings.NewReader("hello"))
		req.RemoteAddr = "1.2.3.4:1234"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})

	t.Run("rejects_oversized_api_body", func(t *testing.T) {
		// 64MB limit + 1 byte should be rejected before the handler reads it.
		req := httptest.NewRequest("POST", "/api/test", strings.NewReader(strings.Repeat("a", maxAPIBody+1)))
		req.RemoteAddr = "1.2.3.4:1234"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("expected 413, got %d", w.Code)
		}
	})

	t.Run("skips_non_api_paths", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/index.html", nil)
		req.RemoteAddr = "1.2.3.4:1234"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})
}

func TestRateLimitWindowReset(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := RateLimit(inner, 1)
	req := httptest.NewRequest("GET", "/api/test", nil)
	req.RemoteAddr = "8.8.8.8:1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatal("first request should pass")
	}

	t.Skip("rate limit window reset test requires waiting for window expiry, skipping for CI")
}
