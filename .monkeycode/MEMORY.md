# User Instruction Memory

This file records user instructions, preferences, and teachings for reference in future interactions.

## Format

### User Instruction Entry
User instruction entries should follow this format:

[User Instruction Summary]
- Date: [YYYY-MM-DD]
- Context: [Mentioned scenario or time]
- Instructions:
  - [Content of user teaching or instruction, described line by line]

### Project Knowledge Entry
Entries discovered by the Agent during task execution should follow this format:

[Project Knowledge Summary]
- Date: [YYYY-MM-DD]
- Context: Discovered by Agent while performing [specific task description]
- Category: [Operations & Deployment|Build Methods|Testing Methods|Troubleshooting & Debugging|Workflow & Collaboration|Environment Configuration]
- Instructions:
  - [Specific knowledge points, described line by line]

## Deduplication Strategy
- Before adding a new entry, check for similar or identical instructions.
- If a duplicate is found, skip the new entry or merge it with the existing one.
- When merging, update the context or date information.
- This helps avoid redundant entries and keeps the memory file tidy.

## Entries

[Project Knowledge Summary]
- Date: 2026-08-25
- Context: Discovered by Agent while debugging FlowConvert static file serving issue
- Category: Troubleshooting & Debugging
- Instructions:
  - Go embed.FS with fs.ReadFile requires exact path matching - no double prefix concatenation
  - When using strings.TrimPrefix(path, "/") to get relative path, do NOT prepend "static/" again in ReadFile call
  - Bug pattern: fs.ReadFile(assets, "static/" + name) where name already contains "static/" prefix causes 404
   - Correct pattern: fs.ReadFile(assets, name) where name = strings.TrimPrefix(requestPath, "/")

[Project Knowledge Summary]
- Date: 2026-08-25
- Context: Discovered by Agent while building FlowConvert image conversion platform
- Category: Build Methods
- Instructions:
  - Build command: cd /workspace && go build -o flowconvert .
  - Run command: ./flowconvert --port 8080
  - Project structure: main.go (entry), internal/{config,handler,service}, web/{static,html}, scripts/{python}
   - Python dependencies required: opencv-contrib-python-headless, pillow, numpy, pdfminer.six, fpdf2, python-docx, openpyxl, python-pptx, translatepy, vtracer

[Project Knowledge Summary]
- Date: 2026-08-25~27
- Context: ID photo, vectorize, translation feature fixes
- Category: Troubleshooting & Debugging
- Instructions:
  - ID photo: use rembg u2netp model (4.57MB) not bria-rmbg (1GB); OpenCV 5.x removed CascadeClassifier
  - ID photo shoulder detection: alpha-channel analysis; smart_crop priority: face_info > alpha > center
  - ID photo algorithm: alpha hard threshold <0.15→0, >0.85→255 before compositing; zero mixed pixels
  - ID photo crop: head_total = shoulder_y - head_top, crop_h = head_total / 0.80, dpi=(300,300) in PIL save
  - ID photo head positioning: v67 algorithm, scale directly to output canvas, bilinear interp; head 7.7%-77.7%
  - ID photo endpoint: POST /api/convert/idphoto returns raw PNG (not JSON)
  - Vectorize input types: imageExts = ["jpg","jpeg","png","bmp","tiff","tif","webp","gif"]; .ai NOT accepted
  - AI format: Inkscape exports PDF; copy PDF to .ai (AI spec is PDF-compatible)
  - PDF translation: OCR via pdf2image+pytesseract for image PDFs; fallback pdfminer for text PDFs
  - reportlab requires TrueType fonts (TTC not supported); use wqy-zenhei.ttc or DroidSansFallbackFull.ttf
  - Install poppler-utils for pdf2image; tesseract-ocr with chi_sim/chi_tra for CJK OCR
  - Translation API: JSON body uses "source"/"target"; RunCmd timeout 60s prevents proxy 502/504

[Project Knowledge Summary]
- Date: 2026-08-27
- Context: Long video (>12s) multi-segment concatenation fix
- Category: Troubleshooting & Debugging
- Instructions:
  - ffmpeg concat demuxer resolves paths against LIST FILE's directory, NOT process cwd
  - Always convert segment paths to absolute (filepath.Abs) before writing into concat list file
  - Agnes API returns 1280x704 (16:9) / 704x1280 (9:16), h264 24fps, aac audio
  - Agnes occasionally fails segments with "DiffGenerator returned no result"; retry same request succeeds
  - Multi-segment generation should retry each segment (not silently drop it)
  - Real AI video verification: /tmp/opencode/concat_probe/ contains downloaded Agnes sample videos

[Project Knowledge Summary]
- Date: 2026-08-27
- Context: Discovered by Agent while fixing long video (>12s) multi-segment concatenation failure
- Category: Troubleshooting & Debugging
- Instructions:
  - ffmpeg concat demuxer resolves relative paths in the list file against the LIST FILE's directory, NOT the process cwd
  - When TmpDir is a relative path (e.g. "data/tmp"), writing `file 'data/tmp/vid_x/seg.mp4'` into a list file located at data/tmp/vid_x/ yields a doubled path (data/tmp/vid_x/data/tmp/vid_x/seg.mp4) and fails
  - Fix: always convert segment paths to absolute (filepath.Abs) before writing into the concat list file
  - Agnes video API returns 1280x704 (16:9) / 704x1280 (9:16), h264 24fps, time_base 1/12288, aac audio, duration ~= requested+float
  - Agnes occasionally fails segments transiently with "DiffGenerator returned no result"; retry same request succeeds - add per-segment retry in multi-segment generation
  - Multi-segment video generation should retry each segment (not silently drop it) to avoid incomplete videos
   - Real AI video verification: /tmp/opencode/concat_probe/ contains downloaded Agnes sample videos

[Project Knowledge Summary]
- Date: 2026-08-28
- Context: Fixed multi-provider translation fallback and language detection in scripts/translate.py
- Category: Troubleshooting & Debugging
- Instructions:
  - translatepy services: Google✓, Yandex✓, DeepL✓, LibreTranslate✓(with key), TranslateCom✓, MyMemory✓
  - translatepy's MyMemory translator uses IE7 UA by default; direct API test must use browser UA
  - detect_language must return None for Latin scripts (FR/DE/ES/IT/PT/NL), not "en"
  - Reorder detect_language checks: hiragana/katakana BEFORE han (Japanese text has CJK ideographs)
  - translate_chunk returns (text, engine, detected_src); detected from result.source_language.alpha2
  - ISO3→ISO2 mapping in translate.py: _ISO3_TO_2 dict (fra→fr, deu→de, esp→es, etc.)
  - MyMemory langpair must use explicit 2-letter codes; auto|XX rejected with 403
  - Go build: go build -ldflags="-s -w" -o flowConvert . produces 7.2MB stripped binary
  - Test: go test ./... all green; system curl tests all pass

## 项目构建与运行

- 构建命令: `go build -o flowConvert .`
- 运行命令: `./flowConvert`
- 默认监听端口: 8080
- 测试命令: `go test ./...`

## 项目依赖

- Python 3 + pip 包: pandas, openpyxl, python-docx, pdf2docx, Pillow, translatepy
- ONNX Runtime (用于证件照抠图模型)
- Tesseract OCR (chi_sim+eng) 用于 PDF 扫描件翻译
- 翻译服务：translatepy (Google/DeepL/MyMemory/TranslateCom 自动切换，无需 API Key)
