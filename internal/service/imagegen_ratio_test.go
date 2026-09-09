package service

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestLetterboxImage(t *testing.T) {
	dir := t.TempDir()
	srcP := filepath.Join(dir, "src.png")
	// 生成 100x50 纯红色源图
	sw := image.NewRGBA(image.Rect(0, 0, 100, 50))
	for y := 0; y < 50; y++ {
		for x := 0; x < 100; x++ {
			sw.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, sw); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcP, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	outP := filepath.Join(dir, "lb.png")
	// letterbox 进 1:1 画布 100x100：内容 100x50 居中，上下留白
	if err := letterboxImage(srcP, outP, 100, 100); err != nil {
		t.Fatalf("letterbox: %v", err)
	}
	f, err := os.Open(outP)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	bounds := img.Bounds()
	check := func(x, y int, want color.NRGBA, name string) {
		got := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
		if got != want {
			t.Errorf("%s: got %v want %v", name, got, want)
		}
	}
	// 上留白行应白色
	check(bounds.Min.X+5, bounds.Min.Y+2, color.NRGBA{255, 255, 255, 255}, "上留白")
	// 中心应为红色（原内容）
	check(bounds.Min.X+50, bounds.Min.Y+50, color.NRGBA{255, 0, 0, 255}, "中心")
}


func TestNearestAgnesRatio(t *testing.T) {
	cases := []struct {
		w, h int
		want string
	}{
		{1080, 1920, "9:16"},
		{1920, 1080, "16:9"},
		{1024, 1024, "1:1"},
		{0, 0, "1:1"},
		{1792, 1024, "16:9"}, // 7:4≈1.75 最近 16:9
		{768, 1024, "3:4"},
	}
	for _, c := range cases {
		if got := nearestAgnesRatio(c.w, c.h); got != c.want {
			t.Errorf("nearestAgnesRatio(%d,%d)=%q, want %q", c.w, c.h, got, c.want)
		}
	}
}

func TestResolveEditDims(t *testing.T) {
	// 竖版原图 + original：档位按长边映射，比例跟随原图 9:16，画布为竖版
	tier, ratio, cw, ch := ResolveEditDims("original", 1080, 1920)
	if ratio != "9:16" {
		t.Errorf("original 竖版 ratio=%q, want 9:16", ratio)
	}
	if tier == "" {
		t.Error("tier 不应为空")
	}
	if cw > ch {
		t.Errorf("竖版画布应高>=宽, got %dx%d", cw, ch)
	}
	// 显式 4k 仍跟随原图比例，画布竖版
	if _, r, cw2, ch2 := ResolveEditDims("4k", 1080, 1920); r != "9:16" || cw2 >= ch2 {
		t.Errorf("4k 竖版 ratio=%q canvas=%dx%d, want 9:16 竖版画布", r, cw2, ch2)
	}
	// 无原图约束回退 1:1 + 非空档位 + 方形画布
	tier, ratio, cw, ch = ResolveEditDims("", 0, 0)
	if ratio != "1:1" || tier == "" || cw != ch {
		t.Errorf("无原图约束 tier=%q ratio=%q canvas=%dx%d, want 非空+1:1+方形", tier, ratio, cw, ch)
	}
}

func TestCropToRatio(t *testing.T) {
	// 模拟 Agnes 输出 16:9 档图（1312x736），裁剪回 7:4（700x400）
	tierImg := image.NewRGBA(image.Rect(0, 0, 1312, 736))
	for y := 0; y < 736; y++ {
		for x := 0; x < 1312; x++ {
			tierImg.Set(x, y, color.RGBA{R: 30, G: 80, B: 200, A: 255})
		}
	}
	got := cropToRatio(tierImg, 700, 400)
	b := got.Bounds()
	if b.Dx() != 700 || b.Dy() != 400 {
		t.Fatalf("cropToRatio 尺寸=%dx%d, want 700x400", b.Dx(), b.Dy())
	}
	// 中心像素应保持主体颜色
	c := color.NRGBAModel.Convert(got.At(350, 200)).(color.NRGBA)
	if c.R < 20 || c.G < 60 || c.B < 160 {
		t.Errorf("中心像素被破坏: %v", c)
	}

	// 竖版：Agnes 9:16 输出裁剪回 9:16 原图尺寸
	portrait := image.NewRGBA(image.Rect(0, 0, 736, 1312))
	got2 := cropToRatio(portrait, 900, 1600)
	b2 := got2.Bounds()
	if b2.Dx() != 900 || b2.Dy() != 1600 {
		t.Fatalf("竖版 cropToRatio 尺寸=%dx%d, want 900x1600", b2.Dx(), b2.Dy())
	}

	// 比例完全一致时无裁剪（直接缩放）
	same := image.NewRGBA(image.Rect(0, 0, 800, 600))
	got3 := cropToRatio(same, 800, 600)
	if got3.Bounds().Dx() != 800 || got3.Bounds().Dy() != 600 {
		t.Fatalf("同比例 cropToRatio 尺寸异常: %v", got3.Bounds())
	}
}
