package service

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestMakeSketch(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "src.png")
	if err := createTestPNG(srcPath, 100, 100); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(ScriptPath("vectorize.py")); err != nil {
		t.Skipf("scripts not available, skipping: %v", err)
	}

	tests := []struct {
		name    string
		sigma   float64
		wantErr bool
	}{
		{"default", 0, false},
		{"small", 0.5, false},
		{"medium", 5.0, false},
		{"large", 10.0, false},
		{"too_large", 100, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MakeSketch(tmpDir, srcPath, tt.sigma)
			if (err != nil) != tt.wantErr {
				t.Errorf("MakeSketch() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if _, err := os.Stat(got); err != nil {
					t.Errorf("output file not created: %v", err)
				}
			}
		})
	}
}

func TestMakeSketchInvalidSource(t *testing.T) {
	if _, err := os.Stat(ScriptPath("vectorize.py")); err != nil {
		t.Skipf("scripts not available, skipping: %v", err)
	}
	tmpDir := t.TempDir()
	_, err := MakeSketch(tmpDir, "/nonexistent/file.png", 3.0)
	if err == nil {
		t.Error("expected error for non-existent source file")
	}
}

func TestMakeSketchSigmaClamping(t *testing.T) {
	if _, err := os.Stat(ScriptPath("vectorize.py")); err != nil {
		t.Skipf("scripts not available, skipping: %v", err)
	}
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "src.png")
	if err := createTestPNG(srcPath, 50, 50); err != nil {
		t.Fatal(err)
	}

	got, err := MakeSketch(tmpDir, srcPath, 0)
	if err != nil {
		t.Fatalf("MakeSketch with sigma=0 failed: %v", err)
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("output not created: %v", err)
	}
}

// ensure createTestPNG is the shared helper from imagegen_test.go
var _ = image.NewRGBA
var _ = color.RGBA{}
var _ = png.Encode
