#!/usr/bin/env python3
"""换背景合成：保留原图人物像素不变，背景用外部生成的空场景图叠加。

人物掩膜优先级：
1) rembg (u2net_human_seg) 人体分割 —— 干净抠出人物（含发丝/边缘），像素级保留；
2) 失败则回退"中心偏置软掩膜"（几何兜底）。

用法: compose_bg.py <orig.png> <bg.png> <out.png>
输出 JSON: {"ok": true, "method": "rembg", "kept": 0.65} 或 {"ok": false, "error": "..."}
"""
import json
import sys


def emit(obj):
    sys.stdout.write(json.dumps(obj) + "\n")


def fallback_mask(H, W):
    """几何中心偏置软掩膜：主体区域 alpha=1，向外渐隐到 0。"""
    import numpy as np

    mn = min(W, H) / 2.0
    Y, X = np.ogrid[:H, :W]
    dx = (X - W / 2.0) / mn
    dy = (Y - H / 2.0) / mn
    d = np.sqrt(dx * dx + dy * dy)
    alpha = np.clip((1.0 - d) / (1.0 - 0.72), 0.0, 1.0)
    return alpha.astype(np.float32)


def rembg_alpha(orig_bgr):
    """用 rembg 人体分割返回 alpha(0-255 uint8)。失败抛异常。
    开启 alpha matting 精修半透明发丝 + decontaminate 去除发丝边缘的背景色渗染，
    使头发边缘能随新背景换色；matting 失败则退回普通后处理。"""
    import cv2
    import numpy as np

    from rembg import new_session, remove

    rgb = cv2.cvtColor(orig_bgr, cv2.COLOR_BGR2RGB)
    sess = new_session("u2net_human_seg")
    try:
        out = remove(
            rgb,
            session=sess,
            alpha_matting=True,
            alpha_matting_foreground_threshold=240,
            alpha_matting_background_threshold=10,
            alpha_matting_erode_size=8,
            decontaminate=True,
            post_process_mask=True,
        )
    except Exception:  # noqa: BLE001  matting 不可用（缺 pymatting/模型异常）则退回
        out = remove(rgb, session=sess, post_process_mask=True)
    if hasattr(out, "convert"):
        out = out.convert("RGBA")
    arr = np.array(out)
    return arr[..., 3]


def main():
    if len(sys.argv) != 4:
        emit({"ok": False, "error": "usage: compose_bg.py orig bg out"})
        return
    orig_p, bg_p, out_p = sys.argv[1], sys.argv[2], sys.argv[3]

    try:
        import cv2
        import numpy as np
    except Exception as e:  # noqa: BLE001
        emit({"ok": False, "error": "cv2 import failed: %s" % e})
        return

    orig = cv2.imread(orig_p, cv2.IMREAD_COLOR)
    if orig is None:
        emit({"ok": False, "error": "orig read failed"})
        return
    H, W = orig.shape[:2]

    bg = cv2.imread(bg_p, cv2.IMREAD_COLOR)
    if bg is not None:
        bg = cv2.resize(bg, (W, H))
    else:
        bg = np.zeros_like(orig)

    # 1) 首选 rembg 人体分割
    method = "rembg"
    try:
        alpha255 = rembg_alpha(orig)
        alpha = alpha255.astype(np.float32) / 255.0
        # 轻微羽化边缘，减少接缝（人物内部保持 255，像素不变）
        alpha = cv2.GaussianBlur(alpha, (5, 5), 0)
        kept = float((alpha255 > 128).mean())
        # 覆盖率过低：模型未检出人体 → 几何兜底，避免整张涂成背景丢掉人物
        if kept < 0.03:
            method = "center"
            alpha = fallback_mask(H, W)
            kept = float(alpha.mean())
    except Exception as e:  # noqa: BLE001
        method = "center"
        alpha = fallback_mask(H, W)
        kept = float(alpha.mean())

    rgb = alpha[..., None]
    out = (orig.astype(np.float32) * rgb + bg.astype(np.float32) * (1.0 - rgb)).astype(np.uint8)
    if not cv2.imwrite(out_p, out):
        emit({"ok": False, "error": "write output failed"})
        return
    emit({"ok": True, "method": method, "kept": round(kept, 3)})


if __name__ == "__main__":
    main()
