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
  - Python dependencies required: opencv-contrib-python-headless, pillow, numpy, pdfminer.six, fpdf2, python-docx, openpyxl, python-pptx, deep-translator, vtracer, pillow

[Project Knowledge Summary]
- Date: 2026-08-25
- Context: Discovered by Agent while implementing ID photo generation feature
- Category: Troubleshooting & Debugging
- Instructions:
  - opencv-python-headless 5.0+ does not include CascadeClassifier - use FaceDetectorYN or DNN models instead
  - Download SSD face detection model: res10_300x300_ssd_iter_140000.caffemodel + deploy.prototxt to /tmp/models/
  - Portrait photo algorithm: DNN face detection -> LAB color segmentation -> GrabCut refinement -> Alpha composite
   - Standard ID photo: head occupies 70-80% of image height, face center at ~38% from top

[Project Knowledge Summary]
- Date: 2026-08-26
- Context: Discovered while fixing vectorize, translation, and idphoto features
- Category: Troubleshooting & Debugging
- Instructions:
  - AI format conversion: Inkscape exports PDF, not .ai directly; copy PDF to .ai file (AI spec is PDF-compatible)
  - Vectorize input types: imageExts = ["jpg","jpeg","png","bmp","tiff","tif","webp","gif"] - .ai files are NOT accepted as input
  - translatepy works in this environment via Google Translate (translate.google.com accessible)
  - translate.py uses source_language parameter (not source) for translatepy Translator.translate()
  - MyMemory fallback engine returns empty engine string when translatepy succeeds - track engine after successful call
  - ID photo: rembg u2netp model (4.57MB) preferred over bria-rmbg (1GB) for speed
  - ID photo crop algorithm: alpha-channel-based shoulder detection, head_total = shoulder_y - head_top_y, crop_h = head_total / 0.80
  - ID photo width constraint: when head_width/crop_width > 0.70, enlarge crop to head_width/0.60
  - ID photo DPI: must set dpi=(300,300) in PIL save() for correct print dimensions
  - ID photo endpoint: POST /api/convert/idphoto returns raw PNG image (not JSON)
  - Translation API field names: JSON body uses "source" and "target" (not "source_language"/"target_language")
  - RunCmd timeout: 60 seconds default prevents proxy 502/504 errors on long translations

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
- Date: 2026-08-26
- Context: Fixed ID photo edge quality and PDF translation
- Category: Troubleshooting & Debugging
- Instructions:
  - ID photo edge fix: hard threshold alpha < 0.15 → 0, alpha > 0.85 → 255 before compositing
  - ID photo edges now show 0 mixed pixels (was 7588-10869) and 60%+ pure background pixels
  - PDF translation uses OCR (pdf2image + pytesseract) for image-based PDFs, fallback to pdfminer for text PDFs
  - reportlab requires TrueType fonts (TTC with postscript outlines not supported); use wqy-zenhei.ttc or DroidSansFallbackFull.ttf
  - Install poppler-utils for pdf2image; install tesseract-ocr with chi_sim/chi_tra langs for CJK OCR
  - Go build: go build -o flowConvert . (scripts are NOT embedded, only HTML files in web/embed.go)
  - ID photo shoulder detection: OpenCV 5.x removed CascadeClassifier; use rembg alpha channel analysis as primary method
  - ID photo smart_crop priority: face_info (alpha-estimated) > alpha-based detection > center fallback
  - ID photo face estimation from alpha: face_y = top + person_h * 0.35, face_h = person_h * 0.30, face_w = face_h * 0.72
  - ID photo crop formula: head_total = shoulder_y - head_top, crop_h = head_total / 0.80, bottom = top + crop_h
