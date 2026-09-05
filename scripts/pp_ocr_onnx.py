"""统一 OCR 模块 - 支持 Tesseract / EasyOCR / PP-OCR ONNX 三种引擎。

优先级:
  1. Tesseract (本地, chi_sim+eng 中英文支持)
  2. EasyOCR (需安装, 中英日韩多语言)
  3. PP-OCR ONNX (纯 CPU, 官方配套模型)

PP-OCR 分支使用 models/ocr 下同一来源下载的配套文件:
  det.onnx = ch_PP-OCRv4_det_infer.onnx
  rec.onnx = ch_PP-OCRv4_rec_infer.onnx (字符表内嵌在模型元数据里, 与字典严格匹配)
  ppocr_keys_v1.txt 仅作元数据缺失时的兜底。

使用:
  from pp_ocr_onnx import ocr_image, ocr_pdf, get_available_engine, ppocr_recognize
  engine = get_available_engine()          # 返回 "tesseract" / "easyocr" / "pp_ocr"
  lines = ppocr_recognize(np_image)        # [{'text', 'score', 'box'}, ...]
"""

import logging
import math
import os
from typing import List, Tuple

os.environ.setdefault("ORT_DISABLE_LOG", "1")

logger = logging.getLogger(__name__)

# 默认语言配置
DEFAULT_LANG = "chi_sim+eng"

MODEL_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "models", "ocr")
DET_PATH = os.path.join(MODEL_DIR, "det.onnx")
REC_PATH = os.path.join(MODEL_DIR, "rec.onnx")
KEYS_PATH = os.path.join(MODEL_DIR, "ppocr_keys_v1.txt")

# 与 RapidOCR / PaddleOCR v4 官方 config.yaml 一致的参数
DET_LIMIT_SIDE_LEN = 736
DET_LIMIT_TYPE = "min"
DET_MAX_SIDE_LEN = 2000
DET_INPUT_MAX = 3500
DET_MEAN = [0.5, 0.5, 0.5]
DET_STD = [0.5, 0.5, 0.5]
DET_THRESH = 0.3
BOX_THRESH = 0.5
UNCLIP_RATIO = 1.6
REC_IMG_SHAPE = (3, 48, 320)
REC_BATCH_NUM = 6
REC_SCORE = 0.5


def get_available_engine() -> str:
    """返回可用的最佳 OCR 引擎名称。"""
    import importlib.util

    if importlib.util.find_spec("pytesseract") is not None:
        try:
            import pytesseract

            pytesseract.get_languages(config="")
            return "tesseract"
        except Exception:
            pass
    if importlib.util.find_spec("easyocr") is not None:
        return "easyocr"
    return "pp_ocr"


def ocr_image(image, lang: str = DEFAULT_LANG) -> List[str]:
    """对单张图像进行 OCR，返回文本行列表。"""
    engine = get_available_engine()
    logger.info(f"使用 OCR 引擎: {engine}")

    if engine == "tesseract":
        return _ocr_image_tesseract(image, lang)
    if engine == "easyocr":
        return _ocr_image_easyocr(image)
    return [item["text"] for item in ppocr_recognize(image)]


def _ocr_image_tesseract(image, lang: str = DEFAULT_LANG) -> List[str]:
    """Tesseract OCR 实现。"""
    import pytesseract
    import numpy as np
    from PIL import Image

    if isinstance(image, np.ndarray):
        image_rgb = image[:, :, ::-1] if image.ndim == 3 and image.shape[2] == 3 else image
        pil_img = Image.fromarray(image_rgb)
    else:
        pil_img = image

    try:
        text = pytesseract.image_to_string(pil_img, lang=lang)
        return [line.strip() for line in text.strip().splitlines() if line.strip()]
    except Exception as e:
        logger.warning(f"Tesseract OCR 失败: {e}")
        return []


def _ocr_image_easyocr(image) -> List[str]:
    """EasyOCR 实现（多语言，质量较好）。"""
    import easyocr
    import numpy as np

    reader = easyocr.Reader(["ch_sim", "en"], gpu=False)
    arr = image if isinstance(image, np.ndarray) else np.array(image)

    return [text.strip() for _, text, _ in reader.readtext(arr) if text.strip()]


# ── PP-OCR ONNX ─────────────────────────────────────────


def _to_bgr(image) -> "np.ndarray":
    """归一化输入为 BGR numpy 数组（PIL Image 视为 RGB）。"""
    import cv2
    import numpy as np

    if isinstance(image, np.ndarray):
        arr = image
    else:
        arr = np.array(image)
        if arr.ndim == 3 and arr.shape[2] == 3:
            arr = cv2.cvtColor(arr, cv2.COLOR_RGB2BGR)
    if arr.ndim == 2:
        return cv2.cvtColor(arr, cv2.COLOR_GRAY2BGR)
    if arr.shape[2] == 4:
        return cv2.cvtColor(arr, cv2.COLOR_RGBA2BGR)
    return arr


def _read_character_list(rec_session) -> List[str]:
    """读取与识别模型严格匹配的字符表。

    官方 ch_PP-OCRv4_rec_infer.onnx 把字符表写在模型元数据的 character 字段里，
    优先使用它，保证模型与字典永远来自同一份导出。
    """
    try:
        meta = dict(rec_session.get_modelmeta().custom_metadata_map)
    except Exception:
        meta = {}
    chars = meta.get("character")
    if chars:
        items = [line for line in chars.splitlines()]
        if items:
            return items
    if os.path.exists(KEYS_PATH):
        with open(KEYS_PATH, "r", encoding="utf-8") as f:
            return [line.rstrip("\r\n") for line in f]
    raise RuntimeError("PP-OCR 字符字典缺失")


def _ppocr_sessions():
    """加载 (det, rec) 会话与字符表，模块级缓存避免每页重复加载。"""
    import onnxruntime as ort

    global _PP_CACHE
    if _PP_CACHE is None:
        if not (os.path.exists(DET_PATH) and os.path.exists(REC_PATH)):
            raise RuntimeError("PP-OCR 模型文件缺失")
        opts = ort.SessionOptions()
        opts.log_severity_level = 3
        det = ort.InferenceSession(DET_PATH, sess_options=opts, providers=["CPUExecutionProvider"])
        rec = ort.InferenceSession(REC_PATH, sess_options=opts, providers=["CPUExecutionProvider"])
        keys = _read_character_list(rec)
        # 官方解码表: [blank] + 字典 + [空格]，blank 在 0 号位
        table = ["blank"] + keys + [" "]
        channels = rec.get_outputs()[0].shape[-1]
        if channels and len(table) != channels:
            logger.warning(
                f"PP-OCR 字符表 {len(table)} 项与模型输出维度 {channels} 不一致，识别结果可能异常"
            )
        _PP_CACHE = (det, rec, table)
    return _PP_CACHE


_PP_CACHE = None


class DBPostProcess:
    """Differentiable Binarization (PP-OCRv4 det) 后处理。"""

    def __init__(self, thresh=DET_THRESH, box_thresh=BOX_THRESH, unclip_ratio=UNCLIP_RATIO):
        import numpy as np

        self.thresh = thresh
        self.box_thresh = box_thresh
        self.unclip_ratio = unclip_ratio
        self.min_size = 3
        self.max_candidates = 1000
        self.dilation_kernel = np.array([[1, 1], [1, 1]])

    def __call__(self, pred, ori_shape):
        import cv2
        import numpy as np

        src_h, src_w = ori_shape
        pred = pred[:, 0, :, :]
        mask = pred[0] > self.thresh
        mask = cv2.dilate(mask.astype(np.uint8), self.dilation_kernel)
        return self._boxes_from_bitmap(pred[0], mask, src_w, src_h)

    def _boxes_from_bitmap(self, pred, bitmap, dest_width, dest_height):
        import cv2
        import numpy as np
        import pyclipper
        from shapely.geometry import Polygon

        height, width = bitmap.shape
        outs = cv2.findContours((bitmap * 255).astype(np.uint8), cv2.RETR_LIST, cv2.CHAIN_APPROX_SIMPLE)
        contours = outs[1] if len(outs) == 3 else outs[0]

        boxes, scores = [], []
        for contour in contours[: self.max_candidates]:
            box, sside = self._min_area_box(contour)
            if sside < self.min_size:
                continue
            score = self._box_score_fast(pred, box)
            if self.box_thresh > score:
                continue
            unclipped = self._unclip(box, Polygon, pyclipper)
            unclipped, sside = self._min_area_box(unclipped)
            if sside < self.min_size + 2:
                continue
            unclipped[:, 0] = np.clip(np.round(unclipped[:, 0] / width * dest_width), 0, dest_width)
            unclipped[:, 1] = np.clip(np.round(unclipped[:, 1] / height * dest_height), 0, dest_height)
            boxes.append(unclipped.astype(np.int32))
            scores.append(score)
        return np.array(boxes, dtype=np.int32), scores

    @staticmethod
    def _min_area_box(contour):
        import cv2
        import numpy as np

        bounding_box = cv2.minAreaRect(contour)
        points = sorted(list(cv2.boxPoints(bounding_box)), key=lambda x: x[0])

        idx1, idx4 = (0, 1) if points[1][1] > points[0][1] else (1, 0)
        idx2, idx3 = (2, 3) if points[3][1] > points[2][1] else (3, 2)
        box = np.array([points[idx1], points[idx2], points[idx3], points[idx4]])
        return box, min(bounding_box[1])

    @staticmethod
    def _box_score_fast(bitmap, box):
        import cv2
        import numpy as np

        h, w = bitmap.shape[:2]
        box = box.copy()
        xmin = int(np.clip(np.floor(box[:, 0].min()), 0, w - 1))
        xmax = int(np.clip(np.ceil(box[:, 0].max()), 0, w - 1))
        ymin = int(np.clip(np.floor(box[:, 1].min()), 0, h - 1))
        ymax = int(np.clip(np.ceil(box[:, 1].max()), 0, h - 1))

        mask = np.zeros((ymax - ymin + 1, xmax - xmin + 1), dtype=np.uint8)
        box[:, 0] -= xmin
        box[:, 1] -= ymin
        cv2.fillPoly(mask, box.reshape(1, -1, 2).astype(np.int32), 1)
        return cv2.mean(bitmap[ymin : ymax + 1, xmin : xmax + 1], mask)[0]

    def _unclip(self, box, Polygon, pyclipper):
        import numpy as np

        poly = Polygon(box)
        distance = poly.area * self.unclip_ratio / poly.length
        offset = pyclipper.PyclipperOffset()
        offset.AddPath(box, pyclipper.JT_ROUND, pyclipper.ET_CLOSEDPOLYGON)
        return np.array(offset.Execute(distance)).reshape((-1, 1, 2))


def _det_image(image):
    """文本检测，返回 [(box, score), ...]，坐标为原图像素。"""
    import cv2
    import numpy as np

    h, w = image.shape[:2]
    work, scale = image, 1.0
    if max(h, w) > DET_MAX_SIDE_LEN:
        ratio = float(DET_MAX_SIDE_LEN) / max(h, w)
        work = cv2.resize(image, (max(int(w * ratio), 1), max(int(h * ratio), 1)))
        scale = 1.0 / ratio

    hh, ww = work.shape[:2]
    if DET_LIMIT_TYPE == "min":
        ratio = DET_LIMIT_SIDE_LEN / min(hh, ww) if min(hh, ww) < DET_LIMIT_SIDE_LEN else 1.0
    else:
        ratio = DET_LIMIT_SIDE_LEN / max(hh, ww) if max(hh, ww) > DET_LIMIT_SIDE_LEN else 1.0

    resize_h = int(round(int(hh * ratio) / 32)) * 32
    resize_w = int(round(int(ww * ratio) / 32)) * 32
    if max(resize_h, resize_w) > DET_INPUT_MAX:
        shrink = float(DET_INPUT_MAX) / max(resize_h, resize_w)
        resize_h = int(round(int(resize_h * shrink) / 32)) * 32
        resize_w = int(round(int(resize_w * shrink) / 32)) * 32
    if resize_h <= 0 or resize_w <= 0:
        return []

    resized = cv2.resize(work, (resize_w, resize_h))
    blob = (resized.astype(np.float32) / 255.0 - np.array(DET_MEAN, dtype=np.float32)) / np.array(
        DET_STD, dtype=np.float32
    )
    blob = blob.transpose((2, 0, 1))[np.newaxis, ...].astype(np.float32)

    det, _, _ = _ppocr_sessions()
    scores = det.run(None, {"x": blob})[0]
    boxes, box_scores = DBPostProcess()(scores, (hh, ww))
    if len(boxes) == 0:
        return []

    out = []
    for box, score in zip(boxes, box_scores):
        if box.shape[0] != 4:
            continue
        if scale != 1.0:
            box = (box.astype(np.float32) * scale).astype(np.int32)
        rect_w = int(np.linalg.norm(box[0] - box[1]))
        rect_h = int(np.linalg.norm(box[0] - box[3]))
        if rect_w <= 3 or rect_h <= 3:
            continue
        out.append((box, float(score)))
    return out


def _crop_box(image, box):
    """把四边形文本框透视裁剪成横向条带。"""
    import cv2
    import numpy as np

    width = int(max(np.linalg.norm(box[0] - box[1]), np.linalg.norm(box[2] - box[3])))
    height = int(max(np.linalg.norm(box[0] - box[3]), np.linalg.norm(box[1] - box[2])))
    if width < 2 or height < 2:
        return None

    pts = np.array([[0, 0], [width, 0], [width, height], [0, height]], dtype=np.float32)
    matrix = cv2.getPerspectiveTransform(box.astype(np.float32), pts)
    crop = cv2.warpPerspective(
        image, matrix, (width, height), borderMode=cv2.BORDER_REPLICATE, flags=cv2.INTER_CUBIC
    )
    if crop.shape[0] * 1.0 / crop.shape[1] >= 1.5:
        crop = np.rot90(crop)
    return crop


def _rec_transform(crops: List["np.ndarray"], max_wh_ratio: float) -> "np.ndarray":
    """批量识别预处理: 高度 48，宽度按整批最大宽高比，右侧补零。"""
    img_c, img_h, img_w = REC_IMG_SHAPE
    img_w = int(img_h * max_wh_ratio)

    import cv2
    import numpy as np

    batch = []
    for crop in crops:
        h, w = crop.shape[:2]
        ratio = w / float(h)
        resized_w = img_w if math.ceil(img_h * ratio) > img_w else int(math.ceil(img_h * ratio))
        resized = cv2.resize(crop, (resized_w, img_h)).astype(np.float32)
        resized = resized.transpose((2, 0, 1)) / 255.0
        resized -= 0.5
        resized /= 0.5
        padded = np.zeros((img_c, img_h, img_w), dtype=np.float32)
        padded[:, :, 0:resized_w] = resized
        batch.append(padded[np.newaxis, :])
    return np.concatenate(batch).astype(np.float32)


def _ctc_decode(logits: "np.ndarray", table: List[str]) -> List[Tuple[str, float]]:
    """CTC 解码: blank 在 0 号位，先合并连续重复，再丢弃 blank。"""
    import numpy as np

    idxs = np.argmax(logits, axis=2)
    probs = np.max(logits, axis=2)
    out = []
    for row_idx, row_prob in zip(idxs, probs):
        keep = np.ones(len(row_idx), dtype=bool)
        keep[1:] = row_idx[1:] != row_idx[:-1]
        keep &= row_idx != 0
        conf = float(np.mean(row_prob[keep])) if keep.any() else 0.0
        text = "".join(table[i] for i in row_idx[keep] if 0 < i < len(table))
        out.append((text, conf))
    return out


def _ctc_decode_topk(logits: "np.ndarray", table: List[str], topk: int = 6):
    """CTC 逐字符解码，返回 [(char, conf, candidates)]。

    rec 模型输出已是 softmax 概率（各帧和为 1），conf 直接取帧内该字符的概率，
    不能再套 softmax。对每个合并后的非 blank 段，取段内该字符概率最大的帧，
    用该帧的 top-K 作为候选——top-K 天然落在形近字混淆集上，供上层通用纠错。
    """
    import numpy as np

    idxs = np.argmax(logits, axis=1)
    chars = []
    n = len(idxs)
    i = 0
    while i < n:
        v = int(idxs[i])
        if v == 0:
            i += 1
            continue
        j = i
        while j < n and idxs[j] == v:
            j += 1
        seg = logits[i:j]
        frame = seg[int(np.argmax(seg[:, v]))]
        conf = float(frame[v])
        cands = []
        for ci in np.argsort(frame)[::-1][:topk]:
            ci = int(ci)
            if 0 < ci < len(table):
                cands.append((table[ci], float(frame[ci])))
        ch = table[v] if 0 < v < len(table) else ""
        chars.append((ch, conf, cands))
        i = j
    return chars


def ppocr_rec_line_chars(crop, topk: int = 6):
    """识别单行文本条带，返回逐字符 (char, conf, candidates)。

    crop 为 BGR 或灰度 numpy 数组；供 CJK 行路由做通用纠错。
    """
    import numpy as np  # noqa: F401

    bgr = _to_bgr(crop)
    if bgr.size == 0 or bgr.shape[1] < 2 or bgr.shape[0] < 2:
        return []
    _, rec, table = _ppocr_sessions()
    max_wh_ratio = max(REC_IMG_SHAPE[2] / float(REC_IMG_SHAPE[1]),
                       bgr.shape[1] * 1.0 / bgr.shape[0])
    blob = _rec_transform([bgr], max_wh_ratio)
    logits = rec.run(None, {"x": blob})[0][0]
    return _ctc_decode_topk(logits, table, topk)


def ppocr_recognize(image, text_score: float = REC_SCORE) -> List[dict]:
    """PP-OCRv4 det+rec 全流程，返回 [{'text','score','box': [x,y,w,h]}]。"""
    import cv2  # noqa: F401
    import numpy as np  # noqa: F401

    bgr = _to_bgr(image)
    det_result = _det_image(bgr)
    if not det_result:
        return []

    _, rec, table = _ppocr_sessions()
    det_result = sorted(det_result, key=lambda item: (item[0][0][1], item[0][0][0]))

    results: List[dict] = []
    for beg in range(0, len(det_result), REC_BATCH_NUM):
        group = det_result[beg : beg + REC_BATCH_NUM]
        crops = []
        crop_boxes = []
        max_wh_ratio = REC_IMG_SHAPE[2] / float(REC_IMG_SHAPE[1])
        for box, _score in group:
            crop = _crop_box(bgr, box)
            if crop is None:
                continue
            max_wh_ratio = max(max_wh_ratio, crop.shape[1] * 1.0 / crop.shape[0])
            crops.append(crop)
            crop_boxes.append(box)

        if not crops:
            continue

        blob = _rec_transform(crops, max_wh_ratio)
        logits = rec.run(None, {"x": blob})[0]
        for _crop, box, (text, score) in zip(crops, crop_boxes, _ctc_decode(logits, table)):
            if not text or score < text_score:
                continue
            x = int(max(min(np.floor(box[:, 0].min()), bgr.shape[1] - 1), 0))
            y = int(max(min(np.floor(box[:, 1].min()), bgr.shape[0] - 1), 0))
            w = int(max(min(np.ceil(box[:, 0].max()), bgr.shape[1]) - x, 1))
            h = int(max(min(np.ceil(box[:, 1].max()), bgr.shape[0]) - y, 1))
            results.append({"text": text, "score": round(score, 4), "box": [x, y, w, h]})

    return results


def _ocr_image_pp_ocr(image) -> List[str]:
    """PP-OCR 识别，仅返回文本行（兼容旧调用方）。"""
    return [item["text"] for item in ppocr_recognize(image)]


def ocr_pdf(pdf_path: str, lang: str = DEFAULT_LANG, dpi: int = 150) -> str:
    """对 PDF 文件进行 OCR，返回合并文本。"""
    engine = get_available_engine()
    logger.info(f"PDF OCR 使用引擎: {engine}")

    if engine == "tesseract":
        return _ocr_pdf_tesseract(pdf_path, lang, dpi)
    if engine == "easyocr":
        return _ocr_pdf_easyocr(pdf_path)
    return _ocr_pdf_pp_ocr(pdf_path)


def _ocr_pdf_tesseract(pdf_path: str, lang: str, dpi: int = 150) -> str:
    """Tesseract PDF OCR。"""
    import io
    import fitz
    import pytesseract
    from PIL import Image

    text_parts = []
    doc = fitz.open(pdf_path)
    try:
        for i, page in enumerate(doc):
            pix = page.get_pixmap(matrix=fitz.Matrix(dpi / 72, dpi / 72))
            img = Image.open(io.BytesIO(pix.tobytes("png")))
            text_parts.append(pytesseract.image_to_string(img, lang=lang))
            if (i + 1) % 5 == 0:
                logger.info(f"  OCR 进度: {i + 1}/{len(doc)} 页")
    finally:
        doc.close()

    return "\n\n".join(text_parts)


def _ocr_pdf_easyocr(pdf_path: str) -> str:
    """EasyOCR PDF OCR。"""
    import io
    import fitz
    import easyocr
    import numpy as np
    from PIL import Image

    reader = easyocr.Reader(["ch_sim", "en"], gpu=False)
    doc = fitz.open(pdf_path)
    text_parts = []
    try:
        for i, page in enumerate(doc):
            pix = page.get_pixmap(matrix=fitz.Matrix(2, 2))
            img = Image.open(io.BytesIO(pix.tobytes("png")))
            lines = [text for _, text, _ in reader.readtext(np.array(img)) if text.strip()]
            text_parts.append("\n".join(lines))
            if (i + 1) % 5 == 0:
                logger.info(f"  OCR 进度: {i + 1}/{len(doc)} 页")
    finally:
        doc.close()
    return "\n\n".join(text_parts)


def _ocr_pdf_pp_ocr(pdf_path: str) -> str:
    """PP-OCR PDF OCR。"""
    import io
    import fitz
    import cv2
    import numpy as np
    from PIL import Image

    text_parts = []
    doc = fitz.open(pdf_path)
    try:
        for i, page in enumerate(doc):
            pix = page.get_pixmap(matrix=fitz.Matrix(2, 2))
            img = Image.open(io.BytesIO(pix.tobytes("png")))
            bgr = cv2.cvtColor(np.array(img), cv2.COLOR_RGB2BGR)
            lines = [item["text"] for item in ppocr_recognize(bgr)]
            text_parts.append("\n".join(lines))
            if (i + 1) % 5 == 0:
                logger.info(f"  OCR 进度: {i + 1}/{len(doc)} 页")
    finally:
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
