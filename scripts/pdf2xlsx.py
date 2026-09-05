#!/usr/bin/env python3
"""PDF 转 XLSX 工具 - 先抽文本层，不可用时降级 OCR"""
import sys
import argparse
from openpyxl import Workbook

from pdf_utils import clean_pdf_text
from pp_ocr_onnx import ocr_pdf as ocr_pdf_func


def pdf_to_xlsx(pdf_path: str, output_path: str, lang: str = "chi_sim+eng") -> bool:
    text = clean_pdf_text(pdf_path, min_chars=1)
    if not text.strip():
        sys.stderr.write("文本层缺失或为乱码，启用 OCR 识别扫描版 PDF...\n")
        text = ocr_pdf_func(pdf_path, lang)
    if not text.strip():
        return False

    wb = Workbook()
    ws = wb.active
    ws.title = "PDF Content"

    for line in text.splitlines():
        # 尝试按空白字符分割成列
        cells = line.split()
        for col_idx, cell_text in enumerate(cells, 1):
            ws.cell(row=ws.max_row, column=col_idx, value=cell_text)
        ws.row_dimensions[ws.max_row].height = 18

    wb.save(output_path)
    return True


def main():
    parser = argparse.ArgumentParser(description="PDF to XLSX converter")
    parser.add_argument("input", help="Input PDF file path")
    parser.add_argument("output", help="Output XLSX file path")
    args = parser.parse_args()

    success = pdf_to_xlsx(args.input, args.output)
    sys.exit(0 if success else 1)


if __name__ == "__main__":
    main()
