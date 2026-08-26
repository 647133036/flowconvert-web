package service

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestMakeImage(t *testing.T) {
	tmpDir := t.TempDir()
	
	tests := []struct {
		name    string
		prompt  string
		width   int
		height  int
		wantErr bool
	}{
		{"basic", "test prompt", 256, 256, false},
		{"large", "large image", 512, 512, false},
		{"square", "square", 1024, 1024, false},
		{"empty prompt", "", 256, 256, false},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MakeImage(tmpDir, tt.prompt, tt.width, tt.height)
			if (err != nil) != tt.wantErr {
				t.Errorf("MakeImage() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if _, err := os.Stat(got); err != nil {
					t.Errorf("Output file not created: %v", err)
				}
			}
		})
	}
}

func TestMakeEditedImage(t *testing.T) {
	tmpDir := t.TempDir()
	
	srcPath := filepath.Join(tmpDir, "src.png")
	if err := createTestPNG(srcPath, 100, 100); err != nil {
		t.Fatalf("Failed to create test PNG: %v", err)
	}
	
	tests := []struct {
		name    string
		prompt  string
		wantErr bool
	}{
		{"sepia", "sepia filter", false},
		{"invert", "invert colors", false},
		{"blur", "blur effect", false},
		{"posterize", "posterize", false},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MakeEditedImage(tmpDir, srcPath, tt.prompt, 0, 0)
			if (err != nil) != tt.wantErr {
				t.Errorf("MakeEditedImage() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if _, err := os.Stat(got); err != nil {
					t.Errorf("Output file not created: %v", err)
				}
			}
		})
	}
}

func TestMakeComposeImage(t *testing.T) {
	tmpDir := t.TempDir()
	
	var refPaths []string
	for i := 0; i < 2; i++ {
		path := filepath.Join(tmpDir, fmt.Sprintf("ref_%d.png", i))
		if err := createTestPNG(path, 50, 50); err != nil {
			t.Fatalf("Failed to create test PNG: %v", err)
		}
		refPaths = append(refPaths, path)
	}
	
	got, err := MakeComposeImage(tmpDir, "compose test", refPaths, 256, 256)
	if err != nil {
		t.Errorf("MakeComposeImage() error = %v", err)
		return
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("Output file not created: %v", err)
	}
}

func TestHashToSeed(t *testing.T) {
	seed1 := hashToSeed("test")
	seed2 := hashToSeed("test")
	seed3 := hashToSeed("different")
	
	if seed1 != seed2 {
		t.Error("Same input should produce same seed")
	}
	if seed1 == seed3 {
		t.Error("Different input should produce different seed")
	}
}

func TestHSL2RGB(t *testing.T) {
	r, g, b := hsl2rgb(0, 1, 0.5)
	if r != 255 || g != 0 || b != 0 {
		t.Errorf("Red HSL(0,100,50) = (%d,%d,%d), want (255,0,0)", r, g, b)
	}
	
	r, g, b = hsl2rgb(120, 1, 0.5)
	if r != 0 || g != 255 || b != 0 {
		t.Errorf("Green HSL(120,100,50) = (%d,%d,%d), want (0,255,0)", r, g, b)
	}
	
	r, g, b = hsl2rgb(240, 1, 0.5)
	if r != 0 || g != 0 || b != 255 {
		t.Errorf("Blue HSL(240,100,50) = (%d,%d,%d), want (0,0,255)", r, g, b)
	}
}

func createTestPNG(path string, w, h int) error {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r := uint8((x + y) % 256)
			g := uint8((x*2 + y) % 256)
			b := uint8((x + y*2) % 256)
			img.Set(x, y, color.RGBA{r, g, b, 255})
		}
	}
	
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	
	return png.Encode(f, img)
}
