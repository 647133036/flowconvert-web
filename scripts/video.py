#!/usr/bin/env python3
"""Video generation script for FlowConvert.
Generates MP4 videos using Pillow for frame rendering and ffmpeg for encoding.

Modes:
  text      - Generate animated video from text prompt
  keyframe  - Interpolate between two keyframe images
  ref       - Generate video using reference images as style/motion guides
"""

import sys
import json
import os
import math
import hashlib
import subprocess
import tempfile
import shutil

from PIL import Image, ImageDraw, ImageFilter
import numpy as np

FPS = 24


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


def render_text_video(payload, dest):
    """Generate a video from a text prompt."""
    prompt = payload.get("prompt", "")
    duration = int(payload.get("duration", 5))
    duration = max(2, min(duration, 60))
    total_frames = duration * FPS
    w, h = 1280, 720

    seed = hash_seed(prompt) if prompt else hash_seed("default")
    tmp_dir = tempfile.mkdtemp(prefix="fc_video_")
    try:
        for i in range(total_frames):
            img = render_frame_gradient(w, h, i, total_frames, seed, prompt)
            img.save(os.path.join(tmp_dir, f"frame_{i:06d}.png"))

        encode_video(tmp_dir, dest, total_frames)
    finally:
        shutil.rmtree(tmp_dir, ignore_errors=True)

    print(json.dumps({"status": "ok", "frames": total_frames}))


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
    tmp_dir = tempfile.mkdtemp(prefix="fc_kf_")
    try:
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

            frame.save(os.path.join(tmp_dir, f"frame_{i:06d}.png"))

        encode_video(tmp_dir, dest, total_frames)
    finally:
        shutil.rmtree(tmp_dir, ignore_errors=True)

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

    tmp_dir = tempfile.mkdtemp(prefix="fc_ref_")
    try:
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

            img.save(os.path.join(tmp_dir, f"frame_{i:06d}.png"))

        encode_video(tmp_dir, dest, total_frames)
    finally:
        shutil.rmtree(tmp_dir, ignore_errors=True)

    print(json.dumps({"status": "ok", "frames": total_frames}))


def encode_video(frame_dir, dest, total_frames):
    """Encode a directory of PNG frames into an MP4 video using ffmpeg."""
    pattern = os.path.join(frame_dir, "frame_%06d.png")
    cmd = [
        "ffmpeg", "-y",
        "-framerate", str(FPS),
        "-i", pattern,
        "-c:v", "libx264",
        "-pix_fmt", "yuv420p",
        "-crf", "23",
        "-preset", "fast",
        "-movflags", "+faststart",
        dest,
    ]
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=300)
    except subprocess.TimeoutExpired:
        raise RuntimeError("ffmpeg 编码超时（300秒）")
    if result.returncode != 0:
        raise RuntimeError(f"ffmpeg 编码失败: {result.stderr[-500:]}")
    if not os.path.exists(dest):
        raise RuntimeError("视频编码未生成输出文件")


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
