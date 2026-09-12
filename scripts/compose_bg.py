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
    """用 rembg 人体分割返回 alpha(0-255 uint8)，与原图同分辨率。失败抛异常。

    u2net_human_seg 在 320x320 推理，输出接近二值的 mask；alpha_matting / post_process
    对此无改善（trimap 过渡区太窄 → 仍二值），反而 post_process 做形态学二值化。
    因此只取原始 mask，alpha 软过渡 + 去污在 main() 用 estimate_foreground_ml 完成。"""
    import cv2
    import numpy as np

    from rembg import new_session, remove
    import onnxruntime as ort

    opts = ort.SessionOptions()
    opts.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_BASIC

    rgb = cv2.cvtColor(orig_bgr, cv2.COLOR_BGR2RGB)
    H0, W0 = rgb.shape[:2]
    max_side = 2048
    scale = 1.0
    if max(H0, W0) > max_side:
        scale = max_side / max(H0, W0)
        rgb = cv2.resize(rgb, (int(W0 * scale), int(H0 * scale)), interpolation=cv2.INTER_AREA)
    sess = new_session(model_name="u2net_human_seg", sess_opts=opts)
    out = remove(rgb, session=sess)
    if hasattr(out, "convert"):
        out = out.convert("RGBA")
    arr = np.array(out)
    a_small = arr[..., 3]
    if scale != 1.0:
        alpha = cv2.resize(a_small, (W0, H0), interpolation=cv2.INTER_LINEAR)
    else:
        alpha = a_small
    return alpha


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
        # 小 blur 制造自然软过渡（u2net 输出接近二值，硬边像剪纸）
        alpha = cv2.GaussianBlur(alpha, (5, 5), 0)
        kept = float((alpha255 > 128).mean())
        # 覆盖率过低：模型未检出人体 → 几何兜底
        if kept < 0.03:
            method = "center"
            alpha = fallback_mask(H, W)
            fg = orig
            kept = float(alpha.mean())
        else:
            # 对 blur 后的完整过渡区做 ML 去污。pymatting 失败时仍用 rembg 掩膜 + 原图像素，
            # 不能整段退回几何掩膜，否则人体分割结果被丢掉。
            try:
                import pymatting
                rgb_img = cv2.cvtColor(orig, cv2.COLOR_BGR2RGB).astype(np.float64) / 255.0
                fg_ml = pymatting.estimate_foreground_ml(rgb_img, alpha)
                fg = cv2.cvtColor((np.clip(fg_ml, 0, 1) * 255).astype(np.uint8), cv2.COLOR_RGB2BGR)
            except Exception:
                fg = orig
    except Exception as e:  # noqa: BLE001
        method = "center"
        alpha = fallback_mask(H, W)
        fg = orig
        kept = float(alpha.mean())

    rgb = alpha[..., None]
    out = (fg.astype(np.float32) * rgb + bg.astype(np.float32) * (1.0 - rgb)).astype(np.uint8)
    if not cv2.imwrite(out_p, out):
        emit({"ok": False, "error": "write output failed"})
        return
    emit({"ok": True, "method": method, "kept": round(kept, 3)})


if __name__ == "__main__":
    main()
