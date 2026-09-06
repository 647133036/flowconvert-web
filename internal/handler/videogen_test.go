package handler

import (
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseFormAcceptsBothContentTypes(t *testing.T) {
	// urlencoded (curl -d, scripts) must not be rejected as "body too large".
	// ParseMultipartForm returns http.ErrNotMultipart for this content type.
	req := httptest.NewRequest("POST", "/x", strings.NewReader("prompt=hello&duration=8"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := parseForm(req, 10<<20); err != nil {
		t.Fatalf("urlencoded body rejected: %v", err)
	}
	if got := req.FormValue("prompt"); got != "hello" {
		t.Errorf("prompt = %q, want hello", got)
	}

	// multipart (browser uploads) must still work, with a well-formed body.
	var buf strings.Builder
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("prompt", "hello")
	_ = w.Close()
	req2 := httptest.NewRequest("POST", "/x", strings.NewReader(buf.String()))
	req2.Header.Set("Content-Type", w.FormDataContentType())
	if err := parseForm(req2, 10<<20); err != nil {
		t.Fatalf("multipart body rejected: %v", err)
	}
	if got := req2.FormValue("prompt"); got != "hello" {
		t.Errorf("multipart prompt = %q, want hello", got)
	}

	// No Content-Type (curl default, empty body) resolves to urlencoded.
	req3 := httptest.NewRequest("POST", "/x", nil)
	if err := parseForm(req3, 10<<20); err != nil {
		t.Fatalf("empty request rejected: %v", err)
	}
}
