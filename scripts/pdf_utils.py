#!/usr/bin/env python3
"""PDF 文本层提取与乱码判定，供 ocr / pdf2docx / pdf2xlsx / translate 共用。

PDF 子集字体缺少 ToUnicode 字符映射时，PDF 里"看起来有文字"，但抽出来的是
`22n2%S25SS iskoe aoe 计时ce` 这类乱码。这类文本层不能当正文使用，必须
回退到图像 OCR。所有入口统一走 clean_pdf_text，避免出现部分入口输出乱码。
"""
import re

LAT_RUN = re.compile(r"[A-Za-z]+")
CJK_RUN = re.compile(
    r"[\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff\u3040-\u30ff\uac00-\ud7af]+"
)

TEXT_LAYER_MIN_CHARS = 60
TEXT_LAYER_SCRIPT_SHARE = 0.05
TEXT_LAYER_LAT_RUN_MIN = 3.0
TEXT_LAYER_CJK_RUN_MIN = 2.5
TEXT_LAYER_SHORT_RUN_MAX = 0.30
TEXT_LAYER_PRIVATE_MAX = 0.01


def _run_profile(text, pattern, min_run, max_short_ratio):
    """该字母体系是否呈现乱码特征：孤立单字成串，或片段普遍极短（如"计时计时计时"）。"""
    runs = [len(m.group()) for m in pattern.finditer(text)]
    if not runs:
        return False
    mean_run = sum(runs) / len(runs)
    short_ratio = sum(1 for r in runs if r == 1) / len(runs)
    max_run = max(runs)
    # 正常文档的词组/句子会形成长片段；乱码常表现为片段普遍很短。
    # 两种乱码形态都判定：孤立单字为主，或所有片段都不超过 min_run。
    return (mean_run < min_run and short_ratio > max_short_ratio) or (
        mean_run < min_run and max_run <= min_run
    )


def text_layer_is_garbled(text):
    """判断 PDF 文本层是否为乱码，是则应回退到图像 OCR。

    判据（命中任一即判定为乱码）:
      1. 私用区字符 U+E000-U+F8FF 占比 > 1%（正常文档不使用私用区，短文本同样检测）
      2. 所有占比 >= 5% 的字母体系（拉丁 / 中日韩）都呈现乱码特征：
         孤立单字成串，或片段普遍极短（无完整词组/句子）
    """
    body = "".join(c for c in text if not c.isspace())
    n = len(body)

    private = sum(1 for c in body if 0xE000 <= ord(c) <= 0xF8FF)
    if private and private / n > TEXT_LAYER_PRIVATE_MAX:
        return True

    if n < TEXT_LAYER_MIN_CHARS:
        return False

    profiles = []
    lat_chars = sum(len(m.group()) for m in LAT_RUN.finditer(text))
    cjk_chars = sum(len(m.group()) for m in CJK_RUN.finditer(text))
    if lat_chars / n >= TEXT_LAYER_SCRIPT_SHARE:
        profiles.append(
            _run_profile(text, LAT_RUN, TEXT_LAYER_LAT_RUN_MIN, TEXT_LAYER_SHORT_RUN_MAX)
        )
    if cjk_chars / n >= TEXT_LAYER_SCRIPT_SHARE:
        profiles.append(
            _run_profile(text, CJK_RUN, TEXT_LAYER_CJK_RUN_MIN, TEXT_LAYER_SHORT_RUN_MAX)
        )
    return bool(profiles) and all(profiles)


def extract_text_layer(pdf_path, min_chars=0):
    """抽取 PDF 内嵌文本层，返回 (文本, 是否可用)。

    文本层为空、少于 min_chars 个字符、或为乱码时返回 ('', False)，
    调用方据此回退到图像 OCR。
    """
    try:
        import fitz
    except Exception:
        return "", False
    try:
        doc = fitz.open(pdf_path)
    except Exception:
        return "", False
    try:
        parts = [page.get_text() for page in doc]
    except Exception:
        parts = []
    finally:
        doc.close()

    text = "\n".join(parts)
    body = "".join(c for c in text if not c.isspace())
    if len(body) < min_chars:
        return "", False
    if text_layer_is_garbled(text):
        return "", False
    return text, True


def clean_pdf_text(pdf_path, min_chars=0):
    """返回可直接使用的 PDF 正文文本；文本层无效时为空串，由调用方决定是否 OCR。"""
    text, ok = extract_text_layer(pdf_path, min_chars)
    return text if ok else ""
