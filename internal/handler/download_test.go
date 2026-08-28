package handler

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flowconvert/internal/config"
)

func TestFileStoreRegisterSuccess(t *testing.T) {
	cfg := &config.Config{
		TmpDir:   t.TempDir(),
		OutDir:   t.TempDir(),
		TTLHours: 24,
	}
	store := NewFileStore(cfg)

	src := filepath.Join(cfg.TmpDir, "test.png")
	if err := os.WriteFile(src, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}

	dl, err := store.Register(src, "test.png")
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if !strings.HasPrefix(dl, "/api/download/") {
		t.Fatalf("unexpected dl path: %s", dl)
	}

	info, err := os.Stat(filepath.Join(cfg.OutDir, filepath.Base(strings.TrimPrefix(dl, "/api/download/"))))
	if err != nil {
		t.Fatalf("registered file missing from OutDir: %v", err)
	}
	if info.Size() != 3 {
		t.Fatalf("unexpected file size: %d", info.Size())
	}
}

func TestFileStoreRegisterSourceMissing(t *testing.T) {
	cfg := &config.Config{
		TmpDir:   t.TempDir(),
		OutDir:   t.TempDir(),
		TTLHours: 24,
	}
	store := NewFileStore(cfg)

	_, err := store.Register("/nonexistent/path.png", "test.png")
	if err == nil {
		t.Fatal("expected error when source file is missing, got nil")
	}
}

func TestFileStoreRegisterSourceIsDeleted(t *testing.T) {
	cfg := &config.Config{
		TmpDir:   t.TempDir(),
		OutDir:   t.TempDir(),
		TTLHours: 24,
	}
	store := NewFileStore(cfg)

	src := filepath.Join(cfg.TmpDir, "ephemeral.png")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	os.Remove(src)

	_, err := store.Register(src, "ephemeral.png")
	if err == nil {
		t.Fatal("expected error when source has been deleted, got nil")
	}
}

func TestLookupNameUniqueness(t *testing.T) {
	names := make(map[string]bool)
	for i := 0; i < 100; i++ {
		name := lookupName("/tmp/a.png", "a.png")
		if names[name] {
			t.Fatalf("duplicate lookupName: %s", name)
		}
		names[name] = true
	}
}

func TestIsValidImageType(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"photo.png", true},
		{"photo.jpg", true},
		{"photo.JPEG", true},
		{"photo.webp", true},
		{"photo.gif", true},
		{"photo.bmp", true},
		{"photo.HEIC", false},
		{"photo.png.exe", false},
		{"photo.pdf", false},
		{"photo", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isValidImageType(tt.name); got != tt.want {
				t.Errorf("isValidImageType(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestHandleTextVideoDurationCap(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir(), OutDir: t.TempDir()}
	store := NewFileStore(cfg)
	jobs := NewVideoJobStore(1 * time.Hour)
	h := &VideoGenH{Cfg: cfg, Store: store, Jobs: jobs}

	payload, ctype := buildTextVideoMultipart(t, 999)
	req := httptest.NewRequest("POST", "/api/convert/video/text", bytes.NewReader(payload))
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()

	h.HandleTextVideo(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleRefVideoDurationCap(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir(), OutDir: t.TempDir()}
	store := NewFileStore(cfg)
	jobs := NewVideoJobStore(1 * time.Hour)
	h := &VideoGenH{Cfg: cfg, Store: store, Jobs: jobs}

	payload, ctype := buildRefMultipart(t, 999)
	req := httptest.NewRequest("POST", "/api/convert/video/ref", bytes.NewReader(payload))
	req.Header.Set("Content-Type", ctype)
	w := httptest.NewRecorder()

	h.HandleRefVideo(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleRefVideoMissingPrompt(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir(), OutDir: t.TempDir()}
	store := NewFileStore(cfg)
	jobs := NewVideoJobStore(1 * time.Hour)
	h := &VideoGenH{Cfg: cfg, Store: store, Jobs: jobs}

	payload := []byte("--b\r\nContent-Disposition: form-data; name=\"duration\"\r\n\r\n5\r\n--b--\r\n")
	req := httptest.NewRequest("POST", "/api/convert/video/ref", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=b")
	w := httptest.NewRecorder()

	h.HandleRefVideo(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func buildTextVideoMultipart(t *testing.T, duration int) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("prompt", "a cat")
	w.WriteField("duration", fmt.Sprintf("%d", duration))
	w.WriteField("aspect_ratio", "16:9")
	w.Close()
	return buf.Bytes(), w.FormDataContentType()
}

func buildRefMultipart(t *testing.T, duration int) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("prompt", "a cat")
	w.WriteField("duration", fmt.Sprintf("%d", duration))
	w.WriteField("aspect_ratio", "16:9")
	f, err := w.CreateFormFile("ref_0", "ref_0.png")
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("fakepng"))
	w.Close()
	return buf.Bytes(), w.FormDataContentType()
}

func TestValidAspectRatio(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"16:9", "16:9", "16:9"},
		{"9:16", "9:16", "9:16"},
		{"1:1", "1:1", "1:1"},
		{"4:3", "4:3", "4:3"},
		{"3:4", "3:4", "3:4"},
		{"21:9", "21:9", "21:9"},
		{"invalid", "5:4", "16:9"},
		{"empty", "", "16:9"},
		{"garbage", "abc", "16:9"},
		{"sql_injection", "1; DROP TABLE", "16:9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validAspectRatio(tt.input)
			if got != tt.want {
				t.Errorf("validAspectRatio(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
