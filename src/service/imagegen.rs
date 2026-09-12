use std::path::PathBuf;
use std::time::Duration;

use image::{DynamicImage, GenericImage, GenericImageView, ImageBuffer, Rgba};
use crate::service::aiclient::{AIClient, AGNES_IMAGE_MODEL};
use crate::util::{python_path, run_cmd_timeout, script_path};


/// Procedural abstract image generation using image crate.
pub fn make_image(tmp_dir: &str, prompt: &str, width: i32, height: i32) -> Result<String, String> {
    let width = if width <= 0 { 1024 } else if width > 4096 { 4096 } else { width };
    let height = if height <= 0 { 1024 } else if height > 4096 { 4096 } else { height };

    let seed = hash_to_seed(prompt);
    let mut rng = Fastrand::new(seed);
    let bg_hue = rng.f64() * 360.0;
    let style = rng.u32(0..5) as usize;

    let mut img = ImageBuffer::new(width as u32, height as u32);
    match style {
        0 => render_gradient(&mut img, width, height, bg_hue, &mut rng),
        1 => render_geo(&mut img, width, height, prompt, &mut rng, bg_hue),
        2 => render_wave(&mut img, width, height, prompt, &mut rng, bg_hue),
        3 => render_particle(&mut img, width, height, prompt, &mut rng, bg_hue),
        _ => render_mosaic(&mut img, width, height, prompt, &mut rng, bg_hue),
    }

    let dest = PathBuf::from(tmp_dir).join("generated.png");
    img.save(&dest).map_err(|e| format!("编码失败: {}", e))?;
    Ok(dest.to_string_lossy().to_string())
}

/// Applied an artistic filter to an existing image.
pub fn make_edited_image(
    tmp_dir: &str,
    src_path: &str,
    prompt: &str,
    width: i32,
    height: i32,
) -> Result<String, String> {
    let data = std::fs::read(src_path).map_err(|e| format!("读取失败: {}", e))?;
    let src = image::load_from_memory(&data)
        .map_err(|e| format!("解码失败: {}", e))?;

    let (src_w, src_h) = src.dimensions();
    let dst_w = if width > 0 { width as u32 } else { src_w };
    let dst_h = if height > 0 { height as u32 } else { src_h };

    let mut dst = DynamicImage::ImageRgba8(ImageBuffer::new(dst_w, dst_h));

    // Resize by sampling
    let sx = src_w as f64 / dst_w as f64;
    let sy = src_h as f64 / dst_h as f64;
    for y in 0..dst_h {
        for x in 0..dst_w {
            let sx_i = (x as f64 * sx) as u32;
            let sy_i = (y as f64 * sy) as u32;
            let sx_i = sx_i.min(src_w - 1);
            let sy_i = sy_i.min(src_h - 1);
            dst.put_pixel(x, y, src.get_pixel(sx_i, sy_i));
        }
    }

    let seed = hash_to_seed(&format!("{}edit", prompt));
    let mut rng = Fastrand::new(seed);
    match rng.u32(0..4) {
        0 => apply_sepia(&mut dst, dst_w, dst_h),
        1 => apply_invert(&mut dst, dst_w, dst_h),
        2 => apply_blur(&mut dst, dst_w, dst_h, &mut rng),
        _ => apply_posterize(&mut dst, dst_w, dst_h, &mut rng),
    }

    let out = PathBuf::from(tmp_dir).join("edited.png");
    dst.save(&out).map_err(|e| format!("保存失败: {}", e))?;
    Ok(out.to_string_lossy().to_string())
}

/// Composes reference images onto a generated background.
pub fn make_compose_image(
    tmp_dir: &str,
    prompt: &str,
    ref_paths: &[&str],
    width: i32,
    height: i32,
) -> Result<String, String> {
    let width = if width <= 0 { 1024 } else if width > 4096 { 4096 } else { width };
    let height = if height <= 0 { 1024 } else if height > 4096 { 4096 } else { height };

    let seed = hash_to_seed(prompt);
    let mut rng = Fastrand::new(seed);
    let bg_hue = rng.f64() * 360.0;

    let mut img = ImageBuffer::new(width as u32, height as u32);
    render_gradient(&mut img, width, height, bg_hue, &mut rng);

    for p in ref_paths {
        if let Ok(data) = std::fs::read(p) {
            if let Ok(ref_img) = image::load_from_memory(&data) {
                blend_overlay(&mut img, &ref_img, width, height, &mut rng);
            }
        }
    }

    let dest = PathBuf::from(tmp_dir).join("composed.png");
    img.save(&dest).map_err(|e| format!("编码失败: {}", e))?;
    Ok(dest.to_string_lossy().to_string())
}

// ── Rendering primitives ──

fn render_gradient(img: &mut ImageBuffer<Rgba<u8>, Vec<u8>>, w: i32, h: i32, hue: f64, rng: &mut Fastrand) {
    for y in 0..h {
        for x in 0..w {
            let t = y as f64 / h as f64;
            let (r, g, b) = hsl2rgb((hue + t * 120.0) % 360.0, 0.6 + 0.2 * rng.f64(), 0.15 + 0.35 * t);
            img.put_pixel(x as u32, y as u32, Rgba([r, g, b, 255]));
        }
    }
}

fn render_geo(img: &mut ImageBuffer<Rgba<u8>, Vec<u8>>, w: i32, h: i32, prompt: &str, _rng: &mut Fastrand, bg_hue: f64) {
    for y in 0..h {
        for x in 0..w {
            let (r, g, b) = hsl2rgb(bg_hue, 0.5, 0.1);
            img.put_pixel(x as u32, y as u32, Rgba([r, g, b, 255]));
        }
    }
    let seed = hash_to_seed(&format!("{}_g", prompt));
    let mut rng2 = Fastrand::new(seed);
    for _ in 0..(8 + rng2.u32(0..12)) {
        let cx = rng2.f64() * w as f64;
        let cy = rng2.f64() * h as f64;
        let rad = 15.0 + rng2.f64() * 120.0;
        let hue = (bg_hue + rng2.f64() * 25.0) % 360.0;
        let alpha = 0.3 + rng2.f64() * 0.5;
        if rng2.f64() < 0.5 {
            draw_circle(img, w, h, cx, cy, rad, hue, 0.6, 0.4, alpha);
        } else {
            draw_rect(img, w, h, cx, cy, rad, rad * 0.7, hue, 0.6, 0.4, alpha);
        }
    }
}

fn render_wave(img: &mut ImageBuffer<Rgba<u8>, Vec<u8>>, w: i32, h: i32, _prompt: &str, rng: &mut Fastrand, bg_hue: f64) {
    for y in 0..h {
        for x in 0..w {
            let (r, g, b) = hsl2rgb(bg_hue, 0.4, 0.08);
            img.put_pixel(x as u32, y as u32, Rgba([r, g, b, 255]));
        }
    }
    use std::f64::consts::PI;
    for wi in 0..(5 + rng.u32(0..6)) {
        let amp = 8.0 + rng.f64() * 35.0;
        let freq = 0.003 + rng.f64() * 0.02;
        let phase = rng.f64() * 2.0 * PI;
        let spd = rng.f64() * 0.3;
        let hue = (bg_hue + wi as f64 * 35.0) % 360.0;
        for y in 0..h {
            for x in 0..w {
                let wave = (x as f64 * freq + phase + y as f64 * spd).sin() * amp;
                let dist = (y as f64 - h as f64 / 2.0 - wave).abs();
                if dist < 12.0 {
                    let fi = 1.0 - dist / 12.0;
                    let (r, g, b) = hsl2rgb(hue, 0.7, 0.5);
                    blend_pixel_float(img, x, y, r as f64, g as f64, b as f64, fi);
                }
            }
        }
    }
}

fn render_particle(img: &mut ImageBuffer<Rgba<u8>, Vec<u8>>, w: i32, h: i32, _prompt: &str, rng: &mut Fastrand, bg_hue: f64) {
    for y in 0..h {
        for x in 0..w {
            let (r, g, b) = hsl2rgb(bg_hue, 0.3, 0.05);
            img.put_pixel(x as u32, y as u32, Rgba([r, g, b, 255]));
        }
    }
    for _ in 0..(80 + rng.u32(0..150)) {
        let cx = rng.f64() * w as f64;
        let cy = rng.f64() * h as f64;
        let r = 2.0 + rng.f64() * 5.0;
        let hue = (bg_hue + rng.f64() * 100.0) % 360.0;
        let r_i = r as i32;
        for dy in -r_i * 3..=r_i * 3 {
            for dx in -r_i * 3..=r_i * 3 {
                let d = (dx * dx + dy * dy) as f64;
                if d > (r * 3.0).powi(2) {
                    continue;
                }
                let px = cx + dx as f64;
                let py = cy + dy as f64;
                if px < 0.0 || px >= w as f64 || py < 0.0 || py >= h as f64 {
                    continue;
                }
                let fi = (1.0 - d.sqrt() / (r * 3.0)) * (1.0 - d.sqrt() / (r * 3.0));
                let (r2, g2, b2) = hsl2rgb(hue, 0.8, 0.6);
                blend_pixel_float(img, px as i32, py as i32, r2 as f64, g2 as f64, b2 as f64, fi);
            }
        }
    }
}

fn render_mosaic(img: &mut ImageBuffer<Rgba<u8>, Vec<u8>>, w: i32, h: i32, prompt: &str, _rng: &mut Fastrand, bg_hue: f64) {
    let seed = hash_to_seed(&format!("{}_m", prompt));
    let mut rng2 = Fastrand::new(seed);
    let ts = 18 + rng2.u32(0..50) as i32;
    let mut y = 0i32;
    while y < h {
        let mut x = 0i32;
        while x < w {
            let hue = (bg_hue + rng2.f64() * 160.0) % 360.0;
            let sat = 0.3 + rng2.f64() * 0.5;
            let light = 0.2 + rng2.f64() * 0.4;
            let mut dy = 0i32;
            while dy < ts && y + dy < h {
                let mut dx = 0i32;
                while dx < ts && x + dx < w {
                    let (r, g, b) = hsl2rgb(hue, sat, light);
                    img.put_pixel((x + dx) as u32, (y + dy) as u32, Rgba([r, g, b, 255]));
                    dx += 1;
                }
                dy += 1;
            }
            x += ts;
        }
        y += ts;
    }
}

fn draw_circle(
    img: &mut ImageBuffer<Rgba<u8>, Vec<u8>>,
    w: i32,
    h: i32,
    cx: f64,
    cy: f64,
    rad: f64,
    hue: f64,
    sat: f64,
    light: f64,
    alpha: f64,
) {
    let rad_i = rad as i32;
    for dy in -rad_i..=rad_i {
        for dx in -rad_i..=rad_i {
            let d = (dx * dx + dy * dy) as f64;
            if d > rad * rad {
                continue;
            }
            let px = cx + dx as f64;
            let py = cy + dy as f64;
            if px < 0.0 || px >= w as f64 || py < 0.0 || py >= h as f64 {
                continue;
            }
            let fi = (1.0 - d.sqrt() / rad) * alpha;
            let (r, g, b) = hsl2rgb(hue, sat, light);
            blend_pixel_float(img, px as i32, py as i32, r as f64, g as f64, b as f64, fi);
        }
    }
}

fn draw_rect(
    img: &mut ImageBuffer<Rgba<u8>, Vec<u8>>,
    w: i32,
    h: i32,
    cx: f64,
    cy: f64,
    rw: f64,
    rh: f64,
    hue: f64,
    sat: f64,
    light: f64,
    alpha: f64,
) {
    let x0 = (cx - rw) as i32;
    let x1 = (cx + rw) as i32;
    let y0 = (cy - rh) as i32;
    let y1 = (cy + rh) as i32;
    for y in y0..y1 {
        if y < 0 || y >= h { continue; }
        for x in x0..x1 {
            if x < 0 || x >= w { continue; }
            let (r, g, b) = hsl2rgb(hue, sat, light);
            blend_pixel_float(img, x, y, r as f64, g as f64, b as f64, alpha);
        }
    }
}

fn blend_overlay(
    dst: &mut ImageBuffer<Rgba<u8>, Vec<u8>>,
    src: &DynamicImage,
    dw: i32,
    dh: i32,
    _rng: &mut Fastrand,
) {
    let sb = src.dimensions();
    let (sw, sh) = (sb.0 as i32, sb.1 as i32);
    let scale = (dw as f64 / sw as f64).min(dh as f64 / sh as f64);
    let nw = (sw as f64 * scale) as i32;
    let nh = (sh as f64 * scale) as i32;
    let nw = if nw < 1 { 1 } else { nw };
    let nh = if nh < 1 { 1 } else { nh };
    let ox = (dw - nw) / 2;
    let oy = (dh - nh) / 2;
    let _alpha = 0.4f64;

    for y in 0..nh {
        for x in 0..nw {
            let src_x = sb.0 - nw as u32 + x as u32;
            let src_y = sb.1 - nh as u32 + y as u32;
            let sr = src.get_pixel(src_x, src_y);
            let Rgba([sr_r, sr_g, sr_b, sr_a]) = sr;
            let dx = ox + x;
            let dy = oy + y;
            if dx < 0 || dx as u32 >= dw as u32 || dy < 0 || dy as u32 >= dh as u32 {
                continue;
            }
            let dl = dst.get_pixel(dx as u32, dy as u32);
            let Rgba([dr, dg, db, _]) = dl;
            let fact = sr_a as f64 / 255.0 * _alpha;
            let cr = (*dr as f64 * (1.0 - fact) + sr_r as f64 * fact) as u8;
            let cg = (*dg as f64 * (1.0 - fact) + sr_g as f64 * fact) as u8;
            let cb = (*db as f64 * (1.0 - fact) + sr_b as f64 * fact) as u8;
            dst.put_pixel(dx as u32, dy as u32, Rgba([cr, cg, cb, 255]));
        }
    }
}

fn apply_sepia(img: &mut DynamicImage, w: u32, h: u32) {
    for y in 0..h {
        for x in 0..w {
            let Rgba([r, g, b, _]) = img.get_pixel(x, y);
            let fr = r as f64;
            let fg = g as f64;
            let fb = b as f64;
            let tr = fr * 0.393 + fg * 0.769 + fb * 0.189;
            let tg = fr * 0.349 + fg * 0.686 + fb * 0.168;
            let tb = fr * 0.272 + fg * 0.534 + fb * 0.131;
            img.put_pixel(x, y, Rgba([
                clamp(tr * 255.0) as u8,
                clamp(tg * 255.0) as u8,
                clamp(tb * 255.0) as u8,
                255,
            ]));
        }
    }
}

fn apply_invert(img: &mut DynamicImage, w: u32, h: u32) {
    for y in 0..h {
        for x in 0..w {
            let Rgba([r, g, b, a]) = img.get_pixel(x, y);
            img.put_pixel(x, y, Rgba([255 - r, 255 - g, 255 - b, a]));
        }
    }
}

fn apply_blur(img: &mut DynamicImage, w: u32, h: u32, rng: &mut Fastrand) {
    let radius = 1 + rng.u32(0..3) as u32;
    let clone = img.clone();
    for y in radius..h.saturating_sub(radius) {
        for x in radius..w.saturating_sub(radius) {
            let mut tr = 0u32;
            let mut tg = 0u32;
            let mut tb = 0u32;
            let mut ta = 0u32;
            let mut n = 0u32;
            for dy in 0..=radius * 2 {
                for dx in 0..=radius * 2 {
                    let py = y as i32 + dx as i32 - radius as i32;
                    let px = x as i32 + dy as i32 - radius as i32;
                    if py < 0 || px < 0 || py as u32 >= h || px as u32 >= w {
                        continue;
                    }
                    let Rgba([r, g, b, a]) = clone.get_pixel(px as u32, py as u32);
                    tr += r as u32;
                    tg += g as u32;
                    tb += b as u32;
                    ta += a as u32;
                    n += 1;
                }
            }
            img.put_pixel(x, y, Rgba([
                (tr / n) as u8,
                (tg / n) as u8,
                (tb / n) as u8,
                (ta / n) as u8,
            ]));
        }
    }
}

fn apply_posterize(img: &mut DynamicImage, w: u32, h: u32, rng: &mut Fastrand) {
    let levels = 2 + rng.u32(0..6) as u32;
    for y in 0..h {
        for x in 0..w {
            let Rgba([r, g, b, a]) = img.get_pixel(x, y);
            let r = posterize_val(r as f64, levels as f64);
            let g = posterize_val(g as f64, levels as f64);
            let b = posterize_val(b as f64, levels as f64);
            img.put_pixel(x, y, Rgba([r, g, b, a]));
        }
    }
}

fn posterize_val(v: f64, levels: f64) -> u8 {
    (f64::round(v / 255.0 * levels) / levels * 255.0).clamp(0.0, 255.0) as u8
}

fn blend_pixel_float(
    img: &mut ImageBuffer<Rgba<u8>, Vec<u8>>,
    x: i32,
    y: i32,
    r: f64,
    g: f64,
    b: f64,
    factor: f64,
) {
    let Rgba([cr, cg, cb, _]) = img.get_pixel(x as u32, y as u32);
    let fr = *cr as f64;
    let fg = *cg as f64;
    let fb = *cb as f64;
    let pr = clamp(fr * (1.0 - factor) + r * factor);
    let pg = clamp(fg * (1.0 - factor) + g * factor);
    let pb = clamp(fb * (1.0 - factor) + b * factor);
    img.put_pixel(x as u32, y as u32, Rgba([pr as u8, pg as u8, pb as u8, 255]));
}

fn hsl2rgb(h: f64, s: f64, l: f64) -> (u8, u8, u8) {
    let h = (h % 360.0).rem_euclid(360.0) / 360.0;
    let (rr, gg, bb) = if s == 0.0 {
        (l, l, l)
    } else {
        let q = if l < 0.5 { l * (1.0 + s) } else { l + s - l * s };
        let p = 2.0 * l - q;
        (hue2rgb(p, q, h + 1.0 / 3.0), hue2rgb(p, q, h), hue2rgb(p, q, h - 1.0 / 3.0))
    };
    (clamp(rr * 255.0) as u8, clamp(gg * 255.0) as u8, clamp(bb * 255.0) as u8)
}

fn hue2rgb(p: f64, q: f64, t: f64) -> f64 {
    let mut t = t.rem_euclid(1.0);
    if t < 0.0 { t += 1.0; }
    if t > 1.0 { t -= 1.0; }
    if t < 1.0 / 6.0 {
        return p + (q - p) * 6.0 * t;
    }
    if t < 0.5 { return q; }
    if t < 2.0 / 3.0 {
        return p + (q - p) * (2.0 / 3.0 - t) * 6.0;
    }
    p
}

fn clamp(v: f64) -> f64 {
    if v < 0.0 { 0.0 } else if v > 255.0 { 255.0 } else { v }
}

fn hash_to_seed(s: &str) -> u64 {
    use std::collections::hash_map::DefaultHasher;
    use std::hash::{Hash, Hasher};
    let mut h = DefaultHasher::new();
    s.hash(&mut h);
    h.finish()
}

#[allow(dead_code)]
fn size_to_tier(w: i32, h: i32) -> String {
    let max = w.max(h);
    match max {
        m if m <= 1024 => "1K".to_string(),
        m if m <= 2048 => "2K".to_string(),
        m if m <= 3072 => "3K".to_string(),
        _ => "4K".to_string(),
    }
}

#[allow(dead_code)]
fn ratio_from_dims(w: i32, h: i32) -> String {
    let g = gcd(w, h);
    if g == 0 {
        return "1:1".to_string();
    }
    format!("{}:{}", w / g, h / g)
}

/// AI-powered image generation functions
pub async fn make_image_ai(
    client: &AIClient,
    tmp_dir: &str,
    prompt: &str,
    width: i32,
    height: i32,
) -> Result<String, String> {
    let width = if width <= 0 { 1024 } else { width };
    let height = if height <= 0 { 1024 } else { height };
    let dest = PathBuf::from(tmp_dir).join("generated.png");
    let size = size_to_tier(width, height);
    let ratio = ratio_from_dims(width, height);

    // Try Agnes first
    if client.has_agnes() {
        match client.gen_image_agnes(AGNES_IMAGE_MODEL, prompt, &size, &ratio, &[]).await {
            Ok((img_url, b64)) => {
                if client.download_image(&img_url, &b64, dest.to_string_lossy().as_ref()).await.is_ok() {
                    return Ok(dest.to_string_lossy().to_string());
                }
            }
            Err(_) => {}
        }
    }
    // Fallback to SenseNova
    if client.has_sensenova() {
        match client.gen_image_sense_nova("sensenova-u1.5-lite", prompt, &size, &ratio, &[]).await {
            Ok((img_url, b64)) => {
                if client.download_image(&img_url, &b64, dest.to_string_lossy().as_ref()).await.is_ok() {
                    return Ok(dest.to_string_lossy().to_string());
                }
            }
            Err(_) => {}
        }
    }
    Err("AI图片生成不可用".to_string())
}

pub async fn make_edited_image_ai(
    client: &AIClient,
    tmp_dir: &str,
    src_path: &str,
    prompt: &str,
    width: i32,
    height: i32,
) -> Result<String, String> {
    let dest = PathBuf::from(tmp_dir).join("edited.png");
    let size = size_to_tier(width, height);
    let ratio = ratio_from_dims(width, height);
    let data_uri = AIClient::file_to_data_uri(src_path).map_err(|e| format!("读取源图片失败: {}", e))?;

    // Try Agnes first
    if client.has_agnes() {
        match client.gen_image_agnes(AGNES_IMAGE_MODEL, prompt, &size, &ratio, &[data_uri.clone()]).await {
            Ok((img_url, b64)) => {
                if client.download_image(&img_url, &b64, dest.to_string_lossy().as_ref()).await.is_ok() {
                    return Ok(dest.to_string_lossy().to_string());
                }
            }
            Err(_) => {}
        }
    }
    // Fallback to SenseNova
    if client.has_sensenova() {
        match client.gen_image_sense_nova("sensenova-u1.5-lite", prompt, &size, &ratio, &[data_uri]).await {
            Ok((img_url, b64)) => {
                if client.download_image(&img_url, &b64, dest.to_string_lossy().as_ref()).await.is_ok() {
                    return Ok(dest.to_string_lossy().to_string());
                }
            }
            Err(_) => {}
        }
    }
    Err("AI图片编辑不可用".to_string())
}

pub async fn make_compose_image_ai(
    client: &AIClient,
    tmp_dir: &str,
    prompt: &str,
    ref_paths: &[String],
    width: i32,
    height: i32,
) -> Result<String, String> {
    let dest = PathBuf::from(tmp_dir).join("composed.png");
    let size = size_to_tier(width, height);
    let ratio = ratio_from_dims(width, height);

    let mut data_uris: Vec<String> = Vec::new();
    for p in ref_paths {
        if let Ok(uri) = AIClient::file_to_data_uri(p) {
            data_uris.push(uri);
        }
    }

    // Try Agnes first
    if client.has_agnes() {
        match client.gen_image_agnes(AGNES_IMAGE_MODEL, prompt, &size, &ratio, &data_uris).await {
            Ok((img_url, b64)) => {
                if client.download_image(&img_url, &b64, dest.to_string_lossy().as_ref()).await.is_ok() {
                    return Ok(dest.to_string_lossy().to_string());
                }
            }
            Err(_) => {}
        }
    }
    // Fallback to SenseNova
    if client.has_sensenova() {
        match client.gen_image_sense_nova("sensenova-u1.5-lite", prompt, &size, &ratio, &data_uris).await {
            Ok((img_url, b64)) => {
                if client.download_image(&img_url, &b64, dest.to_string_lossy().as_ref()).await.is_ok() {
                    return Ok(dest.to_string_lossy().to_string());
                }
            }
            Err(_) => {}
        }
    }
    Err("AI图片合成不可用".to_string())
}

const AGNES_RATIO_TIERS: &[(&str, f64)] = &[
    ("1:1", 1.0),
    ("16:9", 16.0 / 9.0),
    ("9:16", 9.0 / 16.0),
    ("4:3", 4.0 / 3.0),
    ("3:4", 3.0 / 4.0),
    ("2:3", 2.0 / 3.0),
    ("3:2", 3.0 / 2.0),
];

fn nearest_agnes_ratio(w: i32, h: i32) -> String {
    if w <= 0 || h <= 0 {
        return "1:1".to_string();
    }
    let target = w as f64 / h as f64;
    let mut best = "1:1";
    let mut best_dist = f64::MAX;
    for (ratio, val) in AGNES_RATIO_TIERS {
        let d = (target.ln() - val.ln()).abs();
        if d < best_dist {
            best_dist = d;
            best = ratio;
        }
    }
    best.to_string()
}

fn agnes_short_side(tier: &str) -> i32 {
    match tier {
        "2K" => 1536,
        "3K" => 2048,
        "4K" => 2560,
        _ => 1024,
    }
}

fn ratio_dims(ratio: &str) -> (i32, i32) {
    let mut a = 1;
    let mut b = 1;
    if let Some((l, r)) = ratio.split_once(':') {
        if let (Ok(x), Ok(y)) = (l.parse::<i32>(), r.parse::<i32>()) {
            if x > 0 && y > 0 {
                a = x;
                b = y;
            }
        }
    }
    (a, b)
}

pub fn resolve_edit_dims(size_key: &str, orig_w: i32, orig_h: i32) -> (String, String, i32, i32) {
    let ratio = nearest_agnes_ratio(orig_w, orig_h);
    let tier = match size_key.to_ascii_lowercase().as_str() {
        "1k" => "1K".to_string(),
        "2k" => "2K".to_string(),
        "4k" => "4K".to_string(),
        _ => size_to_tier(orig_w, orig_h),
    };
    let (aw, ah) = ratio_dims(&ratio);
    let short = agnes_short_side(&tier);
    let (mut canvas_w, mut canvas_h) = if aw >= ah {
        (short * aw / ah, short)
    } else {
        (short, short * ah / aw)
    };
    if canvas_w < 2 {
        canvas_w = 2;
    }
    if canvas_h < 2 {
        canvas_h = 2;
    }
    (tier, ratio, canvas_w, canvas_h)
}

fn crop_to_ratio(src: &DynamicImage, tw: i32, th: i32) -> DynamicImage {
    let (sw, sh) = src.dimensions();
    if tw <= 0 || th <= 0 || sw == 0 || sh == 0 {
        return src.clone();
    }
    let tr = tw as f64 / th as f64;
    let sr = sw as f64 / sh as f64;
    let (cw, ch, x0, y0) = if sr > tr {
        let ch = sh;
        let cw = ((sh as f64) * tr + 0.5) as u32;
        let x0 = (sw.saturating_sub(cw)) / 2;
        (cw.max(1), ch, x0, 0u32)
    } else {
        let cw = sw;
        let ch = ((sw as f64) / tr + 0.5) as u32;
        let y0 = (sh.saturating_sub(ch)) / 2;
        (cw, ch.max(1), 0u32, y0)
    };
    let cropped = src.crop_imm(x0, y0, cw, ch);
    cropped.resize_exact(tw as u32, th as u32, image::imageops::FilterType::Triangle)
}

fn background_prompt(user_prompt: &str) -> String {
    format!(
        "{}, empty scene background, no people, no human, no person, wide angle, photorealistic, high detail",
        user_prompt.trim()
    )
}

fn save_cropped_png(src_path: &str, dest: &str, orig_w: i32, orig_h: i32) -> Result<(), String> {
    let data = std::fs::read(src_path).map_err(|e| e.to_string())?;
    let img = image::load_from_memory(&data).map_err(|e| e.to_string())?;
    let cropped = crop_to_ratio(&img, orig_w, orig_h);
    cropped.save(dest).map_err(|e| e.to_string())
}

/// 换背景：AI 空场景 → AI 合成（发丝）→ 本地 rembg → 图生图。
pub async fn make_edited_image_composed(
    client: &AIClient,
    tmp_dir: &str,
    src_path: &str,
    prompt: &str,
    size_key: &str,
    orig_w: i32,
    orig_h: i32,
) -> Result<String, String> {
    if orig_w <= 0 || orig_h <= 0 || (!client.has_agnes() && !client.has_sensenova()) {
        return make_edited_image_ai(client, tmp_dir, src_path, prompt, orig_w, orig_h).await;
    }

    let (tier, ratio, _, _) = resolve_edit_dims(size_key, orig_w, orig_h);
    let bg_prompt = background_prompt(prompt);
    let bg_raw = PathBuf::from(tmp_dir).join("bg_raw.png");
    let bg_raw_s = bg_raw.to_string_lossy().to_string();
    let mut got = false;
    if client.has_agnes() {
        if let Ok((u, b)) = client.gen_image_agnes(AGNES_IMAGE_MODEL, &bg_prompt, &tier, &ratio, &[]).await {
            if client.download_image(&u, &b, &bg_raw_s).await.is_ok() {
                got = true;
            }
        }
    }
    if !got && client.has_sensenova() {
        if let Ok((u, b)) = client
            .gen_image_sense_nova("sensenova-u1.5-lite", &bg_prompt, &tier, &ratio, &[])
            .await
        {
            if client.download_image(&u, &b, &bg_raw_s).await.is_ok() {
                got = true;
            }
        }
    }
    if !got {
        return make_edited_image_ai(client, tmp_dir, src_path, prompt, orig_w, orig_h).await;
    }

    let bg_out = PathBuf::from(tmp_dir).join("bg.png");
    let bg_out_s = bg_out.to_string_lossy().to_string();
    if save_cropped_png(&bg_raw_s, &bg_out_s, orig_w, orig_h).is_err() {
        return make_edited_image_ai(client, tmp_dir, src_path, prompt, orig_w, orig_h).await;
    }

    let dest = PathBuf::from(tmp_dir).join("edited.png");
    let dest_s = dest.to_string_lossy().to_string();
    if client.has_agnes() {
        if let (Ok(src_uri), Ok(bg_uri)) = (AIClient::file_to_data_uri(src_path), AIClient::file_to_data_uri(&bg_out_s)) {
            let composite_prompt = format!(
                "{}, place the person from the first reference image onto the background of the second reference image, keep the person identical, only change the background",
                prompt
            );
            if let Ok((img_url, b64)) = client
                .gen_image_agnes(AGNES_IMAGE_MODEL, &composite_prompt, &tier, &ratio, &[src_uri, bg_uri])
                .await
            {
                if client.download_image(&img_url, &b64, &dest_s).await.is_ok() {
                    let _ = save_cropped_png(&dest_s, &dest_s, orig_w, orig_h);
                    return Ok(dest_s);
                }
            }
        }
    }
    tracing::warn!("[compose-edit] AI合成失败，退回本地 rembg 合成");

    let script = script_path("compose_bg.py");
    let result = run_cmd_timeout(
        Duration::from_secs(120),
        python_path(),
        &[&script, src_path, &bg_out_s, &dest_s],
    );
    if result.error.is_none() {
        if let Ok(v) = serde_json::from_str::<serde_json::Value>(result.stdout.trim()) {
            if v.get("ok").and_then(|x| x.as_bool()) == Some(true) {
                return Ok(dest_s);
            }
        }
    }
    tracing::warn!(
        "[compose-edit] 本地合成失败(err={:?} out={})，退回图生图",
        result.error,
        result.stdout.trim()
    );
    make_edited_image_ai(client, tmp_dir, src_path, prompt, orig_w, orig_h).await
}

#[allow(dead_code)]
fn gcd(a: i32, b: i32) -> i32 {
    let (mut a, mut b) = (a.abs(), b.abs());
    while b != 0 {
        let t = b;
        b = a % b;
        a = t;
    }
    a
}

/// Minimal fast random number generator for procedural art.
#[derive(Clone)]
pub struct Fastrand {
    state: u64,
}

impl Fastrand {
    fn new(seed: u64) -> Self {
        Self { state: seed }
    }

    fn f64(&mut self) -> f64 {
        self.state = self.state.wrapping_mul(6364136223846793005).wrapping_add(1);
        (self.state >> 11) as f64 / (1u64 << 53) as f64
    }

    fn u32(&mut self, range: core::ops::Range<u32>) -> u32 {
        let r = range.end - range.start;
        if r == 0 { return range.start; }
        let v = self.f64() * r as f64;
        (range.start + v as u32).min(range.end - 1)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::util::new_id;

    #[test]
    fn test_make_image() {
        let tmp = std::env::temp_dir().join(format!("flowconvert_{}", new_id(8)));
        std::fs::create_dir_all(&tmp).unwrap();
        let result = make_image(tmp.to_str().unwrap(), "test prompt", 100, 100);
        assert!(result.is_ok());
        let path = result.unwrap();
        assert!(std::path::Path::new(&path).exists());
        let _ = std::fs::remove_dir_all(tmp);
    }

    #[test]
    fn test_size_clamp() {
        let tmp = std::env::temp_dir().join(format!("flowconvert_{}", new_id(8)));
        std::fs::create_dir_all(&tmp).unwrap();
        // Width 0 -> 1024, height 0 -> 1024
        let result = make_image(tmp.to_str().unwrap(), "clamp test", 0, 0);
        assert!(result.is_ok());
        let _ = std::fs::remove_dir_all(tmp);

        let tmp = std::env::temp_dir().join(format!("flowconvert_{}", new_id(8)));
        std::fs::create_dir_all(&tmp).unwrap();
        // Width > 4096 -> 4096, height > 4096 -> 4096
        let result = make_image(tmp.to_str().unwrap(), "clamp test", 5000, 6000);
        assert!(result.is_ok());
        let _ = std::fs::remove_dir_all(tmp);
    }

    #[test]
    fn test_size_to_tier() {
        assert_eq!(size_to_tier(500, 500), "1K");
        assert_eq!(size_to_tier(1024, 1024), "1K");
        assert_eq!(size_to_tier(1500, 1000), "2K");
        assert_eq!(size_to_tier(2048, 2048), "2K");
        assert_eq!(size_to_tier(2500, 2000), "3K");
        assert_eq!(size_to_tier(3072, 3072), "3K");
        assert_eq!(size_to_tier(4000, 3000), "4K");
        assert_eq!(size_to_tier(4097, 4097), "4K");
    }

    #[test]
    fn test_ratio_from_dims() {
        assert_eq!(ratio_from_dims(0, 0), "1:1");
        assert_eq!(ratio_from_dims(1920, 1080), "16:9");
        assert_eq!(ratio_from_dims(1080, 1920), "9:16");
        assert_eq!(ratio_from_dims(100, 100), "1:1");
        assert_eq!(ratio_from_dims(4, 3), "4:3");
        assert_eq!(ratio_from_dims(3, 4), "3:4");
    }

    #[test]
    fn test_nearest_agnes_ratio() {
        assert_eq!(nearest_agnes_ratio(1080, 1920), "9:16");
        assert_eq!(nearest_agnes_ratio(1920, 1080), "16:9");
        assert_eq!(nearest_agnes_ratio(1024, 1024), "1:1");
        assert_eq!(nearest_agnes_ratio(0, 0), "1:1");
        assert_eq!(nearest_agnes_ratio(1792, 1024), "16:9");
        assert_eq!(nearest_agnes_ratio(768, 1024), "3:4");
    }

    #[test]
    fn test_resolve_edit_dims() {
        let (tier, ratio, cw, ch) = resolve_edit_dims("original", 1080, 1920);
        assert_eq!(ratio, "9:16");
        assert!(!tier.is_empty());
        assert!(cw <= ch);
        let (_, r, cw2, ch2) = resolve_edit_dims("4k", 1080, 1920);
        assert_eq!(r, "9:16");
        assert!(cw2 < ch2);
        let (tier, ratio, cw, ch) = resolve_edit_dims("", 0, 0);
        assert_eq!(ratio, "1:1");
        assert!(!tier.is_empty());
        assert_eq!(cw, ch);
    }

    #[test]
    fn test_crop_to_ratio() {
        let tier_img = DynamicImage::ImageRgba8(ImageBuffer::from_pixel(1312, 736, Rgba([30, 80, 200, 255])));
        let got = crop_to_ratio(&tier_img, 700, 400);
        assert_eq!(got.dimensions(), (700, 400));
        let c = got.get_pixel(350, 200);
        assert!(c[0] >= 20 && c[1] >= 60 && c[2] >= 160, "中心像素被破坏: {:?}", c);

        let portrait = DynamicImage::ImageRgba8(ImageBuffer::from_pixel(736, 1312, Rgba([10, 10, 10, 255])));
        let got2 = crop_to_ratio(&portrait, 900, 1600);
        assert_eq!(got2.dimensions(), (900, 1600));

        let same = DynamicImage::ImageRgba8(ImageBuffer::from_pixel(800, 600, Rgba([1, 2, 3, 255])));
        let got3 = crop_to_ratio(&same, 800, 600);
        assert_eq!(got3.dimensions(), (800, 600));
    }
}
