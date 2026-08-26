#!/usr/bin/env python3
"""PDF 转 DOCX 工具 - 使用 pdfminer + python-docx"""
import sys
import argparse
from pdfminer.high_level import extract_text
from docx import Document
from docx.shared import Pt, Inches
from docx.enum.text import WD_ALIGN_PARAGRAPH


def pdf_to_docx(pdf_path: str, output_path: str) -> bool:
    text = extract_text(pdf_path)
    if not text.strip():
        return False

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
    return True


def main():
    parser = argparse.ArgumentParser(description="PDF to DOCX converter")
    parser.add_argument("input", help="Input PDF file path")
    parser.add_argument("output", help="Output DOCX file path")
    args = parser.parse_args()

    success = pdf_to_docx(args.input, args.output)
    sys.exit(0 if success else 1)


if __name__ == "__main__":
    main()
