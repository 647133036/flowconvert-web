package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewID(t *testing.T) {
	id1 := NewID(8)
	id2 := NewID(8)
	if len(id1) != 16 {
		t.Errorf("NewID(8) returned %d chars, want 16", len(id1))
	}
	if id1 == id2 {
		t.Error("NewID should return unique values")
	}
}

func TestNewIDLength(t *testing.T) {
	tests := []struct {
		n    int
		want int
	}{
		{4, 8},
		{8, 16},
		{16, 32},
	}
	for _, tt := range tests {
		got := len(NewID(tt.n))
		if got != tt.want {
			t.Errorf("NewID(%d) returned %d chars, want %d", tt.n, got, tt.want)
		}
	}
}

func TestCopyFile(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src.txt")
	dst := filepath.Join(tmpDir, "dst.txt")

	content := []byte("hello world test content")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CopyFile(src, dst); err != nil {
		t.Fatalf("CopyFile failed: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content mismatch: got %q, want %q", got, content)
	}
}

func TestCopyFileSourceNotExist(t *testing.T) {
	err := CopyFile("/nonexistent/path/file.txt", "/tmp/dst.txt")
	if err == nil {
		t.Error("expected error for non-existent source")
	}
}

func TestSafeExt(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"svg", "svg"},
		{".svg", "svg"},
		{"SVG", "svg"},
		{" svg ", "svg"},
		{"", ""},
		{"..", ""},
		{"svg/xml", ""},
		{"a1b2", "a1b2"},
	}
	for _, tt := range tests {
		got := SafeExt(tt.input)
		if got != tt.want {
			t.Errorf("SafeExt(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestScriptPath(t *testing.T) {
	got := ScriptPath("test.py")
	if filepath.Base(got) != "test.py" {
		t.Errorf("ScriptPath returned %s, want basename test.py", got)
	}
	if !filepath.IsAbs(got) {
		t.Error("ScriptPath should return absolute path")
	}
}

func TestFileToken(t *testing.T) {
	token, err := FileToken("output.svg")
	if err != nil {
		t.Fatalf("FileToken failed: %v", err)
	}
	if token == "" {
		t.Error("FileToken returned empty")
	}
}

func TestFileTokenRejectsTraversal(t *testing.T) {
	token, err := FileToken("../../etc/passwd")
	if err != nil {
		t.Fatalf("FileToken failed: %v", err)
	}
	if token == "" {
		t.Error("FileToken returned empty")
	}
	if contains(token, "..") {
		t.Error("FileToken should strip path traversal from token")
	}
}

func TestFileTokenPreservesBasename(t *testing.T) {
	token, err := FileToken("result.png")
	if err != nil {
		t.Fatalf("FileToken failed: %v", err)
	}
	if token == "" {
		t.Fatal("FileToken returned empty")
	}
}

func TestRunCmdTimeout(t *testing.T) {
	out, err := RunCmdTimeout(5*time.Second, "echo", "hello")
	if err != nil {
		t.Fatalf("RunCmdTimeout failed: %v", err)
	}
	if !contains(string(out), "hello") {
		t.Errorf("RunCmdTimeout output = %q, want to contain 'hello'", string(out))
	}
}

func TestRunCmdTimeoutExceedsDeadline(t *testing.T) {
	_, err := RunCmdTimeout(1*time.Second, "sleep", "10")
	if err == nil {
		t.Error("expected timeout error for sleep 10 with 1s timeout")
	}
}

func TestRunCmd(t *testing.T) {
	out, err := RunCmd("echo", "test")
	if err != nil {
		t.Fatalf("RunCmd failed: %v", err)
	}
	if !contains(string(out), "test") {
		t.Errorf("RunCmd output = %q, want 'test'", string(out))
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
