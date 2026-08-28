package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"flowconvert/internal/config"
)

func TestHandleFormats(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	conv := &ConvertH{Cfg: cfg, Store: store}

	req := httptest.NewRequest("GET", "/api/formats", nil)
	w := httptest.NewRecorder()
	conv.HandleFormats(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !contains(body, "image_input") {
		t.Error("response missing image_input")
	}
	if !contains(body, "jpeg") {
		t.Error("image_input should include jpeg")
	}
	if !contains(body, "vector_output") {
		t.Error("response missing vector_output")
	}
}

func TestHandleUploadVectorizeMethod(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	conv := &ConvertH{Cfg: cfg, Store: store}

	req := httptest.NewRequest("GET", "/api/convert/upload", nil)
	w := httptest.NewRecorder()
	conv.HandleUploadVectorize(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET, got %d", w.Code)
	}
}

func TestHandleIdPhotoMethod(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	conv := &ConvertH{Cfg: cfg, Store: store}

	req := httptest.NewRequest("GET", "/api/convert/idphoto", nil)
	w := httptest.NewRecorder()
	conv.HandleIdPhoto(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET, got %d", w.Code)
	}
}

func TestHandleSketchMethod(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	conv := &ConvertH{Cfg: cfg, Store: store}

	req := httptest.NewRequest("GET", "/api/convert/sketch", nil)
	w := httptest.NewRecorder()
	conv.HandleSketch(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET, got %d", w.Code)
	}
}

func TestHandlePdfToOfficeMethod(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	conv := &ConvertH{Cfg: cfg, Store: store}

	req := httptest.NewRequest("GET", "/api/convert/pdf-to-office", nil)
	w := httptest.NewRecorder()
	conv.HandlePdfToOffice(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET, got %d", w.Code)
	}
}

func TestParseVecParams(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/convert/upload?mode=polygon&color_precision=4&filter_speckle=8&corner_threshold=90", nil)
	params := parseVecParams(req)

	if params.Mode != "polygon" {
		t.Errorf("expected mode=polygon, got %s", params.Mode)
	}
	if params.ColorPrecision != 4 {
		t.Errorf("expected color_precision=4, got %d", params.ColorPrecision)
	}
}

func TestParseIntDefault(t *testing.T) {
	tests := []struct {
		input string
		def   int
		want  int
	}{
		{"", 10, 10},
		{"5", 10, 5},
		{"abc", 10, 10},
		{"-3", 10, -3},
	}
	for _, tt := range tests {
		got := parseIntDefault(tt.input, tt.def)
		if got != tt.want {
			t.Errorf("parseIntDefault(%q, %d) = %d, want %d", tt.input, tt.def, got, tt.want)
		}
	}
}

func TestAllowedType(t *testing.T) {
	if !allowedType("jpg", "image/jpeg", imageExts) {
		t.Error("jpg should be allowed")
	}
	if !allowedType("png", "image/png", imageExts) {
		t.Error("png should be allowed")
	}
	if allowedType("exe", "application/octet-stream", imageExts) {
		t.Error("exe should not be allowed")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
