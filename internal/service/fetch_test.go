package service

import (
	"net"
	"strings"
	"testing"
)

func TestIsBlockedIP(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"loopback", "127.0.0.1", true},
		{"loopback6", "::1", true},
		{"private10", "10.0.0.1", true},
		{"private172", "172.16.0.1", true},
		{"private192", "192.168.1.1", true},
		{"linklocal", "169.254.1.1", true},
		{"cgnat", "100.64.0.1", true},
		{"public", "8.8.8.8", false},
		{"public2", "1.1.1.1", false},
		{"multicast", "224.0.0.1", true},
		{"unspecified", "0.0.0.0", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("invalid IP: %s", tt.ip)
			}
			if got := isBlockedIP(ip); got != tt.want {
				t.Errorf("isBlockedIP(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestCheckHost(t *testing.T) {
	if err := checkHost("127.0.0.1"); err == nil {
		t.Error("expected error for loopback IP")
	}
	if err := checkHost("localhost"); err == nil {
		t.Error("expected error for localhost")
	}
	if err := checkHost("[::1]:8080"); err == nil {
		t.Error("expected error for IPv6 loopback with port")
	}
	if err := checkHost("192.168.1.1:8080"); err == nil {
		t.Error("expected error for private IP with port")
	}
}

func TestExtractWebText(t *testing.T) {
	page := `<!DOCTYPE html>
<html><head><title>T</title><style>.a{color:red}</style><script>alert('x')</script></head>
<body>
<h1>Hello &amp; Welcome</h1>
<p>This is <b>bold</b> text.</p>
<div>Second line<br>Third line</div>
<!-- hidden comment -->
</body></html>`

	got := ExtractWebText(page)

	if !strings.Contains(got, "Hello & Welcome") {
		t.Errorf("expected decoded entity, got %q", got)
	}
	if !strings.Contains(got, "This is bold text.") {
		t.Errorf("expected inline tag stripped, got %q", got)
	}
	if !strings.Contains(got, "Second line") || !strings.Contains(got, "Third line") {
		t.Errorf("expected br/block content on separate lines, got %q", got)
	}
	if strings.Contains(got, "alert") {
		t.Errorf("script content should be removed, got %q", got)
	}
	if strings.Contains(got, "color:red") {
		t.Errorf("style content should be removed, got %q", got)
	}
	if strings.Contains(got, "hidden comment") {
		t.Errorf("comment should be removed, got %q", got)
	}
}
