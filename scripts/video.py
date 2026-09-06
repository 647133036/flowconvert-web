#!/usr/bin/env python3
"""Video generation script for FlowConvert.
Generates MP4 videos using Pillow for frame rendering and ffmpeg for encoding.

Modes:
  text      - Generate animated video from text prompt
  keyframe  - Interpolate between two keyframe images
  ref       - Generate video using reference images as style/motion guides
"""

import sys
import re
import json
import os
import math
import hashlib
import subprocess

from PIL import Image, ImageDraw, ImageFilter, ImageFont
import numpy as np

FPS = 24


class VideoWriter:
    """Stream raw RGB frames straight into ffmpeg over stdin.

    The previous path wrote every frame as a PNG file and then had ffmpeg read
    them back: PNG compression alone cost ~0.3s per 720p frame, which is why a
    40s fallback video took ~8 minutes. Piping rawvideo skips files entirely.
    """

    def __init__(self, dest, w, h):
        self._dest = dest
        self._closed = False
        self.proc = subprocess.Popen(
            ["ffmpeg", "-y", "-f", "rawvideo", "-pix_fmt", "rgb24",
             "-s", f"{w}x{h}", "-framerate", str(FPS), "-i", "-",
             "-c:v", "libx264", "-pix_fmt", "yuv420p", "-crf", "23",
             "-preset", "fast", "-movflags", "+faststart", dest],
            stdin=subprocess.PIPE, stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE)

    def write(self, img):
        self.proc.stdin.write(np.asarray(img.convert("RGB"), dtype=np.uint8).tobytes())

    def close(self):
        if self._closed:
            return
        self._closed = True
        self.proc.stdin.close()
        self.proc.wait(timeout=180)
        if self.proc.returncode != 0:
            err = self.proc.stderr.read().decode(errors="replace")
            raise RuntimeError(f"ffmpeg 编码失败: {err[-500:]}")
        if not os.path.exists(self._dest):
            raise RuntimeError("视频编码未生成输出文件")

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        # Killing ffmpeg mid-stream on the success path leaves the MP4 without
        # a moov atom: an unplayable file that looks fine by size. Finalize
        # cleanly instead; only kill when the block raised.
        if exc_type is None:
            self.close()
        elif self.proc.poll() is None:
            try:
                self.proc.stdin.close()
            except BrokenPipeError:
                pass
            self.proc.kill()
        return False

# Fonts tried in order. WenQuanYi Zen Hei covers CJK; DejaVu is a Latin
# fallback so a missing CJK font degrades to boxes instead of crashing.
FONT_CANDIDATES = [
    "/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc",
    "/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
    "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
]

# Keyword -> (top, mid, bottom) palette. The first matching group wins, so
# order matters: strong season/fire cues are listed before the generic
# "forest / tree / green" group, which would otherwise steal autumn prompts.
SCENE_PALETTES = [
    (["雪", "冬", "冰", "霜", "snow", "winter", "ice"],
     (205, 228, 255), (118, 168, 228), (34, 62, 110)),
    (["火", "爆炸", "焰", "fire", "flame", "explode"],
     (252, 168, 84), (212, 56, 52), (66, 18, 42)),
    (["秋", "枫", "夕", "落日", "日落", "黄昏", "晚霞", "sunset", "dusk", "autumn"],
     (250, 158, 96), (214, 74, 62), (58, 26, 54)),
    (["红", "red"], (240, 108, 118), (186, 52, 74), (72, 22, 44)),
    (["森林", "树", "绿", "春", "草", "野", "leaf", "forest", "green"],
     (168, 214, 148), (58, 148, 88), (14, 58, 44)),
    (["海", "湖", "河", "水", "波", "ocean", "water", "river", "sea"],
     (158, 214, 240), (58, 138, 218), (14, 48, 108)),
    (["夜", "星", "月", "暗", "night", "star", "moon"],
     (96, 86, 176), (44, 40, 108), (8, 8, 38)),
    (["太空", "宇宙", "space", "galaxy", "cosmos"],
     (124, 96, 204), (48, 30, 120), (6, 5, 32)),
    (["花", "樱", "粉", "pink", "rose"], (250, 186, 204), (226, 122, 158), (86, 40, 76)),
    (["沙漠", "沙", "desert", "sand"], (236, 208, 142), (206, 148, 70), (104, 58, 28)),
    (["蓝", "blue"], (160, 200, 240), (62, 122, 206), (18, 44, 96)),
    (["金", "黄", "gold", "yellow"], (244, 214, 120), (214, 158, 52), (92, 60, 20)),
    (["光", "光晕", "light", "glow", "bright"], (196, 226, 240), (120, 172, 214), (36, 66, 108)),
]

DEFAULT_PALETTE = ((140, 202, 220), (72, 112, 198), (20, 40, 88))


def load_font(size):
    for path in FONT_CANDIDATES:
        if os.path.exists(path):
            try:
                return ImageFont.truetype(path, size)
            except Exception:
                pass
    return ImageFont.load_default()


def palette_for_prompt(prompt, seed):
    """Pick a scene palette from prompt keywords, falling back to a
    deterministic hue rotation so different prompts still differ."""
    p = (prompt or "").lower()
    for keys, top, mid, bottom in SCENE_PALETTES:
        if any(k in p for k in keys):
            return top, mid, bottom
    rng = np.random.RandomState(seed)
    base = rng.uniform(0, 12, 3)
    out = []
    for i, v in enumerate(base):
        c = hsl_to_rgb(v * 30, 0.55, 0.30 + 0.16 * (2 - i) * 0.4)
        out.append(c)
    return tuple(out)


def wrap_text(text, max_chars):
    """Break CJK/ASCII text into display lines. Chinese has no word spaces,
    so we prefer to break after punctuation and otherwise hard-break."""
    text = re.sub(r"\s+", " ", (text or "")).strip()
    if not text:
        return [""]
    break_after = "，。！？；、,.!?;…"
    lines, cur = [], ""
    for ch in text:
        cur += ch
        if len(cur) >= max_chars and ch in break_after:
            lines.append(cur.rstrip())
            cur = ""
        elif len(cur) >= max_chars + 4:
            lines.append(cur.rstrip())
            cur = ""
    if cur.strip():
        lines.append(cur.rstrip())
    return lines or [""]


def build_gradient(w, h, top, mid, bottom):
    """A 3-stop vertical gradient with a gentle horizontal drift, so the
    frame never reads as a flat band."""
    ys = np.linspace(0.0, 1.0, h, dtype=np.float32)
    xs = np.linspace(0.0, 1.0, w, dtype=np.float32)
    grad = np.empty((h, w, 3), dtype=np.float32)
    drift = 0.045 * np.sin(xs[None, :] * math.pi + 0.35)[:, None]
    for c in range(3):
        col = np.interp(ys, [0, 0.55, 1.0], [top[c], mid[c], bottom[c]])[:, None]
        grad[:, :, c] = np.clip(col + drift * 60.0, 0, 255)
    return grad


def build_blob(radius):
    """Soft radial light blob sprite; larger radius costs quadratic memory."""
    r = np.linspace(-1, 1, 2 * radius + 1, dtype=np.float32)
    gx, gy = np.meshgrid(r, r)
    d = np.sqrt(gx * gx + gy * gy)
    return np.clip(1.0 - d, 0, 1) ** 2.4


def add_blob(frame, cx, cy, radius, color, intensity, blob):
    """Add a prebuilt blob at frame point (cx, cy).

    The blob is (2*radius+1) x (2*radius+1) with its centre at index radius.
    The frame rectangle is clamped to the canvas first and the same clamps are
    mirrored back into blob space, which keeps patch and target the exact same
    shape — clipping only one side used to leave them mismatched.
    """
    n = blob.shape[0]
    half = n // 2
    fh, fw = frame.shape[:2]
    fx0, fx1 = max(0, cx - half), min(fw, cx + half + 1)
    fy0, fy1 = max(0, cy - half), min(fh, cy + half + 1)
    if fx1 <= fx0 or fy1 <= fy0:
        return
    bx0, bx1 = fx0 - cx + half, fx1 - cx + half
    by0, by1 = fy0 - cy + half, fy1 - cy + half
    if bx0 < 0:
        fx0 += -bx0
        bx0 = 0
    if by0 < 0:
        fy0 += -by0
        by0 = 0
    if bx1 > n:
        fx1 -= bx1 - n
        bx1 = n
    if by1 > n:
        fy1 -= by1 - n
        by1 = n
    patch = blob[by0:by1, bx0:bx1]
    frame[fy0:fy1, fx0:fx1] += patch[:, :, None] * intensity * np.array(
        color, dtype=np.float32
    )


def render_caption_lines(w, h, text):
    """Render the prompt as one RGBA layer per line, so the caller can reveal
    lines progressively across the clip instead of showing one static card.
    A 40s fallback then has visible things happening the whole time."""
    max_chars = 20 if w >= 1100 else 13
    all_lines = wrap_text(text, max_chars)
    lines = all_lines[:6]  # keep the card readable rather than overflowing
    if len(all_lines) > len(lines):
        lines[-1] = lines[-1][:-1] + "…"

    # Pick the largest font that still fits, and measure the real ink box so
    # the backdrop hugs the text instead of the line boxes.
    font, metrics = None, None
    for cand in range(68, 28, -2):
        f = load_font(cand)
        gap = int(cand * 0.46)
        boxes = [f.getbbox(l) for l in lines]
        ink_h = sum(b[3] - b[1] for b in boxes) + gap * max(0, len(lines) - 1)
        ink_w = max((b[2] - b[0]) for b in boxes)
        if ink_h <= h * 0.58 and ink_w <= w * 0.86:
            font, metrics = f, (boxes, gap, ink_h, ink_w)
            break
    if font is None:
        font = load_font(30)
        boxes = [font.getbbox(l) for l in lines]
        gap = int(30 * 0.46)
        metrics = (boxes, gap, h * 0.58, w * 0.8)
    boxes, gap, ink_h, ink_w = metrics

    size = font.size
    pad = int(size * 0.8)
    cx, cy = w // 2, h // 2
    layers = []
    y = cy - ink_h // 2
    for li, line in enumerate(lines):
        if li:
            y += gap
        l, t, r, b = boxes[li]
        ink_w_l = r - l
        x = cx - ink_w_l // 2 - l
        strip = Image.new("RGBA", (w, h), (0, 0, 0, 0))
        d = ImageDraw.Draw(strip)
        d.rounded_rectangle(
            [cx - ink_w_l // 2 - pad, y - pad // 2,
             cx + ink_w_l // 2 + pad, y + (b - t) + pad // 2],
            radius=int(size * 0.4), fill=(0, 0, 0, 108))
        for dx, dy in ((0, 0), (1, 1), (2, 2)):
            d.text((x + dx, y - t + dy), line, font=font, fill=(0, 0, 0, 200))
        d.text((x, y - t), line, font=font, fill=(255, 255, 255, 248))
        layers.append(strip)
        y += (b - t)
    return layers


def render_label(w, text="本地合成预览 · AI 服务暂不可用"):
    """Small top banner making clear this is a local fallback, not AI output."""
    font = load_font(24)
    l, t, r, b = font.getbbox(text)
    tw, th = r - l, b - t
    strip = Image.new("RGBA", (tw + 44, th + 22), (0, 0, 0, 0))
    d = ImageDraw.Draw(strip)
    d.rounded_rectangle([0, 0, tw + 43, th + 21], radius=16, fill=(0, 0, 0, 96))
    d.text((22 - l, 11 - t), text, font=font, fill=(255, 255, 255, 214))
    return strip


def build_vignette(w, h):
    ys, xs = np.mgrid[0:h, 0:w].astype(np.float32)
    cx, cy = w / 2.0, h / 2.0
    d = np.sqrt(((xs - cx) / cx) ** 2 + ((ys - cy) / cy) ** 2)
    return np.clip((d - 0.55) / 0.85, 0, 1) ** 1.4 * 0.42


def render_text_video(payload, dest):
    """Generate a motion title card that actually shows the prompt.

    The previous implementation hashed the prompt into a random gradient or
    particle field, so the output had no relation to what the user asked for.
    This renders a scene-tinted background with drifting light, the prompt as
    a shadowed caption, and a progress bar so the card reads as a video.
    """
    prompt = (payload.get("prompt") or "").strip()
    duration = int(payload.get("duration", 5))
    duration = max(2, min(duration, 60))
    aspect = str(payload.get("aspect_ratio") or "16:9").strip().lower()

    dims = {"16:9": (1280, 720), "9:16": (720, 1280), "1:1": (960, 960),
            "4:3": (1120, 840), "3:4": (840, 1120)}
    w, h = dims.get(aspect, (1280, 720))

    seed = hash_seed(prompt) if prompt else hash_seed("default")
    top, mid, bottom = palette_for_prompt(prompt, seed)

    total_frames = duration * FPS
    base = build_gradient(w, h, top, mid, bottom)
    vignette = build_vignette(w, h)
    line_layers = render_caption_lines(w, h, prompt)
    label_layer = render_label(w)
    label_x, label_y = (w - label_layer.width) // 2, 30
    n_lines = len(line_layers)

    rng = np.random.RandomState(seed)
    n_blobs = 4 + int(rng.randint(0, 3))
    blob_r = max(40, min(w, h) // 4)
    blob = build_blob(blob_r)
    specs = []
    for i in range(n_blobs):
        specs.append(dict(
            cx0=rng.uniform(0.1, 0.9), cy0=rng.uniform(0.1, 0.9),
            speed=rng.uniform(0.05, 0.22), phase=rng.uniform(0, math.pi * 2),
            amp=rng.uniform(0.06, 0.20),
            col=(int(top[0] * rng.uniform(0.8, 1.3)) % 256,
                 int(top[1] * rng.uniform(0.8, 1.3)) % 256,
                 int(top[2] * rng.uniform(0.8, 1.3)) % 256),
            intensity=float(rng.uniform(26, 70)),
        ))

    # A handful of prebuilt noise tiles cycled per frame: real film-grain
    # shimmer at a fraction of the cost of generating noise every frame.
    grain_tiles = [rng.normal(0, 2.6, (h, w)).astype(np.float32) for _ in range(3)]

    bar_h = max(3, h // 240)
    with VideoWriter(dest, w, h) as vw:
        for i in range(total_frames):
            t = i / max(total_frames - 1, 1)
            frame = base.copy()
            # Slow hue-ish drift keeps the background alive without flicker.
            frame += np.float32(6.0 * math.sin(t * math.pi * 2 + seed % 7))

            for s in specs:
                ang = t * s["speed"] * math.pi * 2 + s["phase"]
                cx = int((s["cx0"] + s["amp"] * math.sin(ang)) * w)
                cy = int((s["cy0"] + s["amp"] * 0.7 * math.cos(ang)) * h)
                add_blob(frame, cx, cy, blob_r, s["col"], s["intensity"], blob)

            frame -= vignette[:, :, None] * 255.0
            frame += grain_tiles[i % len(grain_tiles)][:, :, None]

            img = Image.fromarray(np.clip(frame, 0, 255).astype(np.uint8)).convert("RGBA")

            # Banner fades in first so the viewer immediately knows this is a
            # fallback preview rather than the requested AI video.
            la = min(1.0, t / 0.04)
            if la > 0.002:
                lay = label_layer
                if la < 0.999:
                    lay = label_layer.copy()
                    lay.putalpha(lay.getchannel("A").point(lambda a: int(a * la)))
                img.alpha_composite(lay, (label_x, label_y))

            # Lines reveal one after another across the first ~70% of the clip.
            for li, layer in enumerate(line_layers):
                start = 0.05 + 0.65 * li / max(n_lines, 1)
                a = (t - start) / 0.05
                if a <= 0:
                    continue
                a = min(1.0, a)
                lay = layer
                if a < 0.999:
                    lay = layer.copy()
                    lay.putalpha(lay.getchannel("A").point(lambda v: int(v * a)))
                img.alpha_composite(lay, (0, 0))

            img = img.convert("RGB")

            # Progress bar: communicates that time is actually passing.
            draw = ImageDraw.Draw(img)
            fw = int((w - 120) * t) + 1
            draw.rectangle([60, h - bar_h - 14, 60 + fw, h - 14], fill=(255, 255, 255))
            draw.rectangle([60, h - bar_h - 14, w - 60, h - 14],
                           outline=(255, 255, 255), width=1)

            vw.write(img)

    print(json.dumps({"status": "ok", "frames": total_frames}))


def hash_seed(s: str) -> int:
    h = hashlib.sha256(s.encode()).hexdigest()
    return int(h[:8], 16)


def hsl_to_rgb(h, s, l):
    h = (h % 360) / 360.0
    if s == 0:
        r = g = b = l
    else:
        q = l * (1 + s) if l < 0.5 else l + s - l * s
        p = 2 * l - q

        def hue2rgb(p, q, t):
            if t < 0:
                t += 1
            if t > 1:
                t -= 1
            if t < 1.0 / 6:
                return p + (q - p) * 6 * t
            if t < 1.0 / 2:
                return q
            if t < 2.0 / 3:
                return p + (q - p) * (2.0 / 3 - t) * 6
            return p

        r = hue2rgb(p, q, h + 1.0 / 3)
        g = hue2rgb(p, q, h)
        b = hue2rgb(p, q, h - 1.0 / 3)
    return int(r * 255), int(g * 255), int(b * 255)


def render_frame_gradient(w, h, frame_idx, total_frames, seed, prompt):
    """Render a gradient animation frame."""
    rng = np.random.RandomState(seed)
    base_hue = rng.uniform(0, 360)
    style = seed % 4
    t = frame_idx / max(total_frames - 1, 1)

    img = Image.new("RGB", (w, h))
    draw = ImageDraw.Draw(img)

    if style == 0:
        # Moving gradient
        for y in range(h):
            ratio = y / h
            hue = (base_hue + ratio * 120 + t * 60) % 360
            r, g, b = hsl_to_rgb(hue, 0.7, 0.3 + 0.2 * ratio)
            draw.line([(0, y), (w, y)], fill=(r, g, b))

    elif style == 1:
        # Pulsing circles
        for y in range(h):
            r, g, b = hsl_to_rgb(base_hue, 0.4, 0.1)
            draw.line([(0, y), (w, y)], fill=(r, g, b))
        num_circles = 5 + seed % 4
        for i in range(num_circles):
            cx = w * (0.2 + 0.6 * ((seed * (i + 1)) % 100) / 100)
            cy = h * (0.2 + 0.6 * ((seed * (i + 2)) % 100) / 100)
            phase = t * 2 * math.pi + i * 0.5
            radius = int(30 + 50 * (1 + math.sin(phase)) / 2)
            hue = (base_hue + i * 40) % 360
            r, g, b = hsl_to_rgb(hue, 0.8, 0.5)
            draw.ellipse([cx - radius, cy - radius, cx + radius, cy + radius],
                         fill=(r, g, b), outline=None)

    elif style == 2:
        # Wave animation
        for y in range(h):
            r, g, b = hsl_to_rgb(base_hue, 0.3, 0.08)
            draw.line([(0, y), (w, y)], fill=(r, g, b))
        for wi in range(4):
            amp = 20 + wi * 10
            freq = 0.01 + wi * 0.003
            phase = t * 2 * math.pi + wi * 1.0
            hue = (base_hue + wi * 45) % 360
            points = []
            for x in range(0, w, 2):
                y_off = math.sin(x * freq + phase) * amp
                points.append((x, h // 2 + y_off))
            if len(points) > 1:
                r, g, b = hsl_to_rgb(hue, 0.8, 0.6)
                draw.line(points, fill=(r, g, b), width=3)

    else:
        # Particle field
        for y in range(h):
            r, g, b = hsl_to_rgb(base_hue, 0.2, 0.05)
            draw.line([(0, y), (w, y)], fill=(r, g, b))
        num_particles = 40
        for i in range(num_particles):
            px = (seed * (i + 1) % 1000) / 1000 * w
            py = (seed * (i + 3) % 800) / 800 * h
            move_x = math.sin(t * 2 * math.pi + i * 0.3) * 30
            move_y = math.cos(t * 2 * math.pi + i * 0.2) * 20
            x = int(px + move_x) % w
            y = int(py + move_y) % h
            radius = 3 + (seed >> (i % 8)) % 5
            hue = (base_hue + i * 15) % 360
            r, g, b = hsl_to_rgb(hue, 0.9, 0.6)
            draw.ellipse([x - radius, y - radius, x + radius, y + radius],
                         fill=(r, g, b))

    return img


def render_keyframe_video(payload, dest):
    """Generate a video by interpolating between two keyframe images."""
    first_path = payload.get("first", "")
    last_path = payload.get("last", "")
    prompt = payload.get("prompt", "")
    duration = int(payload.get("duration", 5))
    duration = max(2, min(duration, 60))
    total_frames = duration * FPS

    if not os.path.exists(first_path):
        print(json.dumps({"error": f"首帧图片不存在: {first_path}"}))
        sys.exit(1)
    if not os.path.exists(last_path):
        print(json.dumps({"error": f"尾帧图片不存在: {last_path}"}))
        sys.exit(1)

    first = Image.open(first_path).convert("RGB")
    last = Image.open(last_path).convert("RGB")

    # Use a common resolution based on the first image aspect ratio
    w, h = first.size
    max_dim = 1280
    if max(w, h) > max_dim:
        scale = max_dim / max(w, h)
        w, h = int(w * scale), int(h * scale)
    w = max(w, 320)
    h = max(h, 180)
    # libx264 requires even dimensions
    w = w if w % 2 == 0 else w + 1
    h = h if h % 2 == 0 else h + 1

    first = first.resize((w, h), Image.LANCZOS)
    last = last.resize((w, h), Image.LANCZOS)

    arr_first = np.array(first, dtype=np.float32)
    arr_last = np.array(last, dtype=np.float32)

    seed = hash_seed(prompt) if prompt else 42
    with VideoWriter(dest, w, h) as vw:
        for i in range(total_frames):
            t = i / max(total_frames - 1, 1)
            # Ease in-out interpolation
            eased = t * t * (3 - 2 * t)
            # Add subtle motion: zoom and pan
            zoom = 1.0 + 0.05 * math.sin(t * math.pi)
            pan_x = int(10 * math.sin(t * 2 * math.pi))
            pan_y = int(5 * math.cos(t * 2 * math.pi))

            arr = arr_first * (1 - eased) + arr_last * eased
            frame = Image.fromarray(np.clip(arr, 0, 255).astype(np.uint8))

            # Apply zoom/pan effect
            zw = int(w * zoom)
            zh = int(h * zoom)
            zoomed = frame.resize((zw, zh), Image.LANCZOS)
            left = max(0, (zw - w) // 2 + pan_x)
            top = max(0, (zh - h) // 2 + pan_y)
            frame = zoomed.crop((left, top, left + w, top + h))

            vw.write(frame)

    print(json.dumps({"status": "ok", "frames": total_frames}))


def render_ref_video(payload, dest):
    """Generate a video using reference images as style/motion guides."""
    prompt = payload.get("prompt", "")
    refs = payload.get("refs", [])
    duration = int(payload.get("duration", 5))
    duration = max(2, min(duration, 60))
    total_frames = duration * FPS
    w, h = 1280, 720

    seed = hash_seed(prompt) if prompt else 12345

    # Load reference images
    ref_images = []
    for rp in refs:
        if os.path.exists(rp):
            img = Image.open(rp).convert("RGB")
            ref_images.append(img)

    with VideoWriter(dest, w, h) as vw:
        for i in range(total_frames):
            t = i / max(total_frames - 1, 1)
            img = render_frame_gradient(w, h, i, total_frames, seed, prompt)

            # Overlay reference images with animation
            for idx, ref in enumerate(ref_images):
                ref_resized = ref.resize((w // 3, h // 3), Image.LANCZOS)
                # Animate position
                x = int(w * (0.2 + 0.6 * ((t + idx * 0.3) % 1.0)))
                y = int(h * (0.2 + 0.3 * math.sin(t * 2 * math.pi + idx)))
                # Blend with transparency
                overlay = Image.new("RGB", img.size, (0, 0, 0))
                overlay.paste(ref_resized, (x, y))
                arr_img = np.array(img, dtype=np.float32)
                arr_overlay = np.array(overlay, dtype=np.float32)
                alpha = 0.3
                blended = arr_img * (1 - alpha) + arr_overlay * alpha
                img = Image.fromarray(np.clip(blended, 0, 255).astype(np.uint8))

            vw.write(img)

    print(json.dumps({"status": "ok", "frames": total_frames}))


def main():
    if len(sys.argv) < 4:
        print(json.dumps({"error": "参数不足，用法: video.py <mode> <payload> <dest>"}))
        sys.exit(1)

    mode = sys.argv[1]
    payload_path = sys.argv[2]
    dest = sys.argv[3]

    with open(payload_path, "r", encoding="utf-8") as f:
        payload = json.load(f)

    if mode == "text":
        render_text_video(payload, dest)
    elif mode == "keyframe":
        render_keyframe_video(payload, dest)
    elif mode == "ref":
        render_ref_video(payload, dest)
    else:
        print(json.dumps({"error": f"未知模式: {mode}"}))
        sys.exit(1)


if __name__ == "__main__":
    main()
