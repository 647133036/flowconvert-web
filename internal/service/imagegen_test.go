package service

import (
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
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

// TestMakeEditedImage_AllFormats 覆盖本地降级编辑：上传图并非都是 PNG，
// 解码须走 image.Decode（全格式），否则 JPEG/GIF/WebP 等会"解码失败"。
func TestMakeEditedImage_AllFormats(t *testing.T) {
	tmpDir := t.TempDir()

	createImg := func(path string, w, h int, enc func(w io.Writer, m image.Image) error) error {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 128, 255})
			}
		}
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		defer f.Close()
		return enc(f, img)
	}

	cases := []struct {
		name string
		file string
		enc  func(w io.Writer, m image.Image) error
	}{
		{"png", "in.png", func(w io.Writer, m image.Image) error { return png.Encode(w, m) }},
		{"jpeg", "in.jpg", func(w io.Writer, m image.Image) error { return jpeg.Encode(w, m, nil) }},
		{"gif", "in.gif", func(w io.Writer, m image.Image) error { return gif.Encode(w, m, nil) }},
		{"bmp", "in.bmp", func(w io.Writer, m image.Image) error { return bmp.Encode(w, m) }},
		{"tiff", "in.tiff", func(w io.Writer, m image.Image) error { return tiff.Encode(w, m, nil) }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := filepath.Join(tmpDir, c.file)
			if err := createImg(src, 80, 60, c.enc); err != nil {
				t.Fatalf("create %s: %v", c.name, err)
			}
			got, err := MakeEditedImage(tmpDir, src, "sepia", 0, 0)
			if err != nil {
				t.Fatalf("MakeEditedImage(%s) error = %v", c.name, err)
			}
			if _, err := os.Stat(got); err != nil {
				t.Errorf("output not created for %s: %v", c.name, err)
			}
		})
	}

	// 负例：喂非法字节应报"解码失败"而不是 panic 或静默成功。
	t.Run("garbage", func(t *testing.T) {
		src := filepath.Join(tmpDir, "in.bin")
		if err := os.WriteFile(src, []byte("this is not an image at all"), 0o644); err != nil {
			t.Fatalf("write garbage: %v", err)
		}
		if _, err := MakeEditedImage(tmpDir, src, "sepia", 0, 0); err == nil {
			t.Errorf("MakeEditedImage(garbage): expected decode error, got nil")
		}
	})
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
