package handler

import (
	"strings"
	"testing"
)

// ── allowedType: extension AND content must both match ──

func TestAllowedTypeStrict(t *testing.T) {
	tests := []struct {
		name  string
		ext   string
		ctype string
		want  bool
	}{
		// consistent uploads accepted
		{"png ok", "png", "image/png", true},
		{"jpg ok", "jpg", "image/jpeg", true},
		{"jpeg ok", "jpeg", "image/jpeg", true},
		{"gif ok", "gif", "image/gif", true},
		{"webp ok", "webp", "image/webp", true},
		{"pdf ok", "pdf", "application/pdf", true},
		// ext not in allow list rejected even with benign content
		{"exe with png content", "exe", "image/png", false},
		{"html with image content", "html", "image/png", false},
		{"svg not in list", "svg", "image/svg+xml", false},
		// ext in list but content mismatch rejected (payload smuggling)
		{"jpg with text content", "jpg", "text/plain; charset=utf-8", false},
		{"png with pdf content", "png", "application/pdf", false},
		{"pdf with zip content", "pdf", "application/zip", false},
		{"jpg with html content", "jpg", "text/html; charset=utf-8", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := allowedType(tt.ext, tt.ctype, imageExts); got != tt.want && tt.ext != "pdf" {
				t.Errorf("allowedType(%q, %q, imageExts) = %v, want %v", tt.ext, tt.ctype, got, tt.want)
			}
		})
	}

	// pdf against pdf-only allow list
	if !allowedType("pdf", "application/pdf", []string{"pdf"}) {
		t.Error("pdf upload should be accepted for pdf list")
	}
	if allowedType("pdf", "application/zip", []string{"pdf"}) {
		t.Error("zip content must be rejected for pdf list")
	}
	if allowedType("exe", "application/pdf", []string{"pdf"}) {
		t.Error("exe extension must be rejected even with pdf content")
	}
}

// ── sanitizeNamePart / lookupName: Content-Disposition injection defense ──

func TestSanitizeNamePart(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"clean.png", "clean.png"},
		{"a\"b", "ab"},
		{"a\\b", "ab"},
		{"a\r\nInjected: 1", "aInjected: 1"},
		{"ctrl\x00\x1f", "ctrl"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := sanitizeNamePart(tt.in); got != tt.want {
			t.Errorf("sanitizeNamePart(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := sanitizeNamePart(strings.Repeat("x", 200)); len(got) > 80 {
		t.Errorf("sanitizeNamePart should cap length at 80, got %d", len(got))
	}
}

func TestLookupNameNoHeaderInjection(t *testing.T) {
	malicious := []string{
		`a"b.png`,
		"line1\r\nX-Injected: 1.png",
		"back\\slash.png",
		"中文名字.png",
		"../../etc/passwd.png",
	}
	for _, m := range malicious {
		name := lookupName("/tmp/source.png", m)
		if strings.ContainsAny(name, "\"\r\n\\") {
			t.Errorf("lookupName(%q) produced unsafe name %q", m, name)
		}
		if strings.Contains(name, "..") {
			t.Errorf("lookupName(%q) produced traversal segment %q", m, name)
		}
		if !strings.HasSuffix(name, ".png") {
			t.Errorf("lookupName(%q) lost extension: %q", m, name)
		}
	}
}

func TestLookupNameUnicodeFilename(t *testing.T) {
	name := lookupName("/tmp/source.png", "照片 翻译.png")
	if !strings.Contains(name, "照片") || !strings.Contains(name, "翻译") {
		t.Errorf("unicode filename should be preserved: %q", name)
	}
}
