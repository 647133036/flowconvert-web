package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand"
	"os"
	"path/filepath"
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
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("解码失败: %v", err)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if width > 0 {
		w = width
	}
	if height > 0 {
		h = height
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(x-b.Min.X, y-b.Min.Y, src.At(x, y))
		}
	}
	seed := hashToSeed(prompt + "_edit")
	rng := rand.New(rand.NewSource(int64(seed)))
	switch rng.Intn(4) {
	case 0:
		applySepia(dst, w, h)
	case 1:
		applyInvert(dst, w, h)
	case 2:
		applyBlur(dst, w, h, rng)
	case 3:
		applyPosterize(dst, w, h, rng)
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
		ref, err := png.Decode(bytes.NewReader(d))
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
				wave := math.Sin(float64(x)*freq+phase+float64(y)*spd)*amp
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
	nw := int(float64(sw)*scale)
	nh := int(float64(sh)*scale)
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
			fact := float64(sa)/65535.0 * alpha
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
