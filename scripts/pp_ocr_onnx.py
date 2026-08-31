"""统一 OCR 模块 - 支持 Tesseract / PP-OCR ONNX / EasyOCR 三种引擎。

优先级:
  1. Tesseract (本地, 快速, chi_sim+eng 中英文支持)
  2. EasyOCR (需安装, 中英日韩多语言)
  3. PP-OCR ONNX (实验性, 轻量级 ONNX 模型, 当前识别效果待优化)

使用:
  from pp_ocr_onnx import ocr_image, ocr_pdf, get_available_engine
  engine = get_available_engine()          # 返回 "tesseract" / "easyocr" / "pp_ocr"
  text = ocr_pdf("/path/to/pdf.pdf")       # 自动选择最佳引擎
  lines = ocr_image(np_image)              # 返回 [(text, bbox), ...]
"""

import logging
import os
from typing import List, Optional, Tuple

logger = logging.getLogger(__name__)

# 默认语言配置
DEFAULT_LANG = "chi_sim+eng"


def get_available_engine() -> str:
    """返回可用的最佳 OCR 引擎名称。"""
    try:
        import pytesseract  # noqa: F401
        return "tesseract"
    except ImportError:
        pass
    try:
        import easyocr  # noqa: F401
        return "easyocr"
    except ImportError:
        pass
    return "pp_ocr"  # 始终可用（ONNX 模型文件存在即可）


def ocr_image(image, lang: str = DEFAULT_LANG) -> List[str]:
    """对单张图像进行 OCR，返回文本行列表。

    Args:
        image: numpy array (BGR/RGB) 或 PIL Image
        lang: Tesseract 语言代码，如 "chi_sim+eng"

    Returns:
        List[str]: 每行文本
    """
    engine = get_available_engine()
    logger.info(f"使用 OCR 引擎: {engine}")

    if engine == "tesseract":
        return _ocr_image_tesseract(image, lang)
    elif engine == "easyocr":
        return _ocr_image_easyocr(image)
    else:
        return _ocr_image_pp_ocr(image)


def _ocr_image_tesseract(image, lang: str = DEFAULT_LANG) -> List[str]:
    """Tesseract OCR 实现。"""
    import pytesseract
    from PIL import Image
    import numpy as np

    if isinstance(image, np.ndarray):
        if image.ndim == 3 and image.shape[2] == 3:
            # BGR -> RGB
            image_rgb = image[:, :, ::-1]
        else:
            image_rgb = image
        pil_img = Image.fromarray(image_rgb)
    else:
        pil_img = image

    try:
        text = pytesseract.image_to_string(pil_img, lang=lang)
        lines = [line.strip() for line in text.strip().splitlines() if line.strip()]
        return lines
    except Exception as e:
        logger.warning(f"Tesseract OCR 失败: {e}")
        return []


def _ocr_image_easyocr(image) -> List[str]:
    """EasyOCR 实现（多语言，质量较好）。"""
    import easyocr
    import numpy as np

    reader = easyocr.Reader(["ch_sim", "en"], gpu=False)

    if isinstance(image, np.ndarray):
        # EasyOCR 接受 BGR numpy array
        arr = image
    else:
        arr = np.array(image)

    results = reader.readtext(arr)
    lines = []
    for _, text, _ in results:
        if text.strip():
            lines.append(text.strip())
    return lines


def _ocr_image_pp_ocr(image) -> List[str]:
    """PP-OCR ONNX 实现（实验性，轻量级）。"""
    import cv2
    import numpy as np

    try:
        import onnxruntime as ort
    except ImportError:
        logger.warning("onnxruntime 未安装，回退到 Tesseract")
        return _ocr_image_tesseract(image)

    det_path = os.path.join(os.path.dirname(__file__), "..", "models", "ocr", "det.onnx")
    rec_path = os.path.join(os.path.dirname(__file__), "..", "models", "ocr", "rec.onnx")
    dict_path = os.path.join(os.path.dirname(__file__), "..", "models", "ocr", "ppocr_keys_v1.txt")

    if not all(os.path.exists(p) for p in [det_path, rec_path, dict_path]):
        logger.warning("PP-OCR 模型文件缺失，回退到 Tesseract")
        return _ocr_image_tesseract(image)

    # 加载模型
    try:
        det_session = ort.InferenceSession(det_path, providers=["CPUExecutionProvider"])
        rec_session = ort.InferenceSession(rec_path, providers=["CPUExecutionProvider"])
    except Exception as e:
        logger.warning(f"PP-OCR 模型加载失败: {e}，回退到 Tesseract")
        return _ocr_image_tesseract(image)

    # 加载词表
    dict_chars = []
    with open(dict_path, encoding="utf-8") as f:
        dict_chars = [line.strip() for line in f if line.strip()]
    dict_full = dict_chars + [" ", "?", "<blank>"]

    # 预处理图像
    if isinstance(image, np.ndarray):
        if image.ndim == 2:
            img_bgr = cv2.cvtColor(image, cv2.COLOR_GRAY2BGR)
        elif image.ndim == 3 and image.shape[2] == 3:
            img_bgr = image
        else:
            img_bgr = cv2.cvtColor(image, cv2.COLOR_RGBA2BGR)
    else:
        img_bgr = cv2.cvtColor(np.array(image), cv2.COLOR_RGB2BGR)

    H, W = img_bgr.shape[:2]

    # 文本检测
    long_side = 960
    scale = long_side / max(H, W)
    nw, nh = int(W * scale), int(H * scale)
    nw = (nw // 32) * 32
    nh = (nh // 32) * 32
    resized = cv2.resize(img_bgr, (nw, nh))
    arr = resized.astype(np.float32) / 255.0
    mean = np.array([0.485, 0.456, 0.406]).reshape(1, 1, 3).astype(np.float32)
    std = np.array([0.229, 0.224, 0.225]).reshape(1, 1, 3).astype(np.float32)
    norm = ((arr - mean) / std).astype(np.float32)
    blob = np.transpose(norm, (2, 0, 1))[np.newaxis, ...].astype(np.float32)

    try:
        scores = det_session.run(None, {"x": blob})[0][0, 0].astype(np.float32)
    except Exception as e:
        logger.warning(f"PP-OCR 检测推理失败: {e}，回退到 Tesseract")
        return _ocr_image_tesseract(image)

    # 后处理：找文本区域
    binary = (scores > 0.3).astype(np.uint8) * 255
    kernel = cv2.getStructuringElement(cv2.MORPH_ELLIPSE, (11, 11))
    dilated = cv2.dilate(binary, kernel, iterations=4)
    eroded = cv2.erode(dilated, kernel, iterations=3)
    contours, _ = cv2.findContours(eroded, cv2.RETR_EXTERNAL, cv2.CHAIN_APPROX_SIMPLE)

    # 识别每个文本区域
    lines = []
    for c in contours:
        if cv2.contourArea(c) < 100:
            continue
        x, y, w, h = cv2.boundingRect(c)
        ox, oy, ow, oh = int(x / scale), int(y / scale), int(w / scale), int(h / scale)

        crop = img_bgr[oy:oy + oh, ox:ox + ow]
        if crop.size == 0 or crop.shape[0] < 10:
            continue

        # 识别预处理
        ch, cw = crop.shape[:2]
        target_h = 48
        rs = target_h / ch
        rnw = int(cw * rs)
        rnw = ((rnw // 4) + 1) * 4
        rcrop = cv2.resize(crop, (rnw, target_h))
        rarr = rcrop.astype(np.float32) / 255.0
        rnorm = ((rarr - 0.5) / 0.5).astype(np.float32)
        rblob = np.transpose(rnorm, (2, 0, 1))[np.newaxis, ...].astype(np.float32)

        try:
            logits = rec_session.run(None, {"x": rblob})[0]
            argmax = np.argmax(logits[0], axis=1)
            text_parts = []
            prev = len(dict_full) - 1
            for idx in argmax:
                if idx != prev and idx < len(dict_full):
                    text_parts.append(dict_full[idx])
                prev = idx
            text = "".join(text_parts).strip()
            if text and text not in ("", " ", "<blank>"):
                lines.append(text)
        except Exception:
            pass

    return lines


def ocr_pdf(pdf_path: str, lang: str = DEFAULT_LANG, dpi: int = 150) -> str:
    """对 PDF 文件进行 OCR，返回合并文本。

    Args:
        pdf_path: PDF 文件路径
        lang: OCR 语言
        dpi: 渲染 DPI（仅 Tesseract 引擎使用）

    Returns:
        合并后的文本字符串
    """
    engine = get_available_engine()
    logger.info(f"PDF OCR 使用引擎: {engine}")

    if engine == "tesseract":
        return _ocr_pdf_tesseract(pdf_path, lang, dpi)
    elif engine == "easyocr":
        return _ocr_pdf_easyocr(pdf_path)
    else:
        return _ocr_pdf_pp_ocr(pdf_path)


def _ocr_pdf_tesseract(pdf_path: str, lang: str, dpi: int = 150) -> str:
    """Tesseract PDF OCR。"""
    import fitz
    import pytesseract
    from PIL import Image
    import io
    import os

    text_parts = []
    doc = fitz.open(pdf_path)
    try:
        for i, page in enumerate(doc):
            pix = page.get_pixmap(matrix=fitz.Matrix(dpi / 72, dpi / 72))
            img_data = pix.tobytes("png")
            img = Image.open(io.BytesIO(img_data))
            txt = pytesseract.image_to_string(img, lang=lang)
            text_parts.append(txt)
            if (i + 1) % 5 == 0:
                logger.info(f"  OCR 进度: {i + 1}/{len(doc)} 页")
    finally:
        doc.close()

    return "\n\n".join(text_parts)


def _ocr_pdf_easyocr(pdf_path: str) -> str:
    """EasyOCR PDF OCR。"""
    import fitz
    import easyocr
    from PIL import Image
    import io

    reader = easyocr.Reader(["ch_sim", "en"], gpu=False)
    doc = fitz.open(pdf_path)
    text_parts = []

    for i, page in enumerate(doc):
        pix = page.get_pixmap(matrix=fitz.Matrix(2, 2))
        img_data = pix.tobytes("png")
        img = Image.open(io.BytesIO(img_data))
        results = reader.readtext(np.array(img))
        lines = [text for _, text, _ in results if text.strip()]
        text_parts.append("\n".join(lines))
        if (i + 1) % 5 == 0:
            logger.info(f"  OCR 进度: {i + 1}/{len(doc)} 页")

    doc.close()
    return "\n\n".join(text_parts)


def _ocr_pdf_pp_ocr(pdf_path: str) -> str:
    """PP-OCR PDF OCR。"""
    import fitz
    from PIL import Image
    import io

    text_parts = []
    doc = fitz.open(pdf_path)

    for i, page in enumerate(doc):
        pix = page.get_pixmap(matrix=fitz.Matrix(2, 2))
        img_data = pix.tobytes("png")
        img = Image.open(io.BytesIO(img_data))
        lines = _ocr_image_pp_ocr(img)
        text_parts.append("\n".join(lines))
        if (i + 1) % 5 == 0:
            logger.info(f"  OCR 进度: {i + 1}/{len(doc)} 页")

    doc.close()
    return "\n\n".join(text_parts)


if __name__ == "__main__":
    import sys
    if len(sys.argv) < 2:
        print(f"用法: {sys.argv[0]} <image_or_pdf_path>")
        sys.exit(1)
    result = ocr_pdf(sys.argv[1]) if sys.argv[1].lower().endswith(".pdf") else ocr_image(sys.argv[1])
    for line in result:
        print(line)
