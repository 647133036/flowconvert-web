#!/usr/bin/env python3
"""PDF 转 DOCX 工具 - 先尝试提取文本层，失败则对每页做 OCR（支持扫描版/照片式 PDF）"""
import sys
import argparse
from docx import Document
from docx.shared import Pt

from pdf_utils import clean_pdf_text
from pp_ocr_onnx import ocr_pdf as ocr_pdf_func


def ocr_pdf(pdf_path: str, lang: str = "chi_sim+eng") -> str:
    """扫描版 PDF 或文本层乱码的 PDF：每页渲染成图后 OCR"""
    return ocr_pdf_func(pdf_path, lang)


def text_to_docx(text: str, output_path: str) -> None:
    doc = Document()
    for line in text.splitlines():
        line = line.strip()
        if not line:
            doc.add_paragraph()
            continue
        p = doc.add_paragraph(line)
        for run in p.runs:
            run.font.size = Pt(11)
    doc.save(output_path)


def pdf_to_docx(pdf_path: str, output_path: str, lang: str = "chi_sim+eng") -> bool:
    # 1. 先提取文本层（空文本层或乱码文本层都视为不可用）
    text = clean_pdf_text(pdf_path, min_chars=1)
    # 2. 文本层不可用 -> 扫描版 / 照片式 PDF 或子集字体乱码，走 OCR
    if not text.strip():
        sys.stderr.write("文本层缺失或为乱码，启用 OCR 识别扫描版 PDF...\n")
        text = ocr_pdf(pdf_path, lang)
    if not text.strip():
        return False
    text_to_docx(text, output_path)
    return True


def main():
    parser = argparse.ArgumentParser(description="PDF to DOCX converter")
    parser.add_argument("input", help="Input PDF file path")
    parser.add_argument("output", help="Output DOCX file path")
    parser.add_argument("--lang", default="chi_sim+eng", help="OCR 语言，如 chi_sim+eng")
    args = parser.parse_args()

    success = pdf_to_docx(args.input, args.output, args.lang)
    sys.exit(0 if success else 1)


if __name__ == "__main__":
    main()
