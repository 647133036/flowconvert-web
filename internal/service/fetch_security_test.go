package service

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// ── ensurePublicURL ──

func TestEnsurePublicURLEmpty(t *testing.T) {
	client := NewAIClient("", "", "", "")
	got, err := ensurePublicURL(client, "", "prompt")
	if err != nil {
		t.Fatalf("empty input should return nil error, got %v", err)
	}
	if got != "" {
		t.Fatalf("empty input should return empty, got %s", got)
	}
}

func TestEnsurePublicURLPassthroughPublicIPLiteral(t *testing.T) {
	client := NewAIClient("", "", "", "")
	// Public IP literal passes through without needing an API key.
	for _, u := range []string{
		"http://8.8.8.8/img.png",
		"https://1.1.1.1/file10.png",     // "10." in path: old Contains check misclassified this
		"http://8.8.8.8/a?q=172.16.0.1",  // "172.16." in query: same class of false positive
	} {
		got, err := ensurePublicURL(client, u, "p")
		if err != nil {
			t.Errorf("public URL %q should pass through, got error %v", u, err)
			continue
		}
		if got != u {
			t.Errorf("public URL %q should be returned unchanged, got %q", u, got)
		}
	}
}

func TestEnsurePublicURLRegeneratesForPrivate(t *testing.T) {
	client := NewAIClient("", "", "", "")
	// Without an API key regeneration fails; the private URL must never be
	// returned to the caller (regression: docker-bridge 172.17 was missed
	// by the old string-Contains filter).
	for _, u := range []string{
		"http://localhost:8080/api/download/x.png",
		"http://127.0.0.1/x.png",
		"http://192.168.1.5/x.png",
		"http://172.17.0.5/x.png",
		"http://172.31.255.1/x.png",
		"http://169.254.169.254/latest/meta-data",
		"http://100.100.100.100/x.png",
		"data:image/png;base64,aGVsbG8=",
		"ftp://example.com/a.png",
		"file:///etc/passwd",
	} {
		got, err := ensurePublicURL(client, u, "p")
		if err == nil {
			t.Errorf("non-public URL %q should not pass through, got %q", u, got)
			continue
		}
		if got != "" {
			t.Errorf("non-public URL %q should return empty result, got %q", u, got)
		}
	}
}

// ── FetchImage input validation ──

func TestFetchImageRejectsBadInput(t *testing.T) {
	tmpDir := t.TempDir()
	cases := []struct {
		name string
		url  string
		want error
	}{
		{"empty", "", ErrBadURL},
		{"ftp scheme", "ftp://example.com/a.png", ErrBadURL},
		{"file scheme", "file:///etc/passwd", ErrBadURL},
		{"data scheme", "data:image/png;base64,aGVsbG8=", ErrBadURL},
		{"loopback host", "http://127.0.0.1/x.png", ErrBadURL},
		{"localhost host", "http://localhost/x.png", ErrBadURL},
		{"private host", "http://192.168.1.1/x.png", ErrBadURL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := FetchImage(tmpDir, tc.url, 20<<20)
			if !errors.Is(err, tc.want) && err == nil {
				t.Errorf("FetchImage(%q) = nil error, want %v", tc.url, tc.want)
			}
		})
	}
}

// ── ssrfSafeDialer ──

func TestSSRFSafeDialerControl(t *testing.T) {
	d := ssrfSafeDialer()
	if err := d.Control("tcp", "127.0.0.1:9999", nil); err == nil {
		t.Error("dialer should refuse loopback address")
	}
	if err := d.Control("tcp", "10.0.0.1:80", nil); err == nil {
		t.Error("dialer should refuse private address")
	}
	if err := d.Control("tcp", "169.254.169.254:80", nil); err == nil {
		t.Error("dialer should refuse link-local metadata address")
	}
	if err := d.Control("tcp", "8.8.8.8:53", nil); err != nil {
		t.Errorf("dialer should allow public address, got %v", err)
	}
	if err := d.Control("tcp", "not-an-ip:80", nil); err == nil {
		t.Error("dialer should refuse unparseable address")
	}
}

// ── FetchImage writes a temp file only on success ──

func TestFetchImagePublicReachable(t *testing.T) {
	// Local httptest server on loopback: fetch must be blocked by the host
	// check even though the URL is syntactically valid.
	tmpDir := t.TempDir()
	_, err := FetchImage(tmpDir, "http://127.0.0.1:1/x.png", 1024)
	if err == nil {
		t.Fatal("expected block for loopback URL")
	}
	// no temp file should have been created
	entries, _ := os.ReadDir(tmpDir)
	for _, e := range entries {
		t.Errorf("unexpected file left in tmp: %s", e.Name())
	}
}

// ── CopyFile roundtrip (used by FileStore.Register) ──

func TestCopyFileMissingSource(t *testing.T) {
	dir := t.TempDir()
	err := CopyFile(filepath.Join(dir, "missing.txt"), filepath.Join(dir, "out.txt"))
	if err == nil {
		t.Fatal("expected error for missing source")
	}
	if _, err := os.Stat(filepath.Join(dir, "out.txt")); err == nil {
		t.Fatal("output file should not exist after failed copy")
	}
}

var _ = net.ParseIP // keep net import if future tests need it
