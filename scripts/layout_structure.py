"""版面分区与表格结构识别（Umi-OCR 思路：PP-DocLayout + SLANeXt）。

在既有启发式版面分析之上叠加一层神经版面检测：
- PP-DocLayout-M 定位页面区域（文本/标题/表格/公式/图等）；
- 表格区域裁剪后交给 TableRecognitionPipelineV2（SLANeXt 结构 + 单元格 OCR），
  输出 HTML，再解析成 cell 矩阵供 txt/xlsx/docx 复用。

两组模型体积大、初始化慢，全部按需懒加载；任一模型缺失或推理失败时返回空结果，
由调用方退回纯启发式版面分析，不影响主流程。
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

_LAYOUT_DET = None
_TABLE_PIPELINE = None
_TRIED_LAYOUT = False
_TRIED_TABLE = False

# 表格区域置信度阈值；低于此值不触发重型表格识别，避免误判普通段落。
TABLE_MIN_SCORE = 0.5


def _log(msg):
    print(msg, file=sys.stderr)


def _to_bgr(image):
    import cv2
    import numpy as np

    arr = np.asarray(image)
    if arr.ndim == 2:
        return cv2.cvtColor(arr, cv2.COLOR_GRAY2BGR)
    if arr.shape[2] == 3:
        return cv2.cvtColor(arr, cv2.COLOR_RGB2BGR)
    if arr.shape[2] == 4:
        return cv2.cvtColor(arr, cv2.COLOR_RGBA2BGR)
    return arr


def layout_detector():
    """懒加载 PP-DocLayout-M。禁 mkldnn 以避开 paddle 3.x 静态图 PIR/oneDNN 崩溃。"""
    global _LAYOUT_DET, _TRIED_LAYOUT
    if _LAYOUT_DET is not None:
        return _LAYOUT_DET
    if _TRIED_LAYOUT:
        return None
    _TRIED_LAYOUT = True
    try:
        from paddleocr import LayoutDetection

        _LAYOUT_DET = LayoutDetection(model_name="PP-DocLayout-M", enable_mkldnn=False)
        return _LAYOUT_DET
    except Exception as exc:
        _log(f"[OCR] 版面检测模型不可用: {exc}")
        return None


def table_pipeline():
    """懒加载表格识别管线。仅开结构+单元格识别，关闭文档方向/去形变/布局子模块。"""
    global _TABLE_PIPELINE, _TRIED_TABLE
    if _TABLE_PIPELINE is not None:
        return _TABLE_PIPELINE
    if _TRIED_TABLE:
        return None
    _TRIED_TABLE = True
    try:
        from paddleocr import TableRecognitionPipelineV2

        _TABLE_PIPELINE = TableRecognitionPipelineV2(
            use_layout_detection=False,
            use_doc_orientation_classify=False,
            use_doc_unwarping=False,
            enable_mkldnn=False,
        )
        return _TABLE_PIPELINE
    except Exception as exc:
        _log(f"[OCR] 表格识别管线不可用: {exc}")
        return None


def detect_table_regions(image):
    """返回 [(bbox_xyxy, score)]，仅含被判为表格的区域。

    任何异常（模型缺失/推理失败）都返回空列表，调用方据此退回启发式检测。
    bbox 为 (x0, y0, x1, y1)，与输入图像同像素坐标系。
    """
    det = layout_detector()
    if det is None:
        return []
    try:
        import numpy as np

        bgr = _to_bgr(image)
        res = det.predict(np.asarray(bgr))
    except Exception as exc:
        _log(f"[OCR] 版面检测推理失败: {exc}")
        return []
    out = []
    for r in res or []:
        try:
            j = r.json if hasattr(r, "json") else r
            j = j.get("res", j) if isinstance(j, dict) else j
            for b in (j.get("boxes") or []):
                if b.get("label") != "table":
                    continue
                score = float(b.get("score", 0.0))
                if score < TABLE_MIN_SCORE:
                    continue
                c = b.get("coordinate") or []
                if len(c) != 4:
                    continue
                x0, y0, x1, y1 = (int(round(v)) for v in c)
                if x1 > x0 and y1 > y0:
                    out.append(((x0, y0, x1, y1), score))
        except Exception:
            continue
    return out


def html_to_cells(html):
    """把 SLANeXt 输出的 HTML 表格解析成规整的二维 cell 矩阵。

    处理 colspan/rowspan 的占位展开：跨列补空串，跨行在后续行首补空串，
    保证每行列数一致，便于 write_xlsx/write_docx 直接消费。
    """
    if not html:
        return []
    from html.parser import HTMLParser

    class _P(HTMLParser):
        def __init__(self):
            super().__init__()
            self.rows = []
            self._row = None
            self._cell = None
            self._span = (1, 1)

        def handle_starttag(self, tag, attrs):
            a = dict(attrs)
            if tag == "tr":
                self._row = []
            elif tag in ("td", "th") and self._row is not None:
                cs = int(a.get("colspan", 1) or 1)
                rs = int(a.get("rowspan", 1) or 1)
                self._cell = []
                self._span = (max(cs, 1), max(rs, 1))

        def handle_data(self, data):
            if self._cell is not None:
                self._cell.append(data)

        def handle_endtag(self, tag):
            if tag in ("td", "th") and self._cell is not None:
                text = "".join(self._cell).strip()
                cs, rs = self._span
                self._row.append((text, cs, rs))
                self._cell = None
            elif tag == "tr" and self._row is not None:
                self.rows.append(self._row)
                self._row = None

    p = _P()
    try:
        p.feed(html)
    except Exception:
        pass

    # 展开 rowspan：先记录每列被上方行占用的行数
    ncol = 0
    for row in p.rows:
        ncol = max(ncol, sum(cs for _, cs, _ in row))
    if ncol == 0:
        return []
    grid = []
    carry = {}  # col -> 剩余被占行数
    for row in p.rows:
        cells = []
        col = 0
        it = iter(row)
        cur = next(it, None)
        while col < ncol:
            if carry.get(col, 0) > 0:
                cells.append("")
                carry[col] -= 1
                col += 1
                continue
            if cur is None:
                cells.append("")
                col += 1
                continue
            text, cs, rs = cur
            cells.append(text)
            for k in range(1, cs):
                cells.append("")
            if rs > 1:
                for k in range(cs):
                    carry[col + k] = rs - 1
            col += cs
            cur = next(it, None)
        grid.append(cells)
    # 清掉残留的 carry 影响，保证等宽
    width = max((len(r) for r in grid), default=0)
    return [r + [""] * (width - len(r)) for r in grid]


def recognize_table(image, bbox):
    """裁剪 bbox 区域并识别成 cell 矩阵；失败返回 None。

    为避免模型对过窄/过小区域误判，先向外扩少量边距。
    """
    pipe = table_pipeline()
    if pipe is None:
        return None
    import numpy as np

    arr = np.asarray(image)
    h, w = arr.shape[:2]
    x0, y0, x1, y1 = bbox
    pad = 8
    x0, y0 = max(0, x0 - pad), max(0, y0 - pad)
    x1, y1 = min(w, x1 + pad), min(h, y1 + pad)
    crop = arr[y0:y1, x0:x1]
    if crop.size == 0:
        return None
    try:
        res = pipe.predict(_to_bgr(crop))
    except Exception as exc:
        _log(f"[OCR] 表格识别失败: {exc}")
        return None
    for r in res or []:
        j = r.json if hasattr(r, "json") else r
        j = j.get("res", j) if isinstance(j, dict) else j
        for tr in (j.get("table_res_list") or []):
            cells = html_to_cells(tr.get("pred_html") or "")
            if cells and any(any(c.strip() for c in row) for row in cells):
                return cells
    return None


def available():
    """版面/表格能力是否可用（模型可加载）。"""
    return layout_detector() is not None
