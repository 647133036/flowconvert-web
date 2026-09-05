package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flowconvert/internal/config"
	"flowconvert/internal/service"
)

func newOcrHandler(t *testing.T) *OcrH {
	t.Helper()
	tmp := t.TempDir()
	cfg := &config.Config{TmpDir: tmp, OutDir: filepath.Join(tmp, "out"), MaxSize: 1 << 20}
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &OcrH{Cfg: cfg, Store: NewFileStore(cfg), Jobs: NewOcrJobStore(time.Hour)}
}

func ocrUpload(t *testing.T, name string, body []byte, extra ...string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(body); err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		if err := mw.WriteField(extra[i], extra[i+1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/ocr", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func ocrURLOptionUpload(t *testing.T, opts url.Values) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "a.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(blankPNG(t, 40, 40)); err != nil {
		t.Fatal(err)
	}
	for key, vals := range opts {
		for _, v := range vals {
			if err := mw.WriteField(key, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/ocr", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func blankPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodeOcrResponse(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("invalid JSON response: %v\n%s", err, body)
	}
	return out
}

func waitOcrResult(t *testing.T, h *OcrH, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	submit := decodeOcrResponse(t, w.Body.Bytes())
	if submit["success"] != true {
		t.Fatalf("expected success=true, got %v", submit)
	}
	id, _ := submit["task_id"].(string)
	if id == "" {
		t.Fatalf("missing task_id: %v", submit)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		job := h.Jobs.Get(id)
		if job == nil {
			t.Fatal("job disappeared")
		}
		if job.Status == "failed" {
			t.Fatalf("ocr job failed: %s", job.Error)
		}
		if job.Status == "completed" {
			if job.Result == nil {
				t.Fatal("completed job has nil result")
			}
			raw, err := json.Marshal(job.Result)
			if err != nil {
				t.Fatalf("marshal result: %v", err)
			}
			return decodeOcrResponse(t, raw)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("ocr job timed out")
	return nil
}

func TestHandleOcrMethodNotAllowed(t *testing.T) {
	h := newOcrHandler(t)
	w := httptest.NewRecorder()
	h.HandleOcr(w, httptest.NewRequest("GET", "/api/ocr", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET, got %d", w.Code)
	}
}

func TestHandleOcrTaskNotFound(t *testing.T) {
	h := newOcrHandler(t)
	w := httptest.NewRecorder()
	h.HandleOcrTask(w, httptest.NewRequest("GET", "/api/ocr/task/missing", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing task, got %d", w.Code)
	}
}

func TestHandleOcrTaskMethodNotAllowed(t *testing.T) {
	h := newOcrHandler(t)
	w := httptest.NewRecorder()
	h.HandleOcrTask(w, httptest.NewRequest("POST", "/api/ocr/task/x", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for POST, got %d", w.Code)
	}
}

func TestHandleOcrTaskCompleted(t *testing.T) {
	h := newOcrHandler(t)
	job := h.Jobs.Create()
	h.Jobs.SetComplete(job.ID, map[string]interface{}{"success": true, "text": "hi", "empty": false})
	w := httptest.NewRecorder()
	h.HandleOcrTask(w, httptest.NewRequest("GET", "/api/ocr/task/"+job.ID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	out := decodeOcrResponse(t, w.Body.Bytes())
	if out["status"] != "completed" {
		t.Errorf("status = %v, want completed", out["status"])
	}
	res, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("result = %T", out["result"])
	}
	if res["text"] != "hi" {
		t.Errorf("result.text = %v", res["text"])
	}
}

func TestHandleOcrMissingFile(t *testing.T) {
	h := newOcrHandler(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/ocr", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	h.HandleOcr(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing file, got %d", w.Code)
	}
}

func TestHandleOcrMultipartURLFallback(t *testing.T) {
	h := newOcrHandler(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("url", "http://127.0.0.1:9/doc.png"); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/ocr", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	h.HandleOcr(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for a loopback URL sent as multipart, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "请选择要上传的文件") {
		t.Errorf("multipart URL was routed to the upload path: %s", w.Body.String())
	}
}

func TestHandleOcrMissingSource(t *testing.T) {
	h := newOcrHandler(t)
	req := httptest.NewRequest("POST", "/api/ocr", strings.NewReader("mode=general"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.HandleOcr(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when neither file nor url is given, got %d", w.Code)
	}
}

func TestHandleOcrBlockedURLHost(t *testing.T) {
	h := newOcrHandler(t)
	req := httptest.NewRequest("POST", "/api/ocr",
		strings.NewReader("url=http://127.0.0.1:9/doc.png"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.HandleOcr(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for a loopback URL, got %d", w.Code)
	}
}

func TestHandleOcrURLBadScheme(t *testing.T) {
	h := newOcrHandler(t)
	req := httptest.NewRequest("POST", "/api/ocr",
		strings.NewReader("url=ftp://example.com/doc.png"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.HandleOcr(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for a non-http scheme, got %d", w.Code)
	}
}

func TestHandleOcrUnsupportedExt(t *testing.T) {
	h := newOcrHandler(t)
	w := httptest.NewRecorder()
	h.HandleOcr(w, ocrUpload(t, "notes.txt", []byte("hello world this is text")))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for .txt, got %d", w.Code)
	}
}

func TestHandleOcrFileTooSmall(t *testing.T) {
	h := newOcrHandler(t)
	w := httptest.NewRecorder()
	h.HandleOcr(w, ocrUpload(t, "tiny.png", []byte{0, 0}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for truncated file, got %d", w.Code)
	}
}

func TestHandleOcrOversizedFile(t *testing.T) {
	h := newOcrHandler(t)
	w := httptest.NewRecorder()
	// MaxSize is 1MB in the test handler; declare a header size above the limit.
	h.HandleOcr(w, ocrUpload(t, "big.png", bytes.Repeat([]byte{0x89, 0x50, 0x4e, 0x47}, 1<<19)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for oversized file, got %d", w.Code)
	}
}

func TestHandleOcrRejectsContentMismatch(t *testing.T) {
	h := newOcrHandler(t)
	w := httptest.NewRecorder()
	// PNG bytes declared as a PDF must be rejected.
	h.HandleOcr(w, ocrUpload(t, "fake.pdf", blankPNG(t, 64, 64)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for content/ext mismatch, got %d", w.Code)
	}
}

func TestHandleOcrInvalidEngine(t *testing.T) {
	h := newOcrHandler(t)
	w := httptest.NewRecorder()
	h.HandleOcr(w, ocrURLOptionUpload(t, url.Values{"engine": {"gpt4o"}}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid engine, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleOcrInvalidOptions(t *testing.T) {
	h := newOcrHandler(t)
	for _, pair := range [][]string{
		{"mode", "mega"},
		{"quality", "ultra"},
		{"charset", "ja"},
	} {
		w := httptest.NewRecorder()
		h.HandleOcr(w, ocrURLOptionUpload(t, url.Values{pair[0]: {pair[1]}}))
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for %s=%s, got %d", pair[0], pair[1], w.Code)
		}
	}
}

func TestParseOcrBool(t *testing.T) {
	for _, v := range []string{"1", "true", "True", "on", "ON", " yes "} {
		if !parseOcrBool(v) {
			t.Errorf("expected %q to be truthy", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "TRUEISH"} {
		if parseOcrBool(v) {
			t.Errorf("expected %q to be falsy", v)
		}
	}
}

func TestParseOcrOptionsDefaults(t *testing.T) {
	req := ocrUpload(t, "a.png", blankPNG(t, 20, 20), "mode", "advanced", "charset", "zh_hant")
	opts := parseOcrOptions(req)
	if opts.Mode != "advanced" {
		t.Errorf("mode = %q, want advanced", opts.Mode)
	}
	if opts.Charset != "zh_hant" {
		t.Errorf("charset = %q, want zh_hant", opts.Charset)
	}
	if opts.Quality != "" || opts.Engine != "" {
		t.Errorf("empty fields must keep their defaults, got %+v", opts)
	}
	if len(opts.Formats) != 1 || opts.Formats[0] != "txt" {
		t.Errorf("formats = %v, want [txt]", opts.Formats)
	}
}

func TestParseOcrOptionsFormatsAndFlags(t *testing.T) {
	req := ocrUpload(t, "a.png", blankPNG(t, 20, 20),
		"formats", "DOCX, json ,redbox",
		"formats", "txt",
		"redbox", "on", "optimize", "1", "translate", "false")
	opts := parseOcrOptions(req)
	if !opts.Redbox || !opts.Optimize || opts.Translate {
		t.Errorf("flags = %v/%v/%v, want true/true/false", opts.Redbox, opts.Optimize, opts.Translate)
	}
	got := strings.Join(opts.Formats, ",")
	want := "docx,json,redbox,txt"
	if got != want {
		t.Errorf("formats = %q, want %q", got, want)
	}
}

func TestOcrURLMsg(t *testing.T) {
	if ocrURLMsg(service.ErrBadURL) == "" {
		t.Error("expected a message for ErrBadURL")
	}
	if ocrURLMsg(service.ErrNotDoc) == "" {
		t.Error("expected a message for ErrNotDoc")
	}
	if ocrURLMsg(errors.New("下载失败: connection refused")) == "" {
		t.Error("expected a fallback message")
	}
}

func TestRegisterOcrOutputs(t *testing.T) {
	h := newOcrHandler(t)
	p1 := filepath.Join(t.TempDir(), "result.txt")
	p2 := filepath.Join(t.TempDir(), "result.docx")
	if err := os.WriteFile(p1, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p2, []byte("docx"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := h.registerOcrOutputs(map[string]string{"docx": p2, "txt": p1})
	if len(out) != 2 {
		t.Fatalf("expected 2 downloads, got %d: %v", len(out), out)
	}
	if out[0]["type"] != "txt" || out[0]["name"] != "识别结果.txt" {
		t.Errorf("first download = %v", out[0])
	}
	if !strings.HasPrefix(out[0]["url"].(string), "/api/download/") {
		t.Errorf("url = %v, want /api/download/ prefix", out[0]["url"])
	}
	if out[1]["type"] != "docx" {
		t.Errorf("second download = %v", out[1])
	}
}

func TestRegisterOcrOutputsSkipsMissing(t *testing.T) {
	h := newOcrHandler(t)
	out := h.registerOcrOutputs(map[string]string{"json": filepath.Join(t.TempDir(), "nope.json")})
	if len(out) != 0 {
		t.Errorf("expected no downloads for a missing file, got %v", out)
	}
}

func TestHandleOcrBlankImageReturnsEmpty(t *testing.T) {
	if os.Getenv("SKIP_OCR_INTEGRATION") == "1" {
		t.Skip("OCR integration disabled")
	}
	if _, err := os.Stat(filepath.Join("..", "..", "scripts", "ocr.py")); err != nil {
		t.Skip("ocr.py not reachable from test cwd")
	}
	oldWd, _ := os.Getwd()
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	h := newOcrHandler(t)
	w := httptest.NewRecorder()
	h.HandleOcr(w, ocrUpload(t, "blank.png", blankPNG(t, 320, 80),
		"mode", "general", "quality", "normal", "charset", "auto",
		"formats", "txt,json,docx", "redbox", "1", "optimize", "1"))
	out := waitOcrResult(t, h, w)
	for _, key := range []string{
		"success", "text", "translated_text", "downloads", "engine", "pages", "chars",
		"charset", "from_text_layer", "layout", "blocks", "lines", "filtered_headers",
		"warnings", "elapsed_ms", "options", "source", "ext", "empty",
	} {
		if _, ok := out[key]; !ok {
			t.Errorf("response missing field %q", key)
		}
	}
	if out["success"] != true {
		t.Errorf("expected success=true, got %v", out["success"])
	}
	if out["empty"] != true {
		t.Errorf("expected empty=true for a blank image, got %v", out["empty"])
	}
	if out["source"] != "file" {
		t.Errorf("expected source=file, got %v", out["source"])
	}
	if out["ext"] != "png" {
		t.Errorf("expected ext=png, got %v", out["ext"])
	}
	for _, key := range []string{"blocks", "lines", "filtered_headers", "warnings", "downloads"} {
		if _, ok := out[key].([]any); !ok {
			t.Errorf("expected %s to be a JSON array, got %T", key, out[key])
		}
	}
	downloads, _ := out["downloads"].([]any)
	if len(downloads) != 4 {
		t.Errorf("expected 4 download entries, got %d: %v", len(downloads), downloads)
	}
	layout, ok := out["layout"].(map[string]any)
	if !ok {
		t.Fatalf("expected layout object, got %T", out["layout"])
	}
	for _, key := range []string{"columns", "tables", "blocks", "lines"} {
		if _, ok := layout[key]; !ok {
			t.Errorf("layout missing %q", key)
		}
	}
}

func TestHandleOcrTranslateFieldIsEmptyWhenDisabled(t *testing.T) {
	if os.Getenv("SKIP_OCR_INTEGRATION") == "1" {
		t.Skip("OCR integration disabled")
	}
	if _, err := os.Stat(filepath.Join("..", "..", "scripts", "ocr.py")); err != nil {
		t.Skip("ocr.py not reachable from test cwd")
	}
	oldWd, _ := os.Getwd()
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	h := newOcrHandler(t)
	w := httptest.NewRecorder()
	h.HandleOcr(w, ocrUpload(t, "blank.png", blankPNG(t, 320, 80), "translate", "0"))
	out := waitOcrResult(t, h, w)
	if out["translated_text"] != "" {
		t.Errorf("expected empty translated_text, got %v", out["translated_text"])
	}
}
