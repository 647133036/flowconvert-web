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
  - Multi-segment generation should retry each segment (not silently drop it) to avoid incomplete videos
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

[Project Knowledge Summary]
- Date: 2026-09-04
- Context: Discovered by Agent while adding the OCR 文字识别 page (/ocr, POST /api/ocr)
- Category: Troubleshooting & Debugging
- Instructions:
  - PP-OCR 乱码根因：解码表布局必须为 ["blank"] + 字典 + [" "]（blank 在 0 号位、空格在最后一位），写成 dict + [" ", "?", "blank"] 会让每个汉字偏移 1 并产生 0.97+ 高置信度乱码
  - PP-OCR 模型与字典必须来自同一来源：官方 ch_PP-OCRv4_rec_infer.onnx 把字符表写在模型元数据 custom_metadata_map["character"]，优先从元数据读字典；仓库里的 ppocr_keys_v1.txt（6623 行）与官方模型内嵌字典逐字一致，仅作兜底
  - models/ocr/rec_v5.onnx（18385 通道）与 v6_rec.onnx（18710 通道）是多语言词表模型，仓库无对应字典，不可使用；只用 det.onnx / rec.onnx / cls.onnx
  - PP-OCR det/rec 依赖 pyclipper + shapely（官方 DBPostProcess unclip），requirements.txt 已包含
  - Tesseract 与 PP-OCR 均已验证可用：auto 优先 Tesseract、不可用时回退 PP-OCR；engine=pp_ocr 反向回退 Tesseract
  - 本机 apt 源默认缺包，装 tesseract 前必须先 `apt-get update`，否则 tesseract-ocr-chi-sim / poppler-utils 报 Unable to locate package
  - 容器内无任何字体，生成含中文的 OCR 测试图前需 `apt-get install -y fonts-wqy-zenhei`，PIL 才能渲染中文
  - Tesseract 预处理：只做灰度+放大（<1200px 宽按 1200 缩放），不要加自适应二值化，二值化会明显降低准确率
  - Tesseract `chi_sim+eng` 识别清晰图效果良好；模糊/小字准确率显著下降
   - 中文/英文混排测试图：/tmp/ocr_cn.png；带文本层 PDF：/tmp/ocr_pdf_text.pdf；扫描 PDF：/tmp/ocr_pdf_scan.pdf；多页含跨页页眉 PDF：/tmp/ocr_multipage.pdf；表格 PDF：/tmp/ocr_table.pdf；文本层为乱码的 PDF：/tmp/ocr_pdf_garbled_layer.pdf
   - PDF 文本层乱码检测（scripts/ocr.py text_layer_is_garbled）不能用 zlib 压缩率或字符频率类指标：非重复中文正文 zlib 比 0.68 与乱码 0.71 几乎相同，二元组唯一性 0.93 也高于乱码的 0.68，会误判；有效判据是「私用区字符占比 >1%」或「占比≥5% 的字母体系平均连续片段长度过短且单字片段占比>30%」（正常英文平均 run 长度约 5.5，乱码约 1-2.3）
   - scripts/ocr.py 的 stdout 首行会混入 PyMuPDF `fitz` 弃用告警，解析其 JSON 输出须从第一个 `{` 开始截取；回归脚本 /tmp/t_garbled.py（判据）与 /tmp/test_ocr.py（流水线，期望 FAILS: 0）
  - 单元测试里 service.ScriptPath 以 cwd 解析 scripts/，handler 集成测试须先 os.Chdir 到仓库根目录
  - handler 集成测试中 FileStore.Register 不创建 OutDir，测试 cfg 须先 os.MkdirAll(cfg.OutDir)，否则报 no such file or directory
  - Go 1.25.6: Request.ParseForm 不解析 multipart 请求体（仅处理 x-www-form-urlencoded），multipart 须先 ParseMultipartForm 并从 r.Form 读字段
   - 浏览器与 curl 习惯用 multipart 提交文档 URL，/api/ocr 在无 file 时会回退读取 multipart 里的 url 字段
   - OCR 慢的根因是 detect_orientation 而非识别本身：它对 0/90/180/270 各跑一次全分辨率 tesseract OSD，单这一项就占 47.7s（通用模式总耗时 132.9s）；image_words 逐个 PSM 试到 15.8s
   - 提速做法（已验证）：方向检测采样图缩到 max_side=760、按宽高比排序候选方向（横图先试 90/270、竖图先试 0/180）、单方向平均置信度 ≥0.6 即收敛；image_words 首个 PSM 达到 score≥0.6 且词数≥20 即提前返回。改后通用模式 22.2s，方向检测 7.2s
   - 方向检测不能用分辨率换准确率的直觉验证：760px 采样图对 90° 旋转仍稳定判对，2960x2000 原图缩到 760 后横竖图分别返回 90 与 0


## 项目构建与运行

- 构建命令: `go build -o flowConvert .`
- 运行命令: `./flowConvert`
- 默认监听端口: 8080
- 测试命令: `go test ./...`

## 项目依赖

- Python 3 + pip 包: pandas, openpyxl, python-docx, pdf2docx, Pillow, translatepy
- ONNX Runtime (用于证件照抠图模型)
- Tesseract OCR (chi_sim+eng) 用于 PDF 扫描件翻译
- 统一 OCR 模块: scripts/pp_ocr_onnx.py（引擎实现）；对外入口 scripts/ocr.py，供 /api/ocr 调用
- PP-OCR ONNX 模型在 models/ocr/: det.onnx (检测), rec.onnx (识别，字符表内嵌模型元数据), cls.onnx (方向分类，未启用), ppocr_keys_v1.txt (词表 6623 行，兜底)
- PP-OCR rec 模型预处理: resize height=48、宽度按整批最大宽高比、右侧补零、normalize (x/255 - 0.5)/0.5；CTC decode 表为 ["blank"] + 字典 + [" "]，blank=0，置信度取每时间步最大概率均值
- EasyOCR 安装: pip install easyocr (需下载~600MB模型)
- 翻译服务：translatepy (Google/DeepL/MyMemory/TranslateCom 自动切换，无需 API Key)
