package handler

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"flowconvert/internal/config"
)

func TestHandleTranslateMethod(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	h := &TranslateH{Cfg: cfg, Store: store}

	req := httptest.NewRequest("POST", "/api/translate", strings.NewReader(`{"text":"hello","source":"en","target":"zh"}`))
	req.Header.Set("Content-Type", "application/json")

	reqGET := httptest.NewRequest("GET", "/api/translate", nil)
	w := httptest.NewRecorder()
	h.HandleTranslate(w, reqGET)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET, got %d", w.Code)
	}
	_ = req
}

func TestHandleTranslateEmptyBody(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	h := &TranslateH{Cfg: cfg, Store: store}

	req := httptest.NewRequest("POST", "/api/translate", nil)
	w := httptest.NewRecorder()
	h.HandleTranslate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty body, got %d", w.Code)
	}
}

func TestHandleTranslateEmptyText(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	h := &TranslateH{Cfg: cfg, Store: store}

	req := httptest.NewRequest("POST", "/api/translate",
		strings.NewReader(`{"text":"","source":"en","target":"zh"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleTranslate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty text, got %d", w.Code)
	}
}

func TestHandleTranslateTextTooLong(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	h := &TranslateH{Cfg: cfg, Store: store}

	longText := make([]rune, 5001)
	for i := range longText {
		longText[i] = 'a'
	}
	body := `{"text":"` + string(longText) + `","source":"en","target":"zh"}`
	req := httptest.NewRequest("POST", "/api/translate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleTranslate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for too long text, got %d", w.Code)
	}
}

func TestHandleTranslateFileMethod(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	h := &TranslateH{Cfg: cfg, Store: store}

	req := httptest.NewRequest("GET", "/api/translate/file", nil)
	w := httptest.NewRecorder()
	h.HandleTranslateFile(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET, got %d", w.Code)
	}
}

func TestValidLang(t *testing.T) {
	if !validLang("zh") {
		t.Error("zh should be valid")
	}
	if !validLang("auto") {
		t.Error("auto should be valid")
	}
	if validLang("xx") {
		t.Error("xx should not be valid")
	}
}

func TestHandleTranslateInvalidLangFallback(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	h := &TranslateH{Cfg: cfg, Store: store}

	req := httptest.NewRequest("POST", "/api/translate",
		strings.NewReader(`{"text":"hello","source":"invalid","target":"also_invalid"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleTranslate(w, req)

	if w.Code == http.StatusBadRequest {
		return
	}
}

func TestDownloadHandler(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir(), OutDir: t.TempDir(), TTLHours: 1}
	store := NewFileStore(cfg)

	req := httptest.NewRequest("GET", "/api/download/nonexistent", nil)
	w := httptest.NewRecorder()
	store.DownloadHandler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for non-existent file, got %d", w.Code)
	}
}

func TestFileStoreRegister(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir(), OutDir: t.TempDir(), TTLHours: 1}
	store := NewFileStore(cfg)

	tmpFile := cfg.TmpDir + "/test_register.txt"
	if err := writeTestFile(tmpFile, "test content"); err != nil {
		t.Fatal(err)
	}

	url, err := store.Register(tmpFile, "download.txt")
	if err != nil {
		t.Fatal(err)
	}
	if url == "" {
		t.Error("Register returned empty URL")
	}
	if url[:len("/api/download/")] != "/api/download/" {
		t.Errorf("Register returned unexpected URL: %s", url)
	}
}

func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func TestFileStoreRegisterAndDownload(t *testing.T) {
	outDir := t.TempDir()
	cfg := &config.Config{TmpDir: t.TempDir(), OutDir: outDir, TTLHours: 1}
	store := NewFileStore(cfg)

	tmpFile := cfg.TmpDir + "/test_dl.txt"
	content := "download me"
	if err := writeTestFile(tmpFile, content); err != nil {
		t.Fatal(err)
	}

	url, err := store.Register(tmpFile, "result.txt")
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(url, "/api/download/")

	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	store.DownloadHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Body.String() != content {
		t.Errorf("content mismatch: got %q, want %q", w.Body.String(), content)
	}
	_ = name
}

func TestLookupName(t *testing.T) {
	name := lookupName("/tmp/path/file.svg", "converted.svg")
	if name == "" {
		t.Error("lookupName returned empty")
	}
	if !strings.HasSuffix(name, ".svg") {
		t.Errorf("lookupName should preserve extension: %s", name)
	}
}

func TestLookupNameWithUnsafePath(t *testing.T) {
	name := lookupName("/tmp/file.svg", "../../etc/passwd")
	if strings.Contains(name, "..") {
		t.Error("lookupName should sanitize path traversal")
	}
}

func TestRegisterOutput(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir(), OutDir: t.TempDir()}
	store := NewFileStore(cfg)
	h := &ImageGenH{Cfg: cfg, Store: store}

	tmpFile := cfg.TmpDir + "/test_reg.txt"
	if err := writeTestFile(tmpFile, "test"); err != nil {
		t.Fatal(err)
	}

	url, err := h.registerOutput(tmpFile, "generated.png")
	if err != nil {
		t.Errorf("registerOutput failed: %v", err)
	}
	if url == "" {
		t.Error("registerOutput returned empty URL")
	}
}

func TestImageGenHNewTmp(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	h := &ImageGenH{Cfg: cfg, Store: store}

	dir, err := h.newTmp()
	if err != nil {
		t.Fatalf("newTmp failed: %v", err)
	}
	if dir == "" {
		t.Error("newTmp returned empty dir")
	}
}

func TestImageGenHWriteJSON(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	h := &ImageGenH{Cfg: cfg, Store: store}

	w := httptest.NewRecorder()
	h.writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected JSON content type, got %s", ct)
	}
}

func TestImageGenHWriteErr(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	h := &ImageGenH{Cfg: cfg, Store: store}

	w := httptest.NewRecorder()
	h.writeErr(w, http.StatusBadRequest, "test error")

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleTextImageBadSize(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	h := &ImageGenH{Cfg: cfg, Store: store}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	writer.WriteField("prompt", "test art")
	writer.WriteField("width", "not_a_number")
	writer.WriteField("height", "0")
	writer.Close()

	req := httptest.NewRequest("POST", "/api/convert/image/text", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	h.HandleTextImage(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with default sizes, got %d", w.Code)
	}
}

func TestHandleComposeImageNoRefs(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	h := &ImageGenH{Cfg: cfg, Store: store}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	writer.WriteField("prompt", "test compose")
	writer.Close()

	req := httptest.NewRequest("POST", "/api/convert/image/compose", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	h.HandleComposeImage(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestHandleEditImageNoFile(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	h := &ImageGenH{Cfg: cfg, Store: store}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	writer.WriteField("prompt", "edit test")
	writer.Close()

	req := httptest.NewRequest("POST", "/api/convert/image/edit", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	h.HandleEditImage(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing image, got %d", w.Code)
	}
}

func TestVideoGenHHandleTextVideoMethod(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	h := &VideoGenH{Cfg: cfg, Store: store}

	req := httptest.NewRequest("GET", "/api/convert/video/text", nil)
	w := httptest.NewRecorder()
	h.HandleTextVideo(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestVideoGenHHandleTextVideoNoPrompt(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	h := &VideoGenH{Cfg: cfg, Store: store}

	req := httptest.NewRequest("POST", "/api/convert/video/text", nil)
	w := httptest.NewRecorder()
	h.HandleTextVideo(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for no prompt, got %d", w.Code)
	}
}

func TestVideoGenHHandleKeyframeVideoMethod(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	h := &VideoGenH{Cfg: cfg, Store: store}

	req := httptest.NewRequest("GET", "/api/convert/video/keyframe", nil)
	w := httptest.NewRecorder()
	h.HandleKeyframeVideo(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestVideoGenHHandleKeyframeVideoNoFrames(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	h := &VideoGenH{Cfg: cfg, Store: store}

	req := httptest.NewRequest("POST", "/api/convert/video/keyframe", nil)
	w := httptest.NewRecorder()
	h.HandleKeyframeVideo(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing frames, got %d", w.Code)
	}
}

func TestVideoGenHHandleRefVideoMethod(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	h := &VideoGenH{Cfg: cfg, Store: store}

	req := httptest.NewRequest("GET", "/api/convert/video/ref", nil)
	w := httptest.NewRecorder()
	h.HandleRefVideo(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestVideoGenHHandleRefVideoNoPrompt(t *testing.T) {
	cfg := &config.Config{TmpDir: t.TempDir()}
	store := NewFileStore(cfg)
	h := &VideoGenH{Cfg: cfg, Store: store}

	req := httptest.NewRequest("POST", "/api/convert/video/ref", nil)
	w := httptest.NewRecorder()
	h.HandleRefVideo(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for no prompt, got %d", w.Code)
	}
}
