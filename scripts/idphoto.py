#!/usr/bin/env python3
"""证件照背景替换 - 基于 rembg (U2Net)

输出 PNG/JPG，写入 DPI 元数据，确保打印物理尺寸正确。
"""
import sys
import json
import os
import io

import numpy as np
from PIL import Image, ImageOps, ImageEnhance

DPI = 300
PX_PER_MM = DPI / 25.4

# 尺寸表: 名称 -> (宽mm, 高mm)
SIZES = {
    "一寸": (25, 35),
    "小一寸": (22, 32),
    "大一寸": (33, 48),
    "小二寸": (35, 45),
    "二寸": (35, 49),
    "大二寸": (40, 50),
    "美签": (51, 51),
    "小三寸": (35, 45),
    "三寸": (55, 84),
    "社保": (32, 36),
    "教资": (25, 35),
    "公务员": (25, 35),
    "四六级": (57, 76),
    "日语": (30, 42),
    "五寸": (89, 127),
    "六寸": (102, 152),
}

BACKGROUNDS = {
    "白色": (255, 255, 255),
    "蓝色": (0, 114, 227),
    "深蓝": (0, 81, 174),
    "红色": (237, 28, 36),
    "粉色": (255, 182, 193),
    "灰色": (191, 191, 191),
    "浅蓝": (173, 216, 230),
    "青色": (0, 191, 255),
}


def mm_to_px(mm):
    return max(1, int(round(mm * PX_PER_MM)))


def remove_background(pil_img):
    """使用 rembg (u2netp) 去除背景，返回 RGBA 图像"""
    from rembg import remove, new_session
    session = new_session(model_name="u2netp")
    result = remove(pil_img, session=session)
    if isinstance(result, (bytes, bytearray)):
        return Image.open(io.BytesIO(result)).convert("RGBA")
    return result.convert("RGBA")


def detect_face(pil_img):
    """人脸检测 - 使用 rembg alpha 通道估算"""
    try:
        from rembg import remove, new_session
        session = new_session(model_name="u2netp")
        rgba = remove(pil_img, session=session).convert("RGBA")
        alpha = np.array(rgba.split()[-1])
        h, w = alpha.shape

        # 前景行
        row_sum = alpha.sum(axis=1)
        fg_rows = np.where(row_sum > w * 0.08)[0]
        if len(fg_rows) < 20:
            return None

        top = fg_rows[0]
        bottom = fg_rows[-1]
        person_h = bottom - top

        # 水平中心
        col_sum = alpha.sum(axis=0)
        fg_cols = np.where(col_sum > h * 0.08)[0]
        center_x = (fg_cols[0] + fg_cols[-1]) // 2 if len(fg_cols) > 0 else w // 2

        # 人脸位置估算（标准证件照比例）
        # 头顶在人物顶部略上方，脸中心约在人物上部 35% 处
        face_y = int(top + person_h * 0.35)
        face_h = int(person_h * 0.30)
        face_w = int(face_h * 0.72)

        return (center_x, face_y, face_w, face_h)
    except Exception:
        pass
    return None


def locate_head_from_alpha(rgba):
    """从 rembg 的 alpha 通道推断头部位置（不需要额外模型）
    
    原理：人物 alpha 通道中，
    - 头部区域：前景宽度较窄（额头到下巴）
    - 颈部：前景宽度最小
    - 肩部：前景宽度突然变大
    
    返回 (center_x, face_center_y, face_h, head_top, shoulder_y) 或 None
    """
    alpha = np.array(rgba.split()[-1])
    h, w = alpha.shape

    # 每行的前景像素数
    row_fg = np.array([(alpha[y] > 30).sum() for y in range(h)], dtype=float)

    # 找到前景的上下边界
    fg_rows = np.where(row_fg > w * 0.05)[0]
    if len(fg_rows) < 20:
        return None

    top = fg_rows[0]
    bottom = fg_rows[-1]
    total_h = bottom - top

    # 策略：找头部区域（顶部附近的第一个宽度峰值），
    # 然后肩部 = 头部峰值之后宽度再次显著增大的位置
    window = max(5, total_h // 30)

    # 从顶部开始扫描，找头部宽度峰值
    # 头部区域：宽度快速增大到峰值后趋于平稳（到达下巴/脖子）
    # 用宽度变化率检测：找宽度增长率骤降的位置
    head_peak_y = top
    head_peak_w = row_fg[top]
    
    # 先平滑宽度
    smooth = np.convolve(row_fg, np.ones(window)/window, mode='same')
    
    # 扫描前 50% 高度，找第一个"增长停滞"点
    # 头部底部：连续多行宽度不再显著增长
    search_end = min(bottom, top + int(total_h * 0.5))
    stall_count = 0
    stall_y = None
    for y in range(top + window, search_end - window):
        prev_w = smooth[y - window]
        cur_w = smooth[y]
        growth = cur_w - prev_w
        if growth < smooth[y] * 0.02:
            stall_count += 1
            if stall_y is None:
                stall_y = y
            if stall_count >= window * 2:
                head_peak_y = stall_y
                head_peak_w = smooth[stall_y]
                break
        else:
            stall_count = 0
            stall_y = None
            if cur_w > head_peak_w:
                head_peak_w = cur_w
                head_peak_y = y

    # 肩部检测策略：
    # 1. 先在 head_peak_y 附近找宽度峰值
    # 2. 从峰值下方扫描，找宽度首次超过 head_peak_w * 1.15 的位置
    # 3. 如果没找到（紧凑半身照/全身照），用下巴位置反推
    shoulder_y = None
    search_limit = min(bottom, head_peak_y + int(total_h * 0.5))
    for y in range(head_peak_y, search_limit):
        local_w = smooth[y]
        if local_w > head_peak_w * 1.15 and local_w > w * 0.10:
            shoulder_y = y
            break

    # 如果没找到（紧凑半身照，肩部宽度与头部接近），用下巴位置反推
    if shoulder_y is None:
        # 下巴位置 ≈ 头部峰值下方约 0.55 倍头部高度（人脸中心到下巴的距离）
        # 脖子高 ≈ 脸高的 0.2-0.25，肩部 ≈ 下巴 + 脖子
        chin_y = head_peak_y + int(head_peak_w / w * total_h * 0.55)
        # 更可靠的方式：从 face_info 获取下巴位置
        # 或者用：头部峰值宽度对应的位置往下，宽度开始稳定下降后的位置
        # 找颈部最窄点（宽度开始回升前的最低点）
        neck_y = head_peak_y
        neck_min_w = float('inf')
        for y in range(head_peak_y, min(bottom, head_peak_y + int(total_h * 0.3))):
            if smooth[y] < neck_min_w:
                neck_min_w = smooth[y]
                neck_y = y
        # 从颈部往下，找到宽度开始显著增大的位置
        for y in range(neck_y, min(bottom, neck_y + int(total_h * 0.25))):
            if smooth[y] > neck_min_w * 1.10:
                shoulder_y = y
                break
        if shoulder_y is None:
            # 最终 fallback：基于人脸检测或固定比例
            shoulder_y = head_peak_y + int(head_peak_w / w * total_h * 0.8)

    # 确保肩部在合理范围内（不超过底部，不低于头部峰值下方 20%）
    shoulder_y = max(head_peak_y + int(total_h * 0.2), min(shoulder_y, bottom))

    # 头部高度 = 顶部到肩部
    head_h = shoulder_y - top
    if head_h < 30:
        # 极端情况：用面部中心反推
        if face_info is not None:
            fx, fy, fw, fh = face_info
            head_h = int(fh * 1.5)
            shoulder_y = top + head_h
        else:
            head_h = total_h * 0.35
            shoulder_y = top + int(head_h)

    # 人脸中心 ≈ 头部中下部（眉线到下巴的中间）
    face_center_y = top + int(head_h * 0.55)
    face_h = int(head_h * 0.65)

    # 水平中心：取头部区域的前景重心
    head_region = alpha[top:shoulder_y]
    cols = np.where(head_region.sum(axis=0) > head_region.shape[0] * 0.1)[0]
    center_x = int(cols.mean()) if len(cols) > 0 else w // 2

    return (center_x, face_center_y, face_h, top, shoulder_y)


def smart_crop(rgba, face_info, target_ratio):
    """按证件照标准裁剪（肩膀以上为主）:

    证件照规范：
    - 头部（含头发）占画面高度的 ~70%
    - 头顶距画面顶部 ~5%
    - 下巴到画面底部 ~20-25%（只露肩膀领口，不露太多身体）
    - 整体裁剪高度不超过原图高度的 50%

    优先使用 face_info 定位人脸，fallback 用 alpha 通道
    返回 (crop_image, method_name)
    """
    src_w, src_h = rgba.size

    # 方案1：优先使用 face_info
    if face_info is not None:
        fx, fy, fw, fh = face_info
        # 下巴位置
        chin_y = int(fy + fh * 0.35)
        # 头顶位置（含头发）
        head_top = int(fy - fh * 0.55)
        # 肩部位置（下巴下方 8-12% 脸高，标准证件照只露少量肩膀）
        shoulder_y = int(chin_y + fh * 0.10)

        head_total = shoulder_y - head_top

        # 裁剪高度 = 头肩高 / 0.75 (让头肩占 75%)
        crop_h = int(head_total / 0.75)

        # 限制：底部不超过肩部下方 15px（证件照只露少量肩膀）
        max_bottom = shoulder_y + 15
        allowed_h = max_bottom - head_top
        if crop_h > allowed_h:
            crop_h = allowed_h

        # 严格限制：不超过原图高度的 45%
        max_crop_h = int(src_h * 0.45)
        crop_h = min(crop_h, max_crop_h)

        crop_w = int(crop_h * target_ratio)
        crop_h = min(crop_h, src_h)
        crop_w = min(crop_w, src_w)

        crop_top = int(head_top - 0.03 * crop_h)
        crop_left = int(fx - crop_w / 2)

        crop_top = max(0, min(crop_top, src_h - crop_h))
        crop_left = max(0, min(crop_left, src_w - crop_w))
        if crop_top + crop_h > src_h:
            crop_top = src_h - crop_h

        # 裁剪基础区域
        base_crop = rgba.crop((crop_left, crop_top, crop_left + crop_w, crop_top + crop_h))

        # 添加透明底部填充（约 15% 的高度，确保背景色可见）
        padding_h = int(crop_h * 0.15)
        new_h = crop_h + padding_h
        new_canvas = Image.new("RGBA", (crop_w, new_h), (0, 0, 0, 0))
        new_canvas.paste(base_crop, (0, 0))

        return new_canvas, "face_padded"

    # 方案2：从 alpha 通道定位头部
    alpha_info = locate_head_from_alpha(rgba)

    if alpha_info is not None:
        cx, face_y, face_h, head_top, shoulder_y = alpha_info

        head_total = shoulder_y - head_top
        crop_h = int(head_total / 0.75)

        # 限制：底部不超过肩部下方 15px
        max_bottom = shoulder_y + 15
        allowed_h = max_bottom - head_top
        if crop_h > allowed_h:
            crop_h = allowed_h

        # 严格限制：不超过原图高度的 45%
        max_crop_h = int(src_h * 0.45)
        crop_h = min(crop_h, max_crop_h)

        crop_w = int(crop_h * target_ratio)
        crop_h = min(crop_h, src_h)
        crop_w = min(crop_w, src_w)

        # 宽度约束
        _alpha = np.array(rgba.split()[-1])
        head_region = _alpha[head_top: head_top + int(head_total * 0.5)]
        head_cols = np.where(head_region.sum(axis=0) > head_region.shape[0] * 0.1)[0]
        head_width = head_cols[-1] - head_cols[0] + 1 if len(head_cols) > 0 else 0
        if head_width < 30:
            head_width = int(face_h * 0.75)
        if crop_w > 0 and head_width / crop_w > 0.70:
            crop_w_new = min(int(head_width / 0.60), src_w)
            crop_h_new = int(crop_w_new / target_ratio)
            crop_h_new = min(crop_h_new, src_h)
            if crop_h_new > crop_h:
                crop_w, crop_h = crop_w_new, crop_h_new

        crop_top = int(head_top - 0.05 * crop_h)
        crop_left = int(cx - crop_w / 2)

        crop_top = max(0, min(crop_top, src_h - crop_h))
        crop_left = max(0, min(crop_left, src_w - crop_w))
        if crop_top + crop_h > src_h:
            crop_top = src_h - crop_h

        base_crop = rgba.crop((crop_left, crop_top, crop_left + crop_w, crop_top + crop_h))

        # 添加透明底部填充（约 15% 的高度，确保背景色可见）
        padding_h = int(crop_h * 0.15)
        new_h = crop_h + padding_h
        new_canvas = Image.new("RGBA", (crop_w, new_h), (0, 0, 0, 0))
        new_canvas.paste(base_crop, (0, 0))

        return new_canvas, "alpha_padded"

    # 方案2：用 face_info (Haar Cascade)
    if face_info is not None:
        fx, fy, fw, fh = face_info
        head_h = fh * 1.5  # 估算含头发的头部高度
        crop_h = int(head_h / 0.70)
        crop_w = int(crop_h * target_ratio)
        crop_h = min(crop_h, src_h)
        crop_w = min(crop_w, src_w)

        head_top = fy - fh * 0.5
        crop_top = int(head_top - 0.05 * crop_h)
        crop_left = int(fx - crop_w / 2)

        crop_top = max(0, min(crop_top, src_h - crop_h))
        crop_left = max(0, min(crop_left, src_w - crop_w))

        return rgba.crop((crop_left, crop_top, crop_left + crop_w, crop_top + crop_h)), "haar"

    # 方案3：从 alpha 前景范围 fallback 裁剪
    alpha = np.array(rgba.split()[-1])
    row_fg = np.array([(alpha[y] > 30).sum() for y in range(alpha.shape[0])], dtype=float)
    fg_rows = np.where(row_fg > alpha.shape[1] * 0.05)[0]

    if len(fg_rows) > 20:
        top = fg_rows[0]
        bottom = fg_rows[-1]
        person_h = bottom - top
        # 只取上 70%（肩膀以上）
        crop_h = int(person_h * 0.70)
        crop_w = int(crop_h * target_ratio)
        crop_top = max(0, top - int(0.05 * crop_h))
        crop_left = max(0, (src_w - crop_w) // 2)
        if crop_top + crop_h > src_h:
            crop_top = src_h - crop_h
        return rgba.crop((crop_left, crop_top, crop_left + crop_w, crop_top + crop_h)), "alpha_fallback"

    # 方案4：纯中心裁剪上半部分
    if src_w / src_h > target_ratio:
        crop_h = int(src_h * 0.75)
        crop_w = int(crop_h * target_ratio)
    else:
        crop_w = src_w
        crop_h = int(crop_w / target_ratio)
    crop_top = max(0, (src_h - crop_h) // 4)
    crop_left = max(0, (src_w - crop_w) // 2)
    return rgba.crop((crop_left, crop_top, crop_left + crop_w, crop_top + crop_h)), "center"


def resize_and_composite(crop_rgba, target_w, target_h, bg_rgb):
    """缩放并合成到纯色背景，边缘使用硬阈值+羽化处理"""
    resized = crop_rgba.resize((target_w, target_h), Image.LANCZOS)
    alpha_raw = np.array(resized.split()[-1]).astype(np.float32)

    # 硬阈值：alpha < 0.15 设为 0（纯透明），alpha > 0.85 设为 255（纯不透明）
    # 中间区域保留 0.15-0.85 的渐变，形成 2px 左右的羽化边缘
    alpha = alpha_raw.copy()
    alpha[alpha < 38] = 0       # 0.15 * 255
    alpha[alpha > 217] = 255    # 0.85 * 255

    fg = np.array(resized.convert("RGB")).astype(np.float32)
    alpha = alpha / 255.0
    alpha_a = alpha[:, :, np.newaxis]

    bg = np.full((target_h, target_w, 3), bg_rgb, dtype=np.float32)
    result = (fg * alpha_a + bg * (1.0 - alpha_a)).astype(np.uint8)

    # 确保四角是纯背景色
    corner = 4
    result[:corner, :] = bg_rgb
    result[-corner:, :] = bg_rgb
    result[:, :corner] = bg_rgb
    result[:, -corner:] = bg_rgb

    return Image.fromarray(result, "RGB")


def make_idphoto(image_path, out_path, size_key, bg_key):
    """生成证件照主函数"""
    if size_key not in SIZES:
        size_key = "一寸"
    if bg_key not in BACKGROUNDS:
        bg_key = "白色"

    mm_w, mm_h = SIZES[size_key]
    tw = mm_to_px(mm_w)
    th = mm_to_px(mm_h)
    bg_rgb = BACKGROUNDS[bg_key]
    target_ratio = tw / th  # 目标宽高比

    with Image.open(image_path) as pil:
        pil = ImageOps.exif_transpose(pil)
        pil = pil.convert("RGB")
        pil = ImageEnhance.Contrast(pil).enhance(1.05)
        pil = ImageEnhance.Brightness(pil).enhance(1.02)

        # 工作尺寸：限制最大边 1200px，保持比例
        work_w = min(1200, pil.width)
        work_h = int(pil.height * work_w / pil.width)
        pil_work = pil.resize((work_w, work_h), Image.LANCZOS)

    # 使用 rembg 去除背景
    rgba = remove_background(pil_work)

    # 人脸检测
    face_info = detect_face(pil_work)

    # 智能裁剪到目标比例
    crop, _ = smart_crop(rgba, face_info, target_ratio)

    # 缩放并合成到纯色背景
    result = resize_and_composite(crop, tw, th, bg_rgb)

    # 保存，写入 DPI 元数据
    ext = os.path.splitext(out_path)[1].lower()
    if ext in (".jpg", ".jpeg"):
        result.save(out_path, "JPEG", quality=95, dpi=(DPI, DPI))
    else:
        # PNG 默认
        result.save(out_path, "PNG", dpi=(DPI, DPI))

    return {
        "size": size_key,
        "bg": bg_key,
        "w": tw,
        "h": th,
        "mm_w": mm_w,
        "mm_h": mm_h,
        "dpi": DPI,
    }


if __name__ == "__main__":
    image_path = sys.argv[1]
    out_path = sys.argv[2]
    size_key = sys.argv[3] if len(sys.argv) > 3 else "一寸"
    bg_key = sys.argv[4] if len(sys.argv) > 4 else "白色"
    info = make_idphoto(image_path, out_path, size_key, bg_key)
    print(json.dumps(info, ensure_ascii=False))
