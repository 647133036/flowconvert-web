"""结构化 OCR 识别 CLI - 供 FlowConvert 后端调用。

用法:
    python3 ocr.py <input_path> --options '<json>' [--outdir <dir>]

options 字段:
    mode      "general" | "advanced"     识别模式；advanced 启用 PP-DocLayout+SLANeXt 表格结构识别
    quality   "normal" | "high"          识别质量（high 渲染更高分辨率，耗时更长）
    charset   "auto" | "zh_hans" | "zh_hant"  输出字形（繁简转换）
    redbox    true | false               是否在结果图上标注文字框
    optimize  true | false               文本优化（过滤页眉页脚）
    translate true | false               是否翻译为简体中文
    formats   ["txt","docx","xlsx","json"] 需要导出的格式
    engine    "auto" | "tesseract" | "pp_ocr"

输出 (stdout, JSON):
    {"success": true, "text": "...", "translated_text": "", "engine": "tesseract",
     "pages": 2, "chars": 42, "charset": "zh_hans", "layout": {...},
     "blocks": [...], "lines": [...], "files": {"txt": "..."},
     "elapsed_ms": 1234, "warnings": [], "options": {...}}

识别策略（全部本地执行，无外部 API 依赖）:
    1. PDF 有文本层 -> PyMuPDF 字符级抽取（无损，保留字号与坐标）
    2. 扫描件/图片  -> Tesseract（general 用 chi_sim+eng，advanced 追加 chi_tra 并多 PSM 取最优）
    3. 版式分析：标题(字号) / 列表(项目符号) / 页眉页脚(位置+跨页重复) / 双栏(x 分裂) /
       表格(PP-DocLayout + SLANeXt，启发式兜底) / 公式(数学符号密度)
    4. 繁简转换：OpenCC（s2t / t2s）
    5. 翻译：复用同目录 translate.py，目标为简体中文
"""

import argparse
import json
import os
import re
import sys
import time
from dataclasses import dataclass, field

from pdf_utils import text_layer_is_garbled

IMAGE_EXTS = (".jpg", ".jpeg", ".png", ".bmp", ".webp", ".gif", ".tif", ".tiff")

QUALITY_PROFILE = {
    "normal": {"dpi": 150, "max_width": 1400, "psms": (6, 3)},
    "high": {"dpi": 300, "max_width": 2400, "psms": (6, 3, 4, 11)},
}

# 语言预设 -> 候选 tesseract 语言组合（按优先级，取第一个语言包齐全的组合）
LANG_PRESETS = {
    "auto": ("chi_sim+chi_tra+eng", "chi_sim+eng", "chi_tra+eng", "eng"),
    "zh": ("chi_sim+chi_tra", "chi_sim", "chi_tra"),
    "en": ("eng",),
    "ja": ("jpn+eng", "jpn"),
    "ko": ("kor+eng", "kor"),
    "de": ("deu+eng", "deu"),
    "fr": ("fra+eng", "fra"),
    "es": ("spa+eng", "spa"),
    "ru": ("rus+eng", "rus"),
    "multi": (
        "eng+chi_sim+chi_tra+jpn+kor",
        "eng+chi_sim+chi_tra",
        "chi_sim+chi_tra+eng",
        "eng",
    ),
}

MATH_SYMBOLS = set(
    "∑∏∫∮√≠≤≥≈≡±×÷∂∇∞∴∵∈∉⊂⊃∪∩∅∀∃αβγδεζηθικλμνξοπρστυφχψω"
    "ΑΒΓΔΕΖΗΘΙΚΛΜΝΞΟΠΡΣΤΥΦΧΨΩ°′″‰§№→←↑↓⇒⇔"
)

LIST_PREFIX = re.compile(
    r"^\s*(?:[•·▪◦●○◆■□★☆]+"
    r"|[\-\*\+]\s+"
    r"|\(\d{1,3}\)\s*|[\(（]\s*[一二三四五六七八九十]+\s*[\)）]\s*"
    r"|\d{1,2}[.、)]\s+"
    r"|[①②③④⑤⑥⑦⑧⑨⑩⑪⑫⑬⑭⑮⑯⑰⑱⑲⑳]\s*"
    r"|[\(（][a-z][\)）]\s+|[a-z]\)\s+)"
)

HEADER_TITLE_WORDS = re.compile(
    r"(页码|第\s*\d+\s*页|page\s*\d+|footer|页脚|第\s*\d+\s*章|chapter\s*\d+)", re.I
)

CJK_TOKEN_CAP = 10
REDBOX_MAX_PAGES = 12


@dataclass
class Word:
    text: str
    x: int
    y: int
    w: int
    h: int
    conf: float
    page: int = 0
    size: float = 0.0

    @property
    def cx(self):
        return self.x + self.w // 2

    @property
    def cy(self):
        return self.y + self.h // 2


@dataclass
class Line:
    text: str
    x: int
    y: int
    w: int
    h: int
    conf: float
    size: float
    page: int
    words: list = field(default_factory=list)

    @property
    def cx(self):
        return self.x + self.w // 2

    @property
    def cy(self):
        return self.y + self.h // 2

    def to_dict(self):
        return {
            "text": self.text,
            "bbox": [self.x, self.y, self.x + self.w, self.y + self.h],
            "conf": round(self.conf, 3),
            "size": round(self.size, 1),
            "page": self.page + 1,
        }


@dataclass
class Block:
    type: str
    text: str
    page: int
    bbox: tuple
    lines: list = field(default_factory=list)
    conf: float = 0.0
    cells: list = None

    def to_dict(self):
        d = {
            "type": self.type,
            "text": self.text,
            "page": self.page + 1,
            "bbox": [int(v) for v in self.bbox],
            "conf": round(self.conf, 3),
        }
        if self.cells is not None:
            d["cells"] = self.cells
        elif self.lines:
            d["lines"] = [ln.to_dict() for ln in self.lines]
        return d


def _log(msg):
    print(msg, file=sys.stderr)


def _median(values):
    vals = sorted(v for v in values if v and v > 0)
    return float(vals[len(vals) // 2]) if vals else 0.0


def _is_cjk(ch):
    return "\u4e00" <= ch <= "\u9fff"


def _join_words(items):
    """Join word tokens, inserting a space only when the horizontal gap is real.

    A gap above 0.3x the token height is a deliberate space; smaller gaps are
    either font metrics between adjacent CJK glyphs or tight Latin-CJK spacing.
    """
    parts = []
    for i, it in enumerate(items):
        if i:
            prev = items[i - 1]
            gap = it.x - (prev.x + prev.w)
            if gap > 0.3 * max(it.h, prev.h, 1):
                parts.append(" ")
        parts.append(it.text)
    return "".join(parts).strip()


# ── 繁简转换 ─────────────────────────────────────────────


class CharsetConverter:
    def __init__(self, charset):
        self.target = charset
        self._cc = None

    def convert(self, text):
        if self.target not in ("zh_hans", "zh_hant") or not text:
            return text
        if self._cc is None:
            try:
                from opencc import OpenCC

                self._cc = OpenCC("t2s") if self.target == "zh_hans" else OpenCC("s2t")
            except Exception as exc:
                _log(f"[OCR] OpenCC 不可用，跳过繁简转换: {exc}")
                return text
        return self._cc.convert(text)


def detect_charset(text):
    """Pick the variant the text reads closest to, using OpenCC in both directions."""
    if not text:
        return "zh_hans"
    try:
        from opencc import OpenCC

        to_s = OpenCC("t2s").convert(text)
        to_t = OpenCC("s2t").convert(text)
    except Exception:
        return "zh_hans"
    # Distance to each variant: fewer edits means the text is already in that form.
    dist_s = _edits(text, to_s)
    dist_t = _edits(text, to_t)
    return "zh_hant" if dist_t < dist_s else "zh_hans"


def _edits(a, b):
    n = min(len(a), len(b))
    diff = sum(1 for i in range(n) if a[i] != b[i])
    return diff + abs(len(a) - len(b))


# ── OCR 引擎 ─────────────────────────────────────────────


class TesseractEngine:
    name = "tesseract"

    def __init__(self, mode="general", quality="normal", lang="auto"):
        import importlib.util

        if importlib.util.find_spec("pytesseract") is None:
            raise RuntimeError("pytesseract 未安装")
        try:
            import pytesseract

            langs = set(pytesseract.get_languages(config=""))
        except Exception as exc:
            raise RuntimeError(f"tesseract 不可用: {exc}") from exc

        self.lang = self._pick_lang(lang, mode, langs)

        self.profile = QUALITY_PROFILE.get(quality, QUALITY_PROFILE["normal"])
        self.dpi = self.profile["dpi"]
        self.max_width = self.profile["max_width"]

    @staticmethod
    def _pick_lang(lang, mode, available):
        """按语言预设挑一个语言包齐全的组合；缺失时逐步降级到单个语言包。"""
        if lang not in LANG_PRESETS:
            lang = "auto"
        combos = list(LANG_PRESETS[lang])
        if lang == "auto" and mode == "advanced" and "chi_sim+chi_tra+eng" in combos:
            combos = [c for c in combos if "chi_tra" not in c] + combos
        for combo in combos:
            wanted = set(combo.split("+"))
            if available & wanted == wanted:
                return combo
        if lang == "auto":
            for combo in LANG_PRESETS["auto"]:
                wanted = set(combo.split("+"))
                if available & wanted == wanted:
                    return combo
        # 最后兜底：任选一个可用的拉丁/中文语言包
        for single in ("eng", "chi_sim", "chi_tra", "jpn", "kor"):
            if single in available:
                return single
        raise RuntimeError("tesseract 语言包缺失")

    @staticmethod
    def _prepare(gray, max_width):
        import cv2

        h, w = gray.shape[:2]
        if w >= max_width:
            return gray
        factor = min(int(round(max_width / float(w))), 4)
        return cv2.resize(gray, (w * factor, h * factor), interpolation=cv2.INTER_CUBIC)

    def _run_psm(self, img, psm, lang):
        """Run one tesseract PSM pass and return the recognized words."""
        import pytesseract

        data = pytesseract.image_to_data(
            img, lang=lang, config=f"--psm {psm}", output_type=pytesseract.Output.DICT
        )
        words = []
        for i, raw in enumerate(data["text"]):
            text = str(raw).strip()
            if not text:
                continue
            try:
                conf = float(data["conf"][i])
            except (TypeError, ValueError):
                conf = 0.0
            if conf < 0:
                conf = 0.0
            words.append(
                Word(
                    text=text,
                    x=int(data["left"][i]),
                    y=int(data["top"][i]),
                    w=max(int(data["width"][i]), 1),
                    h=max(int(data["height"][i]), 1),
                    conf=conf,
                    page=0,
                    size=float(data["height"][i]),
                )
            )
        return words

    def image_words(self, image):
        """Return list[Word] (box + confidence) for one PIL image.

        逐个 PSM 尝试并按平均置信度择优；首个 PSM 已足够好时提前收敛，
        避免在大尺寸图上重复跑 PSM（PSM 11 单次可达数十秒）。
        """
        import cv2
        import numpy as np
        from PIL import Image

        gray = self._prepare(
            cv2.cvtColor(np.array(image.convert("RGB")), cv2.COLOR_RGB2GRAY), self.max_width
        )
        img = Image.fromarray(gray)

        best, best_score = [], -1.0
        for psm in self.profile["psms"]:
            words = self._run_psm(img, psm, self.lang)
            if not words:
                continue
            score = sum(w.conf for w in words) / len(words)
            if score > best_score or (score == best_score and len(words) > len(best)):
                best, best_score = words, score
            if score >= 0.6 and len(words) >= 20:
                return best
        return best


class PPocREngine:
    """PP-OCR ONNX 引擎（官方配套 det/rec 模型，字符表内嵌在模型元数据中）。"""

    name = "pp_ocr"

    def __init__(self, mode="general", quality="normal"):
        import importlib.util

        for mod in ("cv2", "numpy", "onnxruntime", "pyclipper", "shapely"):
            if importlib.util.find_spec(mod) is None:
                raise RuntimeError(f"{mod} 未安装")
        self.mode = mode
        self.quality = quality
        _pp_scripts_path()
        from pp_ocr_onnx import _ppocr_sessions

        _ppocr_sessions()  # 预热：尽早暴露模型缺失或字符表不匹配

    def image_words(self, image):
        """Return list[Word] (box + confidence) for one PIL image."""
        import cv2
        import numpy as np

        from pp_ocr_onnx import ppocr_recognize

        bgr = cv2.cvtColor(np.array(image.convert("RGB")), cv2.COLOR_RGB2BGR)
        words = []
        for item in ppocr_recognize(bgr):
            x, y, w, h = item["box"]
            words.append(
                Word(
                    text=item["text"],
                    x=int(x),
                    y=int(y),
                    w=max(int(w), 1),
                    h=max(int(h), 1),
                    conf=float(item["score"]),
                    page=0,
                    size=float(h),
                )
            )
        return words


def _pp_scripts_path():
    here = os.path.dirname(os.path.abspath(__file__))
    if here not in sys.path:
        sys.path.insert(0, here)


def get_engine(mode="general", quality="normal", prefer="auto", lang="auto"):
    if prefer == "pp_ocr":
        candidates = ("pp_ocr", "tesseract")
    else:
        candidates = ("tesseract", "pp_ocr")
    last = None
    for name in candidates:
        try:
            if name == "pp_ocr":
                return PPocREngine(mode=mode, quality=quality)
            return TesseractEngine(mode=mode, quality=quality, lang=lang)
        except Exception as exc:
            last = exc
            _log(f"[OCR] {name} 不可用: {exc}")
    raise RuntimeError(f"没有可用的 OCR 引擎: {last}")


def _char_set(text):
    """用于跨来源比对的字符集合: CJK 与字母数字, 拉丁转小写, 去空白与标点。"""
    out = set()
    for c in text:
        o = ord(c)
        if (
            "\u4e00" <= c <= "\u9fff"
            or "\u3400" <= c <= "\u4dbf"
            or "\u3040" <= c <= "\u30ff"
            or "\uac00" <= c <= "\ud7af"
            or c.isalnum()
        ):
            out.add(c.lower())
    return out


def text_layer_mismatch(tl_text, ocr_text):
    """文本层与同页图像 OCR 结果的字符集合重合度过低则判为乱码。

    正确文本层与图像 OCR 同源, 字符集合高度重合; 乱码文本层(错码/私用区/错位)
    与图像 OCR 几乎不相交。OCR 误识只影响少量字符, 不会让重合度跌到 1/4 以下。
    """
    a, b = _char_set(tl_text), _char_set(ocr_text)
    if len(a) < 20 or len(b) < 20:
        return False
    inter = len(a & b)
    return inter / len(a) < 0.25 or inter / len(b) < 0.25


def _det_lang():
    """方向检测用的语言: 优先中英, 缺包时退到任一可用。"""
    try:
        import pytesseract

        avail = set(pytesseract.get_languages(config=""))
    except Exception:
        return "eng"
    if {"chi_sim", "eng"} <= avail:
        return "chi_sim+eng"
    if "eng" in avail:
        return "eng"
    return next(iter(avail), "eng")


def detect_orientation(image, max_side=760, accept_score=0.6):
    """检测图像需顺时针旋转多少度(0/90/180/270)才能正立。

    tesseract OSD 对中英混排与细长条带不可靠, 改用四方向置信度择优:
    对每个方向各识别一次, 选平均置信度最高者。

    两个提速手段: 采样图缩到 max_side 边长(方向判别不需要原图分辨率),
    以及按宽高比排序候选方向后、首个方向置信度达标即收敛。
    """
    import pytesseract
    from PIL import Image

    pil = image if isinstance(image, Image.Image) else Image.fromarray(image)
    m = max(pil.size)
    if m > max_side:
        pil = pil.resize((int(pil.width * max_side / m), int(pil.height * max_side / m)), Image.LANCZOS)

    det_lang = _det_lang()
    landscape = pil.width >= pil.height
    order = (90, 270, 0, 180) if landscape else (0, 180, 90, 270)

    best_angle, best_score = 0, -1.0
    for angle in order:
        rot = pil if angle == 0 else pil.rotate(-angle, expand=True)
        try:
            data = pytesseract.image_to_data(
                rot, lang=det_lang, config="--psm 6", output_type=pytesseract.Output.DICT
            )
        except Exception:
            continue
        confs = []
        for c in data.get("conf", []):
            try:
                v = float(c)
            except (TypeError, ValueError):
                continue
            if v >= 0:
                confs.append(v)
        score = (sum(confs) / len(confs)) if confs else 0.0
        if score > best_score:
            best_score, best_angle = score, angle
        if score >= accept_score:
            break
    return best_angle if best_score > 0 else 0


def _orient_page(image, angle, pw, ph):
    """按顺时针 angle 旋转图像正立, 返回 (旋转图, 新逻辑宽, 新逻辑高)。

    90/270 时宽高互换, 使后续坐标映射仍指向正立后的版面。
    """
    if not angle or angle % 360 == 0:
        return image, pw, ph
    from PIL import Image

    pil = image if isinstance(image, Image.Image) else Image.fromarray(image)
    rot = pil.rotate(-angle, expand=True)
    if angle % 180 == 0:
        return rot, pw, ph
    return rot, ph, pw


# ── PDF 文本层 ───────────────────────────────────────────


def _span_words(span, pno):
    """Split a PDF span into word-level boxes from its character bboxes."""
    words = []
    size = float(span.get("size") or 11)
    buf = []

    def flush():
        items = list(buf)
        del buf[:]
        if not items:
            return
        text = "".join(c[0] for c in items).strip()
        if not text:
            return
        x0 = min(c[1][0] for c in items)
        y0 = min(c[1][1] for c in items)
        x1 = max(c[1][2] for c in items)
        y1 = max(c[1][3] for c in items)
        words.append(
            Word(
                text=text,
                x=int(x0),
                y=int(y0),
                w=max(int(x1 - x0), 1),
                h=max(int(y1 - y0), 1),
                conf=1.0,
                page=pno,
                size=size,
            )
        )

    for ch in span.get("chars") or []:
        glyph = ch.get("c", "")
        bbox = ch.get("bbox")
        if not glyph or bbox is None:
            continue
        if glyph.isspace():
            flush()
            continue
        if buf:
            prev = buf[-1]
            gap = bbox[0] - prev[1][2]
            prev_w = max(prev[1][2] - prev[1][0], 0.5)
            split = gap > 0.45 * prev_w
            if not split and _is_cjk(glyph):
                split = sum(len(c[0]) for c in buf) >= CJK_TOKEN_CAP
            if split:
                flush()
        buf.append((glyph, bbox))
    flush()
    return words


def pdf_page_count(pdf_path):
    import fitz

    doc = fitz.open(pdf_path)
    try:
        return doc.page_count
    finally:
        doc.close()


def pdf_page_sizes(pdf_path):
    import fitz

    doc = fitz.open(pdf_path)
    try:
        return [(float(p.rect.width), float(p.rect.height)) for p in doc]
    finally:
        doc.close()


def pdf_text_words(pdf_path):
    """Extract word boxes from a PDF text layer with real geometry and font size."""
    import fitz

    doc = fitz.open(pdf_path)
    words = []
    try:
        for pno, page in enumerate(doc):
            d = page.get_text("rawdict")
            for block in d.get("blocks", []):
                if block.get("type") != 0:
                    continue
                for line in block.get("lines", []):
                    for span in line.get("spans", []):
                        words.extend(_span_words(span, pno))
    finally:
        doc.close()
    return words


def pdf_page_to_image(pdf_path, pno, dpi=150):
    import io
    import fitz
    from PIL import Image

    doc = fitz.open(pdf_path)
    try:
        page = doc[pno]
        pix = page.get_pixmap(matrix=fitz.Matrix(dpi / 72.0, dpi / 72.0))
        return Image.open(io.BytesIO(pix.tobytes("png"))).convert("RGB")
    finally:
        doc.close()


def pdf_page_native_image(pdf_path, pno, max_side=6000):
    """纯图像 PDF 页: 按原生分辨率抽取嵌入图并合成, 绕过 get_pixmap 重采样。

    get_pixmap 对 JPEG 条带做重采样会劣化小字(如 e→c); 直接抽取嵌入图按其
    在页面上的位置以原生像素合成, 保留源图细节。页面含文本层或矢量绘图时
    返回 None, 由调用方退回渲染。
    """
    import io
    import fitz
    from PIL import Image

    doc = fitz.open(pdf_path)
    try:
        page = doc[pno]
        if page.get_text().strip() or page.get_drawings():
            return None
        info = page.get_image_info(xrefs=True)
        if not info:
            return None
        # 各图的原生像素/点比例。PDF 常以非等比方式摆放扫描条带(x 比例略大于 y),
        # 若按目标尺寸 resize 会重采样 JPEG 条带并劣化小字(e→c), 故按原生像素直贴。
        ratios = []
        for it in info:
            bb = it.get("bbox") or (0, 0, 0, 0)
            bw = max(bb[2] - bb[0], 1e-6)
            bh = max(bb[3] - bb[1], 1e-6)
            if not it.get("width") or not it.get("height"):
                return None
            ratios.append((bb, it["width"] / bw, it["height"] / bh))
        # 比例明显不一致时退回渲染, 避免同页出现不同尺度的混贴
        xs = [r[1] for r in ratios]
        ys = [r[2] for r in ratios]
        if len(ratios) > 1:
            if max(xs) / min(xs) > 1.10 or max(ys) / min(ys) > 1.10:
                return None
        sx, sy = sum(xs) / len(xs), sum(ys) / len(ys)
        placements = []
        for bb, rx, ry in ratios:
            x0 = int(round(bb[0] * sx))
            y0 = int(round(bb[1] * sy))
            tw = max(int(round((bb[2] - bb[0]) * rx)), 1)
            th = max(int(round((bb[3] - bb[1]) * ry)), 1)
            placements.append((x0, y0, tw, th))
        cw = max(p[0] + p[2] for p in placements)
        ch = max(p[1] + p[3] for p in placements)
        if max(cw, ch) > max_side:
            return None
        canvas = Image.new("RGB", (cw, ch), (255, 255, 255))
        placed = False
        for it, (x0, y0, tw, th) in zip(info, placements):
            xref = it.get("xref", 0)
            if not xref:
                continue
            try:
                d = doc.extract_image(xref)
            except Exception:
                continue
            try:
                im = Image.open(io.BytesIO(d["image"])).convert("RGB")
            except Exception:
                continue
            if im.size != (tw, th):
                im = im.resize((tw, th), Image.LANCZOS)
            canvas.paste(im, (x0, y0))
            placed = True
        return canvas if placed else None
    finally:
        doc.close()


def pdf_pages_to_images(pdf_path, dpi=150):
    return [pdf_page_to_image(pdf_path, i, dpi) for i in range(pdf_page_count(pdf_path))]


# ── 版式分析 ─────────────────────────────────────────────


def cluster_lines(words):
    """Group words into Lines by vertical proximity."""
    if not words:
        return []
    ordered = sorted(words, key=lambda w: (w.y, w.x))
    med_h = _median(w.h for w in ordered) or 20
    tol = max(med_h * 0.55, 3)

    groups = []
    for w in ordered:
        for g in groups:
            cy = sum(i.cy for i in g) / len(g)
            if abs(w.cy - cy) <= tol:
                g.append(w)
                break
        else:
            groups.append([w])

    lines = []
    for g in groups:
        items = sorted(g, key=lambda i: i.x)
        heights = sorted(i.h for i in items)
        lines.append(
            Line(
                text=_join_words(items),
                x=min(i.x for i in items),
                y=min(i.y for i in items),
                w=max(i.x + i.w for i in items) - min(i.x for i in items),
                h=max(i.h for i in items),
                conf=sum(i.conf for i in items) / len(items),
                size=float(heights[len(heights) // 2]),
                page=items[0].page,
                words=items,
            )
        )
    return lines


def detect_columns(lines, page_w):
    """Return 2 when a clear vertical gap splits the layout, else 1."""
    if not page_w or page_w <= 0 or len(lines) < 4:
        return 1
    for ratio in (0.5, 0.48, 0.52, 0.45, 0.55):
        split = page_w * ratio
        left = sum(1 for ln in lines if ln.cx < split)
        right = len(lines) - left
        if left < 2 or right < 2 or min(left, right) < 0.3 * len(lines):
            continue
        if all(not (ln.x < split - page_w * 0.05 and ln.x + ln.w > split + page_w * 0.05)
               for ln in lines):
            return 2
    return 1


def order_lines(lines, page_w, ncols):
    """Reorder lines into reading order, handling two-column layouts."""
    if ncols < 2 or not page_w:
        return sorted(lines, key=lambda ln: (ln.y, ln.x))
    left = [ln for ln in lines if ln.cx < page_w / 2.0]
    right = [ln for ln in lines if ln.cx >= page_w / 2.0]
    return sorted(left, key=lambda ln: (ln.y, ln.x)) + sorted(right, key=lambda ln: (ln.y, ln.x))


def is_header_footer(ln, page_w, page_h):
    if not page_w or not page_h:
        return False
    if ln.y < page_h * 0.07 and ln.w <= page_w * 0.95:
        return True
    return ln.y + ln.h > page_h * 0.93 and ln.w <= page_w * 0.95


def is_title(ln, med_size):
    if not ln.size or not med_size:
        return False
    return ln.size >= med_size * 1.35 and len(ln.text) <= 240


def is_formula(ln):
    chars = [c for c in ln.text if not c.isspace()]
    if len(chars) < 2:
        return False
    ratio = sum(1 for c in chars if c in MATH_SYMBOLS) / len(chars)
    cjk = sum(1 for c in chars if _is_cjk(c))
    return ratio >= 0.12 and cjk / float(len(chars)) < 0.4


def _column_bounds(rows, tol):
    spans = sorted((it.x, it.x + it.w) for row in rows for it in row)
    merged = []
    for s, e in spans:
        if merged and s <= merged[-1][1] + tol:
            merged[-1] = (merged[-1][0], max(merged[-1][1], e))
        else:
            merged.append((s, e))
    return [(a, b) for a, b in merged if (b - a) >= tol]


def _column_index(cx, bounds):
    for i, (a, b) in enumerate(bounds):
        if a - 2 <= cx <= b + 2:
            return i
    return 0


def _table_candidate_rows(words, gap_thr):
    """Group words into rows, keeping rows whose words are spread by clear gaps."""
    rows = []
    for w in sorted(words, key=lambda i: (i.y, i.x)):
        for r in rows:
            rh = max(r[0].h, 1)
            cy = sum(i.cy for i in r) / len(r)
            if abs(w.cy - cy) <= max(rh * 0.6, 4):
                r.append(w)
                break
        else:
            rows.append([w])
    out = []
    for r in rows:
        items = sorted(r, key=lambda i: i.x)
        if len(items) < 2:
            continue
        min_gap = min(items[i + 1].x - (items[i].x + items[i].w) for i in range(len(items) - 1))
        if min_gap >= gap_thr:
            out.append(items)
    return out


def _row_cells(row, bounds):
    cols = {i: [] for i in range(len(bounds))}
    for it in sorted(row, key=lambda i: i.x):
        cols[_column_index(it.cx, bounds)].append(it)
    return [_join_words(cols[i]) for i in range(len(bounds))]


def _build_table_cells(grp, med_h):
    """Turn grouped rows into a cell matrix, pruning rows that break the grid.

    A real table repeats one column structure. Columns that are empty in more
    than half the rows are artifacts of a wide stray line, so they are dropped
    before the remaining rows are checked. Rows whose non-empty cell count
    deviates from the group median are removed one by one and the column bounds
    are recomputed, because a wide stray line shifts every boundary. Rows that
    start with a list marker are enumerator lines rather than table rows.
    Returns (cells, kept_rows) or None when nothing looks tabular.
    """
    rows = [r for r in grp if not LIST_PREFIX.match(" ".join(it.text for it in r))]
    if len(rows) < 3:
        return None
    for _ in range(len(rows)):
        bounds = _column_bounds(rows, max(med_h * 0.5, 3))
        if len(bounds) < 2:
            return None
        matrix = [_row_cells(r, bounds) for r in rows]
        keep = [i for i in range(len(bounds))
                if sum(1 for r in matrix if r[i].strip()) / float(len(matrix)) >= 0.5]
        if len(keep) < 2:
            return None
        matrix = [[r[i] for i in keep] for r in matrix]
        ncol = len(keep)
        counts = [sum(1 for v in r if v.strip()) for r in matrix]
        if (max(counts) - min(counts)) <= 1 and min(counts) >= 2 \
                and max(counts) / float(ncol) >= 0.5:
            return matrix, rows
        med = sorted(counts)[len(counts) // 2]
        worst = max(range(len(rows)), key=lambda i: (abs(counts[i] - med), -rows[i][0].y))
        rows.pop(worst)
        if len(rows) < 3:
            return None
    return None


def detect_tables(words, lines, page_h=None):
    """Cluster words into grid tables.

    Returns (tables, consumed) where consumed is a set of id(Line) that already
    belong to a table and must be skipped by the block grouper.
    """
    consumed = set()
    tables = []
    if len(words) < 4:
        return tables, consumed

    med_h = _median(w.h for w in words) or 12
    gap_thr = max(med_h * 0.6, 4)
    cand_rows = _table_candidate_rows(words, gap_thr)
    if len(cand_rows) < 2:
        return tables, consumed

    groups = []
    for row in sorted(cand_rows, key=lambda r: (r[0].y, r[0].x)):
        rh = max(row[0].h, 1)
        if groups and row[0].y - groups[-1][-1][0].y <= rh * 3.2:
            groups[-1].append(row)
        else:
            groups.append([row])

    for grp in groups:
        built = _build_table_cells(grp, med_h)
        if not built:
            continue
        cells, kept = built

        xs = [it.x for row in kept for it in row]
        ys = [it.y for row in kept for it in row]
        xe = [it.x + it.w for row in kept for it in row]
        ye = [it.y + it.h for row in kept for it in row]
        bbox = (min(xs), min(ys), max(xe), max(ye))
        conf = sum(it.conf for row in kept for it in row) / float(sum(len(r) for r in kept))
        page = kept[0][0].page
        tables.append(
            Block(
                type="table",
                text="\n".join(" | ".join(r) for r in cells),
                page=page,
                bbox=bbox,
                conf=conf,
                cells=cells,
            )
        )
        pad = max(med_h * 0.4, 2)
        for ln in lines:
            if ln.page != page:
                continue
            if ln.x >= bbox[0] - pad and ln.x + ln.w <= bbox[2] + pad \
               and ln.y >= bbox[1] - pad and ln.y + ln.h <= bbox[3] + pad:
                consumed.add(id(ln))
    return tables, consumed


def group_blocks(lines, page_w, page_h, optimize, header_cache):
    """Turn lines into typed Blocks, dropping explicit page-number footers."""
    med_size = _median(ln.size for ln in lines)
    blocks = []
    cur = None

    def flush():
        nonlocal cur
        if cur and cur["lines"]:
            items = cur["lines"]
            blocks.append(
                Block(
                    type=cur["type"],
                    text="\n".join(ln.text for ln in items).strip(),
                    page=items[0].page,
                    bbox=(
                        min(ln.x for ln in items),
                        min(ln.y for ln in items),
                        max(ln.x + ln.w for ln in items),
                        max(ln.y + ln.h for ln in items),
                    ),
                    lines=list(items),
                    conf=sum(ln.conf for ln in items) / float(len(items)),
                )
            )
        cur = None

    for ln in lines:
        text = ln.text.strip()
        if not text:
            continue
        if optimize and HEADER_TITLE_WORDS.search(text):
            header_cache.append(text)
            continue

        if is_title(ln, med_size):
            btype = "title"
        elif is_formula(ln):
            btype = "formula"
        elif LIST_PREFIX.match(text):
            btype = "list"
        else:
            btype = "paragraph"

        if cur is None or cur["type"] != btype:
            flush()
            cur = {"type": btype, "lines": [ln]}
        else:
            cur["lines"].append(ln)
    flush()
    return blocks


def drop_running_headers(blocks, lines, page_dims):
    """Remove margin lines whose text repeats in the same margin on other pages.

    A margin line that appears once is treated as the document title and kept.
    Returns the surviving blocks plus the removed header texts in reading order.
    """
    hits = {}
    for ln in lines:
        pw, ph = page_dims.get(ln.page, (0.0, 0.0))
        if not is_header_footer(ln, pw, ph):
            continue
        key = ("top" if ln.cy < ph / 2.0 else "bottom", ln.text.strip())
        hits.setdefault(key, set()).add(ln.page)
    running = {k[1] for k, v in hits.items() if len(v) >= 2 and k[1]}
    if not running:
        return blocks, []

    def is_running(ln):
        pw, ph = page_dims.get(ln.page, (0.0, 0.0))
        return is_header_footer(ln, pw, ph) and ln.text.strip() in running

    removed = []
    kept = []
    for blk in blocks:
        src = blk.lines or []
        if not src:
            kept.append(blk)
            continue
        keep = [ln for ln in src if not is_running(ln)]
        for ln in src:
            if is_running(ln) and ln.text.strip() not in removed:
                removed.append(ln.text.strip())
        if not keep:
            continue
        if len(keep) == len(src):
            kept.append(blk)
            continue
        blk.lines = keep
        blk.text = "\n".join(ln.text for ln in keep).strip()
        blk.bbox = (
            min(ln.x for ln in keep),
            min(ln.y for ln in keep),
            max(ln.x + ln.w for ln in keep),
            max(ln.y + ln.h for ln in keep),
        )
        blk.conf = sum(ln.conf for ln in keep) / float(len(keep))
        if blk.text:
            kept.append(blk)
    return kept, removed


def _bbox_overlap(a, b):
    x0 = max(a[0], b[0])
    y0 = max(a[1], b[1])
    x1 = min(a[2], b[2])
    y1 = min(a[3], b[3])
    if x1 <= x0 or y1 <= y0:
        return 0.0
    inter = (x1 - x0) * (y1 - y0)
    area = max((a[2] - a[0]) * (a[3] - a[1]), 1.0)
    return inter / area


def apply_neural_tables(image, lines, page, pw, ph):
    """PP-DocLayout 定位表格区, SLANeXt 识别结构, 返回 (table_blocks, consumed_line_ids)。

    image 是像素坐标系的 PIL 图; lines 是页面坐标系。失败或无表格时返回空, 调用方走启发式。
    """
    if image is None:
        return [], set()
    iw, ih = image.size if hasattr(image, "size") else (0, 0)
    if min(iw, ih) < 200 or max(iw, ih) < 600:
        return [], set()
    try:
        from layout_structure import detect_table_regions, recognize_table
    except Exception as exc:
        _log(f"[OCR] 版面模块不可用: {exc}")
        return [], set()
    try:
        regions = detect_table_regions(image)
    except Exception as exc:
        _log(f"[OCR] 版面检测失败: {exc}")
        return [], set()
    if not regions:
        return [], set()
    iw, ih = image.size if hasattr(image, "size") else (pw, ph)
    sx = max(float(iw), 1.0) / max(float(pw), 1.0)
    sy = max(float(ih), 1.0) / max(float(ph), 1.0)
    tables = []
    consumed = set()
    for bbox_px, score in regions:
        cells = recognize_table(image, bbox_px)
        if not cells:
            continue
        x0 = bbox_px[0] / sx
        y0 = bbox_px[1] / sy
        x1 = bbox_px[2] / sx
        y1 = bbox_px[3] / sy
        bbox = (x0, y0, x1, y1)
        tables.append(
            Block(
                type="table",
                text="\n".join(" | ".join(r) for r in cells),
                page=page,
                bbox=bbox,
                conf=float(score),
                cells=cells,
            )
        )
        pad = max((y1 - y0) * 0.08, 4)
        tb = (bbox[0] - pad, bbox[1] - pad, bbox[2] + pad, bbox[3] + pad)
        table_chars = {c.lower() for c in tables[-1].text if c.isalnum()}
        y_lo = bbox[1] - max((y1 - y0) * 0.15, 8)
        y_hi = bbox[3] + max((y1 - y0) * 0.15, 8)
        for ln in lines:
            if ln.page != page:
                continue
            lb = (ln.x, ln.y, ln.x + ln.w, ln.y + ln.h)
            if _bbox_overlap(lb, tb) >= 0.25:
                consumed.add(id(ln))
                continue
            cy = ln.y + ln.h * 0.5
            if y_lo <= cy <= y_hi:
                ln_chars = {c.lower() for c in ln.text if c.isalnum()}
                if ln_chars and len(ln_chars & table_chars) / len(ln_chars) >= 0.55:
                    consumed.add(id(ln))
    return tables, consumed


def analyze_page(words, page_w, page_h, optimize, header_cache, image=None, neural_tables=False):
    """Full layout analysis for one page: tables + typed blocks + reading order.

    neural_tables=True 时才调用 PP-DocLayout + SLANeXt；模型体积大、单页推理
    可达数分钟，因此默认关闭，仅高级模式开启。
    """
    page_w = max(float(page_w or 1), 1.0)
    page_h = max(float(page_h or 1), 1.0)
    lines = cluster_lines(words)
    ncols = detect_columns(lines, page_w)
    lines = order_lines(lines, page_w, ncols)
    tables, consumed = detect_tables(words, lines, page_h)
    page = words[0].page if words else 0
    if neural_tables:
        neural, ncons = apply_neural_tables(image, lines, page, page_w, page_h)
        if neural:
            tables = neural
            consumed |= ncons
    blocks = group_blocks([ln for ln in lines if id(ln) not in consumed],
                          page_w, page_h, optimize, header_cache)
    blocks.extend(tables)
    blocks.sort(key=lambda b: (b.bbox[1], b.bbox[0]))
    return blocks, lines, ncols


# ── 导出 ─────────────────────────────────────────────────


def write_txt(path, text):
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)


def write_json(path, payload):
    with open(path, "w", encoding="utf-8") as f:
        json.dump(payload, f, ensure_ascii=False, indent=2)


def write_xlsx(path, blocks):
    """Sheet 1 lists every block; one extra sheet per detected table."""
    from openpyxl import Workbook
    from openpyxl.styles import Alignment, Font
    from openpyxl.utils import get_column_letter

    wb = Workbook()
    ws = wb.active
    ws.title = "识别结果"
    ws.append(["类型", "页码", "内容"])
    for col in (1, 2, 3):
        ws.cell(row=1, column=col).font = Font(bold=True)
    for blk in blocks:
        ws.append([blk.type, blk.page + 1, blk.text])
    ws.column_dimensions["A"].width = 12
    ws.column_dimensions["B"].width = 8
    ws.column_dimensions["C"].width = 100
    for row in ws.iter_rows(min_row=2, min_col=3, max_col=3):
        row[0].alignment = Alignment(wrap_text=True, vertical="top")

    for idx, blk in enumerate(blocks, 1):
        if blk.type != "table" or not blk.cells:
            continue
        ncol = max(len(r) for r in blk.cells)
        tw = wb.create_sheet(f"表格{idx}")
        for r, row in enumerate(blk.cells):
            for c in range(ncol):
                cell = tw.cell(row=r + 1, column=c + 1, value=row[c] if c < len(row) else "")
                cell.alignment = Alignment(wrap_text=True, vertical="top")
                if r == 0:
                    cell.font = Font(bold=True)
        for c in range(1, ncol + 1):
            width = 10
            for row in blk.cells:
                if c - 1 < len(row):
                    width = max(width, min(len(str(row[c - 1])) + 2, 50))
            tw.column_dimensions[get_column_letter(c)].width = width
    wb.save(path)


def write_docx(path, blocks, page_sizes=None, point_units=False):
    """Layout-aware DOCX: headings, lists, formulas, tables and line indentation."""
    from docx import Document
    from docx.enum.text import WD_ALIGN_PARAGRAPH
    from docx.shared import Emu, Pt

    doc = Document()
    for blk in blocks:
        if blk.type == "table" and blk.cells:
            ncol = max(len(r) for r in blk.cells)
            table = doc.add_table(rows=len(blk.cells), cols=ncol)
            table.style = "Table Grid"
            for r, row in enumerate(blk.cells):
                for c in range(ncol):
                    cell = table.cell(r, c)
                    cell.text = row[c] if c < len(row) else ""
                    if r == 0:
                        for p in cell.paragraphs:
                            for run in p.runs:
                                run.bold = True
            continue

        lines = blk.lines or []
        if not lines:
            continue
        pw = page_sizes[blk.page][0] if page_sizes and blk.page < len(page_sizes) else None
        for i, ln in enumerate(lines):
            p = doc.add_paragraph()
            p.paragraph_format.space_before = Pt(0 if i == 0 else 2)
            if pw:
                ratio = max(0.0, min(ln.x / float(pw), 0.8))
                p.paragraph_format.left_indent = Emu(int(ratio * 6100000))
            run = p.add_run(ln.text)
            if blk.type == "title":
                run.bold = True
                size = ln.size if point_units else ln.size * 0.8
                run.font.size = Pt(max(10, min(int(size), 26)))
                if len(lines) == 1:
                    p.alignment = WD_ALIGN_PARAGRAPH.CENTER
            elif blk.type == "list":
                run.font.size = Pt(11)
            elif blk.type == "formula":
                run.font.name = "Cambria Math"
    doc.save(path)


def write_redbox(path, page_images, page_scales, lines, pages):
    """Draw red boxes around recognized lines and stack the pages vertically."""
    from PIL import Image, ImageDraw

    rendered = []
    for i, img in enumerate(page_images):
        scale = page_scales[i] if i < len(page_scales) else 1.0
        draw = ImageDraw.Draw(img)
        stroke = max(2, img.width // 500)
        for ln in lines:
            if ln.page != i:
                continue
            pad = max(1.0, ln.h * scale * 0.15)
            draw.rectangle(
                [ln.x * scale - pad, ln.y * scale - pad,
                 (ln.x + ln.w) * scale + pad, (ln.y + ln.h) * scale + pad],
                outline=(220, 0, 0), width=stroke,
            )
        rendered.append(img)

    gap = 30 if len(rendered) > 1 else 0
    width = max(im.width for im in rendered) + (gap * 2 if rendered else 0)
    height = sum(im.height + gap for im in rendered)
    canvas = Image.new("RGB", (max(width, 1), max(height, 1)), (245, 245, 245))
    draw = ImageDraw.Draw(canvas)
    try:
        label_font = ImageFont_truetype_safe(18)
    except Exception:
        label_font = None

    y = 0
    for i, img in enumerate(rendered):
        canvas.paste(img, (gap, y + (22 if label_font else 0)))
        if label_font:
            draw.text((gap, y), f"Page {i + 1}/{pages}", fill=(90, 90, 90), font=label_font)
        y += img.height + gap
    canvas.save(path)


def ImageFont_truetype_safe(size):
    from PIL import ImageFont

    for name in ("/usr/share/fonts/wqy-zenhei/wqy-zenhei.ttc",
                 "/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc"):
        if os.path.exists(name):
            return ImageFont.truetype(name, size)
    try:
        return ImageFont.load_default(size=size)
    except TypeError:
        return ImageFont.load_default()


# ── 翻译 ─────────────────────────────────────────────────


def translate_to_zh(text):
    """Translate to Simplified Chinese through the shared translate.py CLI."""
    import subprocess
    import tempfile

    script = os.path.join(os.path.dirname(os.path.abspath(__file__)), "translate.py")
    if not os.path.exists(script):
        _log("[OCR] 未找到 translate.py，跳过翻译")
        return ""
    payload = tempfile.NamedTemporaryFile(suffix=".json", delete=False, mode="w", encoding="utf-8")
    try:
        json.dump({"text": text[:8000]}, payload, ensure_ascii=False)
        payload.close()
        out = subprocess.run(
            [sys.executable, script, "text", "auto", "zh", payload.name],
            capture_output=True, text=True, timeout=180,
        )
        if out.returncode != 0:
            _log(f"[OCR] 翻译失败: {out.stderr.strip()[:200]}")
            return ""
        return (json.loads(out.stdout.strip().splitlines()[-1]).get("text") or "").strip()
    except Exception as exc:
        _log(f"[OCR] 翻译异常: {exc}")
        return ""
    finally:
        try:
            os.remove(payload.name)
        except OSError:
            pass


# ── 主流程 ───────────────────────────────────────────────


def run(src, opts, outdir):
    started = time.time()
    warnings = []

    mode = opts.get("mode") or "general"
    quality = opts.get("quality") or "normal"
    charset = opts.get("charset") or "auto"
    redbox = bool(opts.get("redbox"))
    optimize = bool(opts.get("optimize"))
    do_translate = bool(opts.get("translate"))
    formats = opts.get("formats") or ["txt"]
    engine_prefer = opts.get("engine") or "auto"
    lang = opts.get("lang") or "auto"
    if lang not in LANG_PRESETS:
        lang = "auto"
    if mode not in ("general", "advanced"):
        mode = "general"
    if quality not in QUALITY_PROFILE:
        quality = "normal"

    ext = os.path.splitext(src)[1].lower()
    converter = CharsetConverter(charset)
    header_cache = []
    all_blocks = []
    all_lines = []
    page_sizes = []
    page_dims = {}
    page_images = []
    page_scales = []
    engine_name = ""
    from_text_layer = False
    layout_columns = 1
    pages = 1
    profile = QUALITY_PROFILE[quality]

    if ext == ".pdf":
        page_sizes = pdf_page_sizes(src)
        pages = len(page_sizes) or 1
        if not page_sizes:
            raise ValueError("PDF 无法解析")
        page_dims = {i: (float(w), float(h)) for i, (w, h) in enumerate(page_sizes)}

        words = pdf_text_words(src)
        tl_text = " ".join(w.text for w in words)
        garbled = text_layer_is_garbled(tl_text)
        has_text = any(w.text.strip() for w in words)

        # 探针: 启发式判定文本层可用时, 再用第 1 页图像 OCR 做一次交叉验证。
        # 文本层是错码/私用区乱码而启发式漏判时, 与图像 OCR 结果几乎不相交,
        # 据此回退到整篇图像 OCR, 兜住所有乱码形态。
        probe_engine = None
        if has_text and not garbled:
            try:
                probe_engine = get_engine(mode, quality, engine_prefer, lang)
                probe_img = pdf_page_to_image(src, 0, 300)
                if probe_img is not None:
                    probe_words = probe_engine.image_words(probe_img)
                    probe_text = " ".join(w.text for w in probe_words if w.text.strip())
                    if text_layer_mismatch(tl_text, probe_text):
                        garbled = True
                        warnings.append(
                            "PDF 文本层与图像识别结果不一致，疑似乱码，已改用图像 OCR"
                        )
            except Exception as exc:
                _log(f"[OCR] 文本层探针失败: {exc}")

        if not garbled and has_text:
            from_text_layer = True
            engine_name = "pdftext"
            for pno in range(pages):
                pw, ph = page_sizes[pno]
                page_words = [w for w in words if w.page == pno]
                blocks, lines, ncols = analyze_page(
                    page_words, pw, ph, optimize, header_cache)
                layout_columns = max(layout_columns, ncols)
                all_blocks.extend(blocks)
                all_lines.extend(lines)
            if redbox:
                for pno in range(min(pages, REDBOX_MAX_PAGES)):
                    img = pdf_page_to_image(src, pno, min(profile["dpi"], 150))
                    pw = max(page_sizes[pno][0], 1.0)
                    page_images.append(img)
                    page_scales.append(img.width / pw)
                if pages > REDBOX_MAX_PAGES:
                    warnings.append(f"红框标记图仅渲染前 {REDBOX_MAX_PAGES} 页")
        else:
            if garbled and not any("已改用图像 OCR" in w for w in warnings):
                warnings.append("PDF 文本层是乱码（子集字体缺少字符映射），已改用图像 OCR")
            engine = probe_engine or get_engine(mode, quality, engine_prefer, lang)
            engine_name = engine.name
            # 文本层乱码时页面本身是清晰可印的，用高 DPI 渲染让 OCR 拿到更多细节
            render_dpi = 300 if garbled else profile["dpi"]
            page_imgs = []
            for pno in range(pages):
                img = pdf_page_native_image(src, pno)
                if img is None:
                    img = pdf_page_to_image(src, pno, render_dpi)
                page_imgs.append(img)
            orient = detect_orientation(page_imgs[0]) if page_imgs else 0
            if orient:
                warnings.append(f"检测到页面旋转 {orient}°，已自动正立")
            for pno, img in enumerate(page_imgs):
                base = page_sizes[pno] if pno < len(page_sizes) else (img.width, img.height)
                oimg, pw, ph = _orient_page(img, orient, float(base[0]), float(base[1]))
                sx = max(float(pw), 1.0) / max(oimg.width, 1)
                sy = max(float(ph), 1.0) / max(oimg.height, 1)
                words_p = engine.image_words(oimg)
                for w in words_p:
                    w.page = pno
                    w.x = int(w.x * sx)
                    w.y = int(w.y * sy)
                    w.w = max(int(w.w * sx), 1)
                    w.h = max(int(w.h * sy), 1)
                blocks, lines, ncols = analyze_page(
                    words_p, pw, ph, optimize, header_cache, oimg,
                    neural_tables=(mode == "advanced"))
                layout_columns = max(layout_columns, ncols)
                all_blocks.extend(blocks)
                all_lines.extend(lines)
                if redbox and pno < REDBOX_MAX_PAGES:
                    page_images.append(oimg)
                    page_scales.append(max(oimg.width, 1) / max(pw, 1.0))
            if pages > REDBOX_MAX_PAGES:
                warnings.append(f"红框标记图仅渲染前 {REDBOX_MAX_PAGES} 页")

    elif ext in IMAGE_EXTS:
        from PIL import Image

        img = Image.open(src).convert("RGB")
        pages = 1
        engine = get_engine(mode, quality, engine_prefer, lang)
        engine_name = engine.name
        orient = detect_orientation(img)
        if orient:
            warnings.append(f"检测到图像旋转 {orient}°，已自动正立")
        oimg, pw, ph = _orient_page(img, orient, float(img.width), float(img.height))
        page_dims = {0: (pw, ph)}
        words = engine.image_words(oimg)
        for w in words:
            w.page = 0
        blocks, lines, ncols = analyze_page(
            words, pw, ph, optimize, header_cache, oimg,
            neural_tables=(mode == "advanced"))
        layout_columns = ncols
        all_blocks.extend(blocks)
        all_lines.extend(lines)
        if redbox:
            page_images.append(oimg)
            page_scales.append(1.0)
    else:
        raise ValueError(f"不支持的文件格式: {ext}")

    if optimize and len(page_dims) > 1:
        all_blocks, running = drop_running_headers(all_blocks, all_lines, page_dims)
        header_cache.extend(running)

    raw_text = "\n\n".join(b.text for b in all_blocks if b.text.strip())
    text = converter.convert(raw_text) if raw_text else ""
    detected = charset if charset != "auto" else detect_charset(raw_text)

    translated = ""
    if do_translate and text:
        translated = translate_to_zh(text)
        if not translated:
            warnings.append("翻译服务暂不可用，已返回原文")

    table_count = sum(1 for b in all_blocks if b.type == "table")
    files = {}
    payload = {
        "engine": engine_name,
        "pages": pages,
        "chars": len(text),
        "charset": detected,
        "from_text_layer": from_text_layer,
        "layout": {
            "columns": layout_columns,
            "tables": table_count,
            "blocks": len(all_blocks),
            "lines": len(all_lines),
        },
        "filtered_headers": header_cache,
        "options": {
            "mode": mode, "quality": quality, "charset": charset, "lang": lang,
            "redbox": redbox, "optimize": optimize, "translate": do_translate,
            "formats": formats,
        },
        "text": text,
        "translated_text": translated,
        "blocks": [b.to_dict() for b in all_blocks],
        "lines": [ln.to_dict() for ln in all_lines],
    }

    if "txt" in formats:
        p = os.path.join(outdir, "result.txt")
        write_txt(p, translated or text)
        files["txt"] = p
    if "json" in formats:
        p = os.path.join(outdir, "result.json")
        write_json(p, payload)
        files["json"] = p
    if "xlsx" in formats:
        p = os.path.join(outdir, "result.xlsx")
        write_xlsx(p, all_blocks)
        files["xlsx"] = p
    if "docx" in formats:
        p = os.path.join(outdir, "result.docx")
        write_docx(p, all_blocks, page_sizes or None, point_units=from_text_layer)
        files["docx"] = p
    if redbox and page_images:
        p = os.path.join(outdir, "redbox.png")
        write_redbox(p, page_images, page_scales, all_lines, pages)
        files["redbox"] = p

    return {
        "success": True,
        "text": text,
        "translated_text": translated,
        "engine": engine_name,
        "pages": pages,
        "chars": len(text),
        "charset": detected,
        "from_text_layer": from_text_layer,
        "layout": payload["layout"],
        "filtered_headers": header_cache,
        "blocks": payload["blocks"],
        "lines": payload["lines"],
        "files": {k: os.path.abspath(v) for k, v in files.items()},
        "warnings": warnings,
        "elapsed_ms": int((time.time() - started) * 1000),
        "options": payload["options"],
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("path")
    parser.add_argument("--options", default="{}")
    parser.add_argument("--outdir", default=".")
    args = parser.parse_args()

    if not os.path.exists(args.path):
        print(json.dumps({"success": False, "error": "文件不存在"}, ensure_ascii=False))
        return 1
    try:
        opts = json.loads(args.options or "{}")
        if not isinstance(opts, dict):
            raise ValueError("options 必须是 JSON 对象")
    except (json.JSONDecodeError, ValueError):
        print(json.dumps({"success": False, "error": "参数格式错误"}, ensure_ascii=False))
        return 1

    os.makedirs(args.outdir, exist_ok=True)
    try:
        result = run(args.path, opts, args.outdir)
    except Exception as exc:
        _log(f"[OCR] 识别失败: {exc}")
        print(json.dumps({"success": False, "error": str(exc)}, ensure_ascii=False))
        return 1

    print(json.dumps(result, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
