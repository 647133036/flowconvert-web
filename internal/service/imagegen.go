package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"log"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MakeImage generates a procedural abstract image from a text prompt.
func MakeImage(tmpDir, prompt string, width, height int) (string, error) {
	if width <= 0 {
		width = 1024
	}
	if height <= 0 {
		height = 1024
	}
	if width > 4096 {
		width = 4096
	}
	if height > 4096 {
		height = 4096
	}
	dest := filepath.Join(tmpDir, "generated.png")
	seed := hashToSeed(prompt)
	rng := rand.New(rand.NewSource(int64(seed)))
	bgHue := rng.Float64() * 360
	style := rng.Intn(5)
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	switch style {
	case 0:
		renderGradient(img, width, height, bgHue, rng)
	case 1:
		renderGeo(img, width, height, prompt, rng, bgHue)
	case 2:
		renderWave(img, width, height, prompt, rng, bgHue)
	case 3:
		renderParticle(img, width, height, prompt, rng, bgHue)
	default:
		renderMosaic(img, width, height, prompt, rng, bgHue)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", fmt.Errorf("编码失败: %v", err)
	}
	if err := os.WriteFile(dest, buf.Bytes(), 0o644); err != nil {
		return "", fmt.Errorf("保存失败: %v", err)
	}
	return dest, nil
}

// MakeEditedImage applies an artistic filter to an uploaded image.
func MakeEditedImage(tmpDir, srcPath, prompt string, width, height int) (string, error) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return "", fmt.Errorf("读取失败: %v", err)
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("解码失败: %v", err)
	}
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	dstW, dstH := srcW, srcH
	if width > 0 {
		dstW = width
	}
	if height > 0 {
		dstH = height
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	scaleX := float64(srcW) / float64(dstW)
	scaleY := float64(srcH) / float64(dstH)
	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW; x++ {
			sx := int(float64(x) * scaleX)
			if sx >= srcW {
				sx = srcW - 1
			}
			sy := int(float64(y) * scaleY)
			if sy >= srcH {
				sy = srcH - 1
			}
			dst.Set(x, y, src.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	seed := hashToSeed(prompt + "_edit")
	rng := rand.New(rand.NewSource(int64(seed)))
	switch rng.Intn(4) {
	case 0:
		applySepia(dst, dstW, dstH)
	case 1:
		applyInvert(dst, dstW, dstH)
	case 2:
		applyBlur(dst, dstW, dstH, rng)
	case 3:
		applyPosterize(dst, dstW, dstH, rng)
	}
	out := filepath.Join(tmpDir, "edited.png")
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return "", fmt.Errorf("编码失败: %v", err)
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		return "", fmt.Errorf("保存失败: %v", err)
	}
	return out, nil
}

// MakeComposeImage blends reference images with prompt-based generation.
func MakeComposeImage(tmpDir, prompt string, refPaths []string, width, height int) (string, error) {
	if width <= 0 {
		width = 1024
	}
	if height <= 0 {
		height = 1024
	}
	if width > 4096 {
		width = 4096
	}
	if height > 4096 {
		height = 4096
	}
	dest := filepath.Join(tmpDir, "composed.png")
	seed := hashToSeed(prompt)
	rng := rand.New(rand.NewSource(int64(seed)))
	bgHue := rng.Float64() * 360
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	renderGradient(img, width, height, bgHue, rng)
	for _, p := range refPaths {
		d, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		ref, _, err := image.Decode(bytes.NewReader(d))
		if err != nil {
			continue
		}
		blendOverlay(img, ref, width, height, rng)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", fmt.Errorf("编码失败: %v", err)
	}
	if err := os.WriteFile(dest, buf.Bytes(), 0o644); err != nil {
		return "", fmt.Errorf("保存失败: %v", err)
	}
	return dest, nil
}

// ── Rendering ──

func renderGradient(img *image.RGBA, w, h int, hue float64, rng *rand.Rand) {
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			t := float64(y) / float64(h)
			r, g, b := hsl2rgb(fmod(hue+t*120, 360), 0.6+0.2*rng.Float64(), 0.15+0.35*t)
			img.Set(x, y, color.RGBA{r, g, b, 255})
		}
	}
}

func renderGeo(img *image.RGBA, w, h int, prompt string, rng *rand.Rand, bgHue float64) {
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b := hsl2rgb(bgHue, 0.5, 0.1)
			img.Set(x, y, color.RGBA{r, g, b, 255})
		}
	}
	seed := hashToSeed(prompt + "_g")
	rng2 := rand.New(rand.NewSource(int64(seed)))
	for i := 0; i < 8+rng2.Intn(12); i++ {
		cx := rng2.Float64() * float64(w)
		cy := rng2.Float64() * float64(h)
		rad := 15 + rng2.Float64()*120
		hue := fmod(bgHue+float64(i)*25, 360)
		a := 0.3 + rng2.Float64()*0.5
		if rng2.Float64() < 0.5 {
			drawCircle(img, w, h, cx, cy, rad, hue, 0.6, 0.4, a)
		} else {
			drawRect(img, w, h, cx, cy, rad, rad*0.7, hue, 0.6, 0.4, a)
		}
	}
}

func renderWave(img *image.RGBA, w, h int, prompt string, rng *rand.Rand, bgHue float64) {
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b := hsl2rgb(bgHue, 0.4, 0.08)
			img.Set(x, y, color.RGBA{r, g, b, 255})
		}
	}
	for wi := 0; wi < 5+rng.Intn(6); wi++ {
		amp := 8 + rng.Float64()*35
		freq := 0.003 + rng.Float64()*0.02
		phase := rng.Float64() * math.Pi * 2
		spd := rng.Float64() * 0.3
		hue := fmod(bgHue+float64(wi)*35, 360)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				wave := math.Sin(float64(x)*freq+phase+float64(y)*spd) * amp
				dist := math.Abs(float64(y) - float64(h/2) - wave)
				if dist < 12 {
					fi := 1.0 - dist/12.0
					r, g, b := hsl2rgb(hue, 0.7, 0.5)
					blendPixelFloat(img, x, y, float64(r), float64(g), float64(b), fi)
				}
			}
		}
	}
}

func renderParticle(img *image.RGBA, w, h int, prompt string, rng *rand.Rand, bgHue float64) {
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b := hsl2rgb(bgHue, 0.3, 0.05)
			img.Set(x, y, color.RGBA{r, g, b, 255})
		}
	}
	for i := 0; i < 80+rng.Intn(150); i++ {
		cx := rng.Float64() * float64(w)
		cy := rng.Float64() * float64(h)
		r := 2 + rng.Float64()*5
		hue := fmod(bgHue+rng.Float64()*100, 360)
		for dy := -int(r * 3); dy <= int(r*3); dy++ {
			for dx := -int(r * 3); dx <= int(r*3); dx++ {
				d := math.Sqrt(float64(dx*dx + dy*dy))
				if d > r*3 {
					continue
				}
				px, py := int(cx)+dx, int(cy)+dy
				if px < 0 || px >= w || py < 0 || py >= h {
					continue
				}
				fi := (1.0 - d/(r*3)) * (1.0 - d/(r*3))
				r2, g2, b2 := hsl2rgb(hue, 0.8, 0.6)
				blendPixelFloat(img, px, py, float64(r2), float64(g2), float64(b2), fi)
			}
		}
	}
}

func renderMosaic(img *image.RGBA, w, h int, prompt string, rng *rand.Rand, bgHue float64) {
	seed := hashToSeed(prompt + "_m")
	rng2 := rand.New(rand.NewSource(int64(seed)))
	ts := 18 + rng2.Intn(50)
	for y := 0; y < h; y += ts {
		for x := 0; x < w; x += ts {
			hue := fmod(bgHue+rng2.Float64()*160, 360)
			sat := 0.3 + rng2.Float64()*0.5
			light := 0.2 + rng2.Float64()*0.4
			for dy := 0; dy < ts && y+dy < h; dy++ {
				for dx := 0; dx < ts && x+dx < w; dx++ {
					r, g, b := hsl2rgb(hue, sat, light)
					img.Set(x+dx, y+dy, color.RGBA{r, g, b, 255})
				}
			}
		}
	}
}

// ── Primitives ──

func drawCircle(img *image.RGBA, w, h int, cx, cy, rad, hue, sat, light, alpha float64) {
	for dy := -int(rad); dy <= int(rad); dy++ {
		for dx := -int(rad); dx <= int(rad); dx++ {
			d := math.Sqrt(float64(dx*dx + dy*dy))
			if d > rad {
				continue
			}
			px, py := int(cx)+dx, int(cy)+dy
			if px < 0 || px >= w || py < 0 || py >= h {
				continue
			}
			fi := (1.0 - d/rad) * alpha
			r, g, b := hsl2rgb(hue, sat, light)
			blendPixelFloat(img, px, py, float64(r), float64(g), float64(b), fi)
		}
	}
}

func drawRect(img *image.RGBA, w, h int, cx, cy, rw, rh, hue, sat, light, alpha float64) {
	x0, x1 := int(cx-rw), int(cx+rw)
	y0, y1 := int(cy-rh), int(cy+rh)
	for y := y0; y < y1; y++ {
		if y < 0 || y >= h {
			continue
		}
		for x := x0; x < x1; x++ {
			if x < 0 || x >= w {
				continue
			}
			r, g, b := hsl2rgb(hue, sat, light)
			blendPixelFloat(img, x, y, float64(r), float64(g), float64(b), alpha)
		}
	}
}

func blendOverlay(dst *image.RGBA, src image.Image, dw, dh int, rng *rand.Rand) {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	scale := math.Min(float64(dw)/float64(sw), float64(dh)/float64(sh))
	nw := int(float64(sw) * scale)
	nh := int(float64(sh) * scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	ox := (dw - nw) / 2
	oy := (dh - nh) / 2
	alpha := 0.4 + rng.Float64()*0.3
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			sr, sg, sb, sa := src.At(sb.Min.X+x, sb.Min.Y+y).RGBA()
			dx, dy := ox+x, oy+y
			if dx < 0 || dx >= dw || dy < 0 || dy >= dh {
				continue
			}
			dr, dg, db, _ := dst.At(dx, dy).RGBA()
			fact := float64(sa) / 65535.0 * alpha
			cr := uint8(float64(dr)/256.0*(1-fact) + float64(sr/256)*fact)
			cg := uint8(float64(dg)/256.0*(1-fact) + float64(sg/256)*fact)
			cb := uint8(float64(db)/256.0*(1-fact) + float64(sb/256)*fact)
			dst.Set(dx, dy, color.RGBA{cr, cg, cb, 255})
		}
	}
}

// ── Edit Filters ──

func applySepia(img *image.RGBA, w, h int) {
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			fr := float64(r) / 256.0
			fg := float64(g) / 256.0
			fb := float64(b) / 256.0
			tr := fr*0.393 + fg*0.769 + fb*0.189
			tg := fr*0.349 + fg*0.686 + fb*0.168
			tb := fr*0.272 + fg*0.534 + fb*0.131
			img.Set(x, y, color.RGBA{
				uint8(clampVal(tr * 255)),
				uint8(clampVal(tg * 255)),
				uint8(clampVal(tb * 255)),
				255,
			})
		}
	}
}

func applyInvert(img *image.RGBA, w, h int) {
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			img.Set(x, y, color.RGBA{
				uint8(255 - r/256),
				uint8(255 - g/256),
				uint8(255 - b/256),
				uint8(a >> 8),
			})
		}
	}
}

func applyBlur(img *image.RGBA, w, h int, rng *rand.Rand) {
	radius := 1 + rng.Intn(3)
	clone := make([]color.RGBA, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			clone[y*w+x] = toColor(img.At(x, y))
		}
	}
	for y := radius; y < h-radius; y++ {
		for x := radius; x < w-radius; x++ {
			var tr, tg, tb, ta int
			n := 0
			for dy := -radius; dy <= radius; dy++ {
				for dx := -radius; dx <= radius; dx++ {
					c := clone[(y+dy)*w+(x+dx)]
					tr += int(c.R)
					tg += int(c.G)
					tb += int(c.B)
					ta += int(c.A)
					n++
				}
			}
			img.Set(x, y, color.RGBA{
				uint8(tr / n),
				uint8(tg / n),
				uint8(tb / n),
				uint8(ta / n),
			})
		}
	}
}

func applyPosterize(img *image.RGBA, w, h int, rng *rand.Rand) {
	levels := 2 + rng.Intn(6)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := toColor(img.At(x, y))
			r := uint8(math.Round(float64(c.R)/255*float64(levels)) / float64(levels) * 255)
			g := uint8(math.Round(float64(c.G)/255*float64(levels)) / float64(levels) * 255)
			b := uint8(math.Round(float64(c.B)/255*float64(levels)) / float64(levels) * 255)
			img.Set(x, y, color.RGBA{r, g, b, c.A})
		}
	}
}

// ── Helpers ──

func blendPixelFloat(img *image.RGBA, x, y int, r, g, b, factor float64) {
	cr, cg, cb, _ := img.At(x, y).RGBA()
	fr := float64(cr) / 256.0
	fg := float64(cg) / 256.0
	fb := float64(cb) / 256.0
	pr := uint8(fr*(1-factor) + r*factor)
	pg := uint8(fg*(1-factor) + g*factor)
	pb := uint8(fb*(1-factor) + b*factor)
	img.Set(x, y, color.RGBA{pr, pg, pb, 255})
}

func hsl2rgb(h, s, l float64) (r, g, b uint8) {
	h = fmod(h, 360) / 360.0
	var rr, gg, bb float64
	if s == 0 {
		rr, gg, bb = l, l, l
	} else {
		q := l
		if l < 0.5 {
			q = l * (1 + s)
		} else {
			q = l + s - l*s
		}
		p := 2*l - q
		rr = hue2rgb(p, q, h+1.0/3.0)
		gg = hue2rgb(p, q, h)
		bb = hue2rgb(p, q, h-1.0/3.0)
	}
	return uint8(rr * 255), uint8(gg * 255), uint8(bb * 255)
}

func hue2rgb(p, q, t float64) float64 {
	if t < 0 {
		t += 1
	}
	if t > 1 {
		t -= 1
	}
	if t < 1.0/6.0 {
		return p + (q-p)*6*t
	}
	if t < 1.0/2.0 {
		return q
	}
	if t < 2.0/3.0 {
		return p + (q-p)*(2.0/3.0-t)*6
	}
	return p
}

func fmod(a, b float64) float64 {
	return a - float64(int(a/b))*b
}

func hashToSeed(s string) uint32 {
	h := sha256.Sum256([]byte(s))
	return binary.BigEndian.Uint32(h[:4])
}

func toColor(c color.Color) color.RGBA {
	r, g, b, a := c.RGBA()
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
}

func clampVal(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// ── AI-powered image generation ──

// sizeToTier converts pixel dimensions to Agnes size tier string.
func sizeToTier(w, h int) string {
	max := w
	if h > max {
		max = h
	}
	switch {
	case max <= 1024:
		return "1K"
	case max <= 2048:
		return "2K"
	case max <= 3072:
		return "3K"
	default:
		return "4K"
	}
}

// ratioFromDims computes an aspect ratio string from width and height.
func ratioFromDims(w, h int) string {
	g := gcd(w, h)
	if g == 0 {
		return "1:1"
	}
	return fmt.Sprintf("%d:%d", w/g, h/g)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// agnesRatioTiers 是 Agnes 图片接口支持的宽高比档位（见 aiclient.go 注释）。
var agnesRatioTiers = []struct {
	ratio string
	val   float64
}{
	{"1:1", 1.0}, {"16:9", 16.0 / 9.0}, {"9:16", 9.0 / 16.0},
	{"4:3", 4.0 / 3.0}, {"3:4", 3.0 / 4.0}, {"2:3", 2.0 / 3.0}, {"3:2", 3.0 / 2.0},
}

// nearestAgnesRatio 把任意宽高映射到最接近的 Agnes 支持档位（对数距离最小，横竖版对称）。
// 尺寸未知(0)时回退 1:1。
func nearestAgnesRatio(w, h int) string {
	if w <= 0 || h <= 0 {
		return "1:1"
	}
	target := float64(w) / float64(h)
	best, bestDist := "1:1", math.MaxFloat64
	for _, t := range agnesRatioTiers {
		d := math.Abs(math.Log(target) - math.Log(t.val))
		if d < bestDist {
			bestDist, best = d, t.ratio
		}
	}
	return best
}

// agnesShortSide 各 size 档位对应的画布短边像素（用于 letterbox 画布分辨率）。
func agnesShortSide(tier string) int {
	switch tier {
	case "2K":
		return 1536
	case "3K":
		return 2048
	case "4K":
		return 2560
	default:
		return 1024 // 1K / 未知
	}
}

// ratioDims 解析 Agnes ratio 标签为 (w, h) 整数比例。
func ratioDims(ratio string) (int, int) {
	for _, t := range agnesRatioTiers {
		if t.ratio == ratio {
			// 从标签字符串解析 a:b
			a, b := 1, 1
			_, _ = fmt.Sscanf(ratio, "%d:%d", &a, &b)
			return a, b
		}
	}
	return 1, 1
}

// ResolveEditDims 依据前端 sizeKey 与原图尺寸，算出 AI 图片接口的 size 档位、ratio
// 以及用于 letterbox 的目标画布像素 (canvasW, canvasH)。
// 目标画布比例始终跟随原图最近档位；原图等比缩放进画布居中、不足处留白，主体不拉伸。
// 原图尺寸未知(ow=oh=0)时回退 1:1 + 1K 画布。
func ResolveEditDims(sizeKey string, origW, origH int) (tier, ratio string, canvasW, canvasH int) {
	ratio = nearestAgnesRatio(origW, origH)
	switch sizeKey {
	case "1k":
		tier = "1K"
	case "2k":
		tier = "2K"
	case "4k":
		tier = "4K"
	default:
		tier = sizeToTier(origW, origH)
	}
	aw, ah := ratioDims(ratio)
	short := agnesShortSide(tier)
	if aw >= ah { // 横版或方形：短边在高
		canvasH = short
		canvasW = short * aw / ah
	} else { // 竖版：短边在宽
		canvasW = short
		canvasH = short * ah / aw
	}
	if canvasW < 2 {
		canvasW = 2
	}
	if canvasH < 2 {
		canvasH = 2
	}
	return tier, ratio, canvasW, canvasH
}

// letterboxImage 把 srcPath 的图等比缩放放入 (tw×th) 画布，居中，留白填白，双线性缩放，输出 PNG。
// 解码失败（如 webp 等标准库不支持的格式）返回 error，调用方应退回原图 dataURI。
func letterboxImage(srcPath, outPath string, tw, th int) error {
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return fmt.Errorf("无效源图尺寸")
	}
	// 等比 fit 进画布
	scale := math.Min(float64(tw)/float64(sw), float64(th)/float64(sh))
	nw := int(math.Round(float64(sw) * scale))
	nh := int(math.Round(float64(sh) * scale))
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	// 白底
	for y := 0; y < th; y++ {
		for x := 0; x < tw; x++ {
			dst.Set(x, y, color.White)
		}
	}
	ox, oy := (tw-nw)/2, (th-nh)/2
	// 双线性缩放原图到 nw×nh，贴到 (ox,oy)
	xr := float64(sw) / float64(nw)
	yr := float64(sh) / float64(nh)
	for y := 0; y < nh; y++ {
		fy := float64(y) * yr
		y0 := int(fy)
		fyFrac := fy - float64(y0)
		y1 := y0 + 1
		if y1 >= sh {
			y1 = sh - 1
		}
		for x := 0; x < nw; x++ {
			fx := float64(x) * xr
			x0 := int(fx)
			fxFrac := fx - float64(x0)
			x1 := x0 + 1
			if x1 >= sw {
				x1 = sw - 1
			}
			r, g, bl := bilinearBlend(src, b, x0, x1, y0, y1, fxFrac, fyFrac)
			dst.Set(ox+x, oy+y, color.RGBA{R: r, G: g, B: bl, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return err
	}
	return os.WriteFile(outPath, buf.Bytes(), 0o644)
}

// bilinearBlend 对 (x0,y0)/(x1,y0)/(x0,y1)/(x1,y1) 四角双线性插值，透明像素与白合成。
func bilinearBlend(src image.Image, b image.Rectangle, x0, x1, y0, y1 int, fx, fy float64) (uint8, uint8, uint8) {
	c00 := whiteBackdrop(src.At(b.Min.X+x0, b.Min.Y+y0))
	c01 := whiteBackdrop(src.At(b.Min.X+x1, b.Min.Y+y0))
	c10 := whiteBackdrop(src.At(b.Min.X+x0, b.Min.Y+y1))
	c11 := whiteBackdrop(src.At(b.Min.X+x1, b.Min.Y+y1))
	lerp3 := func(r0, r1, r2, r3 float64, fx, fy float64) float64 {
		top := r0 + (r1-r0)*fx
		bot := r2 + (r3-r2)*fx
		return top + (bot-top)*fy
	}
	return uint8(lerp3(float64(c00[0]), float64(c01[0]), float64(c10[0]), float64(c11[0]), fx, fy)),
		uint8(lerp3(float64(c00[1]), float64(c01[1]), float64(c10[1]), float64(c11[1]), fx, fy)),
		uint8(lerp3(float64(c00[2]), float64(c01[2]), float64(c10[2]), float64(c11[2]), fx, fy))
}

// whiteBackdrop 取像素 RGB；若透明则与白底 alpha 合成，返回 [r,g,b]（0-255 非预乘）。
func whiteBackdrop(c color.Color) [3]uint8 {
	n, ok := c.(color.NRGBA)
	if !ok {
		n = color.NRGBAModel.Convert(c).(color.NRGBA)
	}
	if n.A == 255 {
		return [3]uint8{n.R, n.G, n.B}
	}
	af := float64(n.A) / 255.0
	blend := func(v uint8) uint8 {
		return uint8(float64(v)*af + 255.0*(1.0-af))
	}
	return [3]uint8{blend(n.R), blend(n.G), blend(n.B)}
}

// cropToRatio 将 src 居中裁剪到 tw:th 比例，再双线性缩放到 tw×th。
// 用于把 Agnes 输出的档位比例图还原为原图精确宽高比，消除"宽高比不对"。
// 主体因 letterbox 已居中，裁剪只会去掉档位溢出的背景边缘。
func cropToRatio(src image.Image, tw, th int) image.Image {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if tw <= 0 || th <= 0 || sw <= 0 || sh <= 0 {
		return src
	}
	tr := float64(tw) / float64(th)
	sr := float64(sw) / float64(sh)
	var cw, ch, x0, y0 int
	if sr > tr {
		// src 更宽，裁宽度
		ch = sh
		cw = int(float64(sh)*tr + 0.5)
		x0 = (sw - cw) / 2
		y0 = 0
	} else {
		// src 更高，裁高度
		cw = sw
		ch = int(float64(sw)/tr + 0.5)
		x0 = 0
		y0 = (sh - ch) / 2
	}
	if cw < 1 {
		cw = sw
	}
	if ch < 1 {
		ch = sh
	}
	cropBox := image.Rect(x0, y0, x0+cw, y0+ch)
	cropImg := image.NewRGBA(cropBox)
	draw.Draw(cropImg, cropImg.Bounds(), src, cropBox.Min, draw.Src)
	// 裁剪后比例 = tw:th，缩放到 tw×th 无失真
	return resizeBilinear(cropImg, tw, th)
}

// resizeBilinear 双线性缩放 src 到 tw×th（透明像素与白底合成）。
func resizeBilinear(src image.Image, tw, th int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 || tw <= 0 || th <= 0 {
		dst := image.NewRGBA(image.Rect(0, 0, max1(sw), max1(sh)))
		draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
		return dst
	}
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	xr := float64(sw) / float64(tw)
	yr := float64(sh) / float64(th)
	for y := 0; y < th; y++ {
		fy := float64(y) * yr
		y0 := int(fy)
		fyFrac := fy - float64(y0)
		y1 := y0 + 1
		if y1 >= sh {
			y1 = sh - 1
		}
		for x := 0; x < tw; x++ {
			fx := float64(x) * xr
			x0 := int(fx)
			fxFrac := fx - float64(x0)
			x1 := x0 + 1
			if x1 >= sw {
				x1 = sw - 1
			}
			r, g, bl := bilinearBlend(src, b, x0, x1, y0, y1, fxFrac, fyFrac)
			dst.Set(x, y, color.RGBA{R: r, G: g, B: bl, A: 255})
		}
	}
	return dst
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// MakeImageAI generates an image via AI API with Agnes fallback to SenseNova.
func MakeImageAI(client *AIClient, tmpDir, prompt string, width, height int) (string, error) {
	if width <= 0 {
		width = 1024
	}
	if height <= 0 {
		height = 1024
	}
	dest := filepath.Join(tmpDir, "generated.png")
	size := sizeToTier(width, height)
	ratio := ratioFromDims(width, height)

	if client.HasAgnes() {
		imgURL, b64, err := client.GenImageAgnes(agnesImageModel, prompt, size, ratio, nil)
		if err == nil {
			if dErr := client.DownloadImage(imgURL, b64, dest); dErr == nil {
				return dest, nil
			}
		}
	}
	if client.HasSenseNova() {
		imgURL, b64, err := client.GenImageSenseNova("sensenova-u1.5-lite", prompt, size, ratio, nil)
		if err == nil {
			if dErr := client.DownloadImage(imgURL, b64, dest); dErr == nil {
				return dest, nil
			}
		}
	}
	return "", fmt.Errorf("AI图片生成不可用")
}

// MakeEditedImageAI edits an image via AI API (image-to-image).
// 先把原图等比缩放放进跟随原图比例的 letterbox 画布（不足处留白），再发给 AI，
// 主体不被拉伸；sizeKey 为 1k/2k/4k/original，origW/origH 为上传原图宽高。
func MakeEditedImageAI(client *AIClient, tmpDir, srcPath, prompt, sizeKey string, origW, origH int) (string, error) {
	dest := filepath.Join(tmpDir, "edited.png")
	tier, ratio, cw, ch := ResolveEditDims(sizeKey, origW, origH)

	// letterbox：原图按目标画布比例等比缩放居中、留白；解码失败退回原图
	src := srcPath
	if lbPath := filepath.Join(tmpDir, "src_letterbox.png"); true {
		if err := letterboxImage(srcPath, lbPath, cw, ch); err == nil {
			src = lbPath
		}
	}

	dataURI, err := FileToDataURI(src)
	if err != nil {
		return "", fmt.Errorf("读取源图片失败: %v", err)
	}

	rawDest := filepath.Join(tmpDir, "edited_raw.png")
	got := false
	if client.HasAgnes() {
		if imgURL, b64, e := client.GenImageAgnes(agnesImageModel, prompt, tier, ratio, []string{dataURI}); e == nil {
			if client.DownloadImage(imgURL, b64, rawDest) == nil {
				got = true
			}
		}
	}
	if !got && client.HasSenseNova() {
		if imgURL, b64, e := client.GenImageSenseNova("sensenova-u1.5-lite", prompt, tier, ratio, []string{dataURI}); e == nil {
			if client.DownloadImage(imgURL, b64, rawDest) == nil {
				got = true
			}
		}
	}
	if !got {
		return "", fmt.Errorf("AI图片编辑不可用")
	}

	// 有原图尺寸：把 Agnes 档位图居中裁剪回原图精确宽高比，消除档位偏差
	if origW > 0 && origH > 0 {
		f, fErr := os.Open(rawDest)
		if fErr != nil {
			return "", fErr
		}
		img, _, dErr := image.Decode(f)
		f.Close()
		if dErr != nil {
			return "", dErr
		}
		final := cropToRatio(img, origW, origH)
		out, oErr := os.Create(dest)
		if oErr != nil {
			return "", oErr
		}
		if pErr := png.Encode(out, final); pErr != nil {
			out.Close()
			return "", pErr
		}
		out.Close()
		os.Remove(rawDest)
	} else {
		// 无原图尺寸信息：直接用档位图
		if rErr := os.Rename(rawDest, dest); rErr != nil {
			return "", rErr
		}
	}
	return dest, nil
}

// MakeEditedImageComposed 换背景：
// 1) 用 AI 生成一张"空场景"背景（跟随原图比例档位），裁剪回原图精确尺寸；
// 2) 把原图 + 背景图一起发给 AI 做合成（AI 能正确处理发丝边缘，优于本地 rembg 320x320 二值 mask）；
// 3) AI 合成失败时退回本地 compose_bg.py（rembg），再失败退回 MakeEditedImageAI（图生图）。
func MakeEditedImageComposed(client *AIClient, tmpDir, srcPath, prompt, sizeKey string, origW, origH int) (string, error) {
	if client == nil || origW <= 0 || origH <= 0 || (!client.HasAgnes() && !client.HasSenseNova()) {
		return MakeEditedImageAI(client, tmpDir, srcPath, prompt, sizeKey, origW, origH)
	}

	// 1. 生成空场景背景（档位比例），再裁剪回原图精确宽高比
	tier, ratio, _, _ := ResolveEditDims(sizeKey, origW, origH)
	bgPrompt := backgroundPrompt(prompt)
	bgRaw := filepath.Join(tmpDir, "bg_raw.png")
	got := false
	if client.HasAgnes() {
		if u, b, e := client.GenImageAgnes(agnesImageModel, bgPrompt, tier, ratio, nil); e == nil {
			if client.DownloadImage(u, b, bgRaw) == nil {
				got = true
			}
		}
	}
	if !got && client.HasSenseNova() {
		if u, b, e := client.GenImageSenseNova("sensenova-u1.5-lite", bgPrompt, tier, ratio, nil); e == nil {
			if client.DownloadImage(u, b, bgRaw) == nil {
				got = true
			}
		}
	}
	if !got {
		return MakeEditedImageAI(client, tmpDir, srcPath, prompt, sizeKey, origW, origH)
	}

	bgOut := filepath.Join(tmpDir, "bg.png")
	f, ferr := os.Open(bgRaw)
	if ferr != nil {
		return MakeEditedImageAI(client, tmpDir, srcPath, prompt, sizeKey, origW, origH)
	}
	img, _, derr := image.Decode(f)
	f.Close()
	if derr != nil {
		return MakeEditedImageAI(client, tmpDir, srcPath, prompt, sizeKey, origW, origH)
	}
	if of, oerr := os.Create(bgOut); oerr != nil {
		return MakeEditedImageAI(client, tmpDir, srcPath, prompt, sizeKey, origW, origH)
	} else {
		_ = png.Encode(of, cropToRatio(img, origW, origH))
		of.Close()
	}

	// 2. AI 合成：原图 + 背景图 → AI 合成（发丝边缘自然，优于本地 rembg 320x320 二值 mask）
	dest := filepath.Join(tmpDir, "edited.png")
	srcURI, srcErr := FileToDataURI(srcPath)
	bgURI, bgErr := FileToDataURI(bgOut)
	if srcErr == nil && bgErr == nil && client.HasAgnes() {
		compositePrompt := prompt + ", place the person from the first reference image onto the background of the second reference image, keep the person identical, only change the background"
		if imgURL, b64, e := client.GenImageAgnes(agnesImageModel, compositePrompt, tier, ratio, []string{srcURI, bgURI}); e == nil {
			if dErr := client.DownloadImage(imgURL, b64, dest); dErr == nil {
				// 裁剪回原图精确宽高比
				if ff, ffErr := os.Open(dest); ffErr == nil {
					if im, _, dd := image.Decode(ff); dd == nil {
						ff.Close()
						if oo, ooErr := os.Create(dest); ooErr == nil {
							_ = png.Encode(oo, cropToRatio(im, origW, origH))
							oo.Close()
						}
					} else {
						ff.Close()
					}
				}
				return dest, nil
			}
		}
	}
	log.Printf("[compose-edit] AI合成失败，退回本地 rembg 合成")

	// 3. 退回本地 compose_bg.py（rembg）
	const composeTimeout = 120 * time.Second
	outJSON, perr := RunCmdTimeout(composeTimeout, PythonPath(), ScriptPath("compose_bg.py"), srcPath, bgOut, dest)
	var res struct {
		Ok       bool    `json:"ok"`
		Error    string  `json:"error"`
		Coverage float64 `json:"coverage"`
	}
	if perr == nil && json.Unmarshal([]byte(strings.TrimSpace(outJSON)), &res) == nil && res.Ok {
		return dest, nil
	}
	// 本地合成也失败：退回图生图
	log.Printf("[compose-edit] 本地合成失败(perr=%v out=%s)，退回图生图", perr, strings.TrimSpace(outJSON))
	return MakeEditedImageAI(client, tmpDir, srcPath, prompt, sizeKey, origW, origH)
}

// backgroundPrompt 把用户"换背景"描述转成"生成空场景背景"提示词（明确不含人物）。
func backgroundPrompt(userPrompt string) string {
	p := strings.TrimSpace(userPrompt)
	return p + ", empty scene background, no people, no human, no person, wide angle, photorealistic, high detail"
}

// MakeComposeImageAI composes multiple reference images via AI API.
func MakeComposeImageAI(client *AIClient, tmpDir, prompt string, refPaths []string, width, height int) (string, error) {
	dest := filepath.Join(tmpDir, "composed.png")
	size := sizeToTier(width, height)
	ratio := ratioFromDims(width, height)

	var dataURIs []string
	for _, p := range refPaths {
		uri, err := FileToDataURI(p)
		if err != nil {
			continue
		}
		dataURIs = append(dataURIs, uri)
	}

	if client.HasAgnes() {
		imgURL, b64, err := client.GenImageAgnes(agnesImageModel, prompt, size, ratio, dataURIs)
		if err == nil {
			if dErr := client.DownloadImage(imgURL, b64, dest); dErr == nil {
				return dest, nil
			}
		}
	}
	if client.HasSenseNova() {
		imgURL, b64, err := client.GenImageSenseNova("sensenova-u1.5-lite", prompt, size, ratio, dataURIs)
		if err == nil {
			if dErr := client.DownloadImage(imgURL, b64, dest); dErr == nil {
				return dest, nil
			}
		}
	}
	return "", fmt.Errorf("AI图片合成不可用")
}
