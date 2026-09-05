#!/usr/bin/env bash
# 安装 FlowConvert 运行所需的全部依赖：系统包 + Python 包
#
# 用法:
#   bash install.sh              # 系统包走 apt（需要 root）
#   sudo bash install.sh         # 非 root 时
#   SKIP_SYSTEM=1 bash install.sh  # 系统包已装好，只装 Python
#
# 产物:
#   .venv/                       Python 虚拟环境
#   .venv/bin/python             启动时通过 FLOWCONVERT_PYTHON 指定

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PY="${PYTHON:-python3}"
VENV="${VENV:-$ROOT/.venv}"

echo "[1/3] 系统依赖"
if [ "${SKIP_SYSTEM:-0}" = "1" ]; then
  echo "  已跳过 (SKIP_SYSTEM=1)"
elif command -v apt-get >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq \
    ffmpeg poppler-utils python3-venv python3-pip \
    tesseract-ocr tesseract-ocr-chi-sim tesseract-ocr-eng
  echo "  已安装 ffmpeg / poppler-utils / tesseract(中英)"
else
  echo "  非 apt 系统，请手动安装: ffmpeg poppler-utils tesseract-ocr tesseract-ocr-chi-sim tesseract-ocr-eng"
fi

echo "[2/3] Python 依赖 ($VENV)"
if [ ! -x "$VENV/bin/python" ]; then
  "$PY" -m venv "$VENV"
fi
"$VENV/bin/pip" install -q -r "$ROOT/requirements.txt"

echo "[3/3] 校验"
"$VENV/bin/python" - <<'PYEOF'
import importlib.util
mods = [
    "PIL", "cv2", "numpy", "docx", "openpyxl", "pptx", "fitz",
    "onnxruntime", "pytesseract", "reportlab", "requests",
    "translatepy", "opencc", "jieba", "rembg", "vtracer",
    "pyclipper", "shapely",
]
missing = [m for m in mods if importlib.util.find_spec(m) is None]
if missing:
    print("  缺失:", ", ".join(missing))
    raise SystemExit(1)
print("  全部", len(mods), "个 Python 依赖就绪")
PYEOF

echo
echo "安装完成。启动方式:"
echo "  cd $ROOT"
echo "  FLOWCONVERT_PYTHON=$VENV/bin/python ./flowConvert"
echo
echo "如需 AI 图片/视频生成，复制 .env.example 为 .env 并填入 AGNES_API_KEY。"
