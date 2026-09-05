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
  - ID photo matting: alpha hard threshold <0.15→0, >0.85→255 before compositing to avoid edge halos; shoulder via alpha-channel analysis, smart_crop priority face_info > alpha > center
  - ID photo geometry: head_total = shoulder_y - head_top, crop_h = head_total / 0.80, head placed 7.7%-77.7%, scale directly to output canvas with bilinear interp, save dpi=(300,300)
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


[Project Knowledge Summary]
- Date: 2026-09-05
- Context: Discovered by Agent while replacing the neural table pipeline in scripts/ocr.py
- Category: Troubleshooting & Debugging
- Instructions:
  - 带边框表格靠墨迹线段还原比词框聚类可靠：tesseract 主遍会整列漏词（本例 3 列表格只剩第 3 列），而表格线不依赖文字。检测用形态学开运算 + 连通域取长条（`connectedComponentsWithStats` 后按 w/h 过滤），按中心坐标 6px 合并同类线段
  - 不要用整页投影覆盖率找表格线：表格只占版面一部分宽度时投影天生达不到整页 50%，会把所有表格线判掉。同理不要假设表格四边都有框线，横线端点与竖线中心差 1-2px，须先合并再当同一根线
  - 脚本里 sx/sy 约定为「每页面单位对应多少像素」，因此像素→页面要**除**、页面→像素要**乘**。坐标换算写反时症状是裁剪框全为空、逐格补识别静默返回空串（`fill_cells` 生效但输出和没生效一模一样），排查时先打印裁剪框尺寸
  - 表格与相邻行的归属按**行中心**判定，不要按包围盒重叠：词框高度能超出真实行框数倍，包围盒再加外扩比例会把表格上方紧贴的说明行一起吞掉；吞掉后两个不相邻的标题会合并成一个块
  - OCR 偶发产生 1x1 的退化词框，聚类后成为独立行，又因框太窄过不了任何重叠判定而残留在输出里；行构造时直接按 `w>=2 and h>=2` 丢弃
  - 行尾标点（「，」）常拿到行内偏移的词框被单独聚成一行，读起来像断句；只含标点的行且 `ln.y <= prev.y + prev.h` 时并回上一行
  - 10pt 左右的中文单字/单行重识别救不回来：孤立字框在 PSM 6/7/8/10/13 下全读成拉丁乱码，整行裁剪在 fx=2/3/4 放大后置信度掉到 0-22 全乱。唯一有效的兜底是固定搭配修复——要求「其余字符完全一致且仅一字不同」，数字位必须原样匹配（否则「共5小题」被改成「每小题」），且要先登记已正确读出的搭配区间，因为「三个选项/四个选项」彼此只差一字会互相覆盖
  - 「整串是否只有标点」的正则判断必须用 `fullmatch`：`match` 遇到 `+` 只要首字符命中就返回成功，导致 `(__)14.When` 被当成标点、后面的真空格全被吃掉，英文行读成 `(__)14.Whendoes`
  - 试卷章节标题的字号与说明句/选项行接近，字号无法可靠分离；有效判据是「罗马序号 + 句尾是 `)`/`）` 的括注」，说明句以「。」/「，」/`.` 收尾可据此排除，选项行（`A. … B. …`）单独排除

[Project Knowledge Summary]
- Date: 2026-09-05
- Context: Discovered by Agent while evaluating lw.PPOCR.OpenCVDNN (GitHub) as a way to improve Chinese OCR
- Category: Troubleshooting & Debugging
- Instructions:
  - 本地 `models/ocr/rec.onnx`（10.8MB）与 `ppocr_keys_v1.txt`（6623 行）完全对齐：输出 6625 = 1(blank)+6623(字典)+1(空格)，CTC 解码表 `[blank]+keys+[" "]` 可用。之前记的「rec_v6 字典不匹配是死档」只对那个 21MB 的 `v6_rec.onnx` 成立，不影响当前在用的 rec.onnx
  - 逐索引验证过：空格位 6624 正常输出，英文单词间空格保留，字典与模型同一导出
  - 中文识别率的两条路已实测：PP-OCR 中文远强于 tesseract（`长对话理解`/`每小题1分`/`三个选项`/`读两遍` 原生全对，置信度 0.88-0.98，零修复）；tesseract 这些行全错（`再解`/`1分`/`锭项`/`两允`），靠固定搭配硬修
  - PP-OCR ch 模型（本地 10.8MB 与 lw.PPOCR.OpenCVDNN 的 4.4MB Tiny）同档：13 行中文同图对比互有胜负（ours `每小避`/`彬据`/`丧格`；repo `请相姆`/`液格`/`一闭`），无净提升
  - 英文 c/e 混淆是 PP-OCR ch 模型通病，两个模型都存在（`noticc`/`becausc`/`hcrc`），换模型修不掉
  - lw.PPOCR.OpenCVDNN 复用结论：不能提高中文识别率；它的 rec.onnx（6906=1+6904+1）是同系 Tiny 模型，字典 6904 行配它自己的模型，我们 6625 通道用不了；且要求 OpenCV 5.0+（环境是 cv2 4.x）。已把它的 ONNX 下载到 /tmp/lwrepo 验证过 SHA-256 与 manifest 一致
  - 提高中文的正确路径：按行路由——tesseract 主遍保英文与块结构，CJK 占比 >0.3 的行用该行 crop 重跑 PP-OCR rec。两个引擎 scripts/ocr.py 已接好（`get_engine`/`PPocREngine`/`TesseractEngine`），中文框约占 11%，补跑 rec ≈0.7s

[Project Knowledge Summary]
- Date: 2026-09-05
- Context: Discovered by Agent while generalizing Chinese OCR (routing + jieba corrector, avoiding exam overfitting)
- Category: Troubleshooting & Debugging
- Instructions:
  - 通用纠错评分：纯 jieba 词频会把正确的「每小题」误判为 OOV（它在 jieba 里是单 token 但 FREQ=0），反而奖励错误词「小器/小腿」（真实词）。正确做法按 token 长度平方奖励连贯长词、词频仅作等长平局、对词典外孤字重罚；经验阈值增益≥4.0 能切分强修复（逸→选 6.6、飓→题 4.7）与误改（题→盟 3.4、话→活 1.1），宁可漏改不可误改
  - CJK 路由架构：tesseract 主遍保英文/块结构/表格，CJK 占比过半的行在 analyze_page 的 group_blocks 前用该行 gray crop 重跑 PP-OCR rec（`ppocr_rec_line_chars` 逐字符 top-K）+ 通用纠错原位替换 line.text。领域无关，不依赖任何固定搭配
  - 考试固定搭配（EXAM_IDIOMS/repair_exam_phrases）降级为 exam 模式（默认关），叠加在路由结果之后；`exam=true` 时在 `_route_cjk_lines` 末尾再跑一次 `repair_exam_phrases(new_text)`，修通用纠错漏掉的弱信号错字（调分→满分 2.5、读两测→读两遍、中逸出→中选出）。route_cjk 默认 True，exam 默认 False
  - image_words 坐标修复（关键 bug）：`page_gray` 经 `_prepare` 会把小图（宽<max_width）放大至 2-4 倍，词框因此是放大后 gray 的像素坐标，而图像路径 `pw=img.width` 未回缩 → analyze_page 里 `sx=gray.width/pw` 再乘一次造成双重缩放，路由裁剪与表格补识别全跑偏（小图症状：截断/重复/乱码）。修复：`image_words` 末尾把词框按 `image.width/gray.shape[1]` 还原回输入图像素坐标。PDF 路径 150/300dpi 不触发放大（fx=1）故行为不变
  - PP-OCR rec 输出已是 softmax 概率（各帧和=1），conf 直接取帧内字符概率，不能再套 softmax；逐字 top-K 取每个合并段内该字符概率最大帧的 argsort
  - 硬错字残留在通用模式下可接受：正确字不在 PP-OCR top-K（潤/遍/请/据 不在各自 top-6）或模型过度自信（conf≥0.85 被安全闸拦）时通用纠错修不了；exam 模式的固定搭配可补修考试场景。免费快速通用 OCR 不追求零错字
  - Go 服务 `OcrOptions` 是固定字段集（不含 route_cjk/exam），服务走 ocr.py 默认值：route_cjk=true（路由开）、exam=false。服务读盘上 scripts/ocr.py 即时生效，无需重启。exam 模式当前仅 CLI `--options '{"exam":true}'` 可达
  - 验证集：650.pdf（考试，路由+exam 全对：长对话理解/满分/三个选项/读两遍/中选出/信息转换/单项选择，仅剩你称/谢根回模型真混淆）；/tmp/nonexam2.png（竖版非试卷，通用模式全对零误改，路由还修好了局动→启动、繁体→简体）



## 项目构建与运行

- 构建命令: `go build -o flowConvert .`
- 运行命令: `./flowConvert`
- 默认监听端口: 8080
- 测试命令: `go test ./...`
- Rust 侧（src/，与 Go 并行的 axum 重写）: 检查 `cargo check --lib`，单元测试 `cargo test --lib`，集成测试 `cargo test --test integration`，构建 `cargo build`
- Rust 工具链不在环境 PATH 里，已装在 /root/.cargo/bin/cargo 与 /root/.rustup，调用时用全路径；首次 `rustup default stable` 需联网下载工具链
- 集成测试在 tests/integration.rs 里自建 AppState，给 AppState 加字段（如 ocr_jobs）后必须同步更新该构造，否则集成测试编译失败
- Rust 单元测试通过 `cargo test --lib` 才能覆盖（handler/ocr.rs、service/ocr.rs 的测试都在 lib 内），跑 `--test integration` 不会执行它们

## 排查与调试

- 手写测试 PNG 的正确做法：IDAT 用 zlib stored block（`78 01 01` + LEN/NLEN 小端 + 原始扫描线），CRC 必须对「块类型名 + 数据」计算，见 `append_png_chunk`；早期版本 IDAT 字节 `0x08 0x90 0x03 0x00 0x00` 不是合法 deflate 流，文件能存盘但 PIL 报 `cannot identify image file`
- 排查这类「秒失败」时先看服务端 `eprintln!`/日志里的 `run_err` 全文，OCR 任务接口的 error 字段是脱敏文案「识别失败，请稍后重试」，不含真实原因
- OCR 中文行准确率（scripts/ocr.py `refine_words`）：多语言组合 chi_sim+chi_tra+eng 会把简体读成繁体并混入拉丁乱码，对形态判为中文的行改用 chi_sim 单语言重识别才准。形态判据是连通域 median 宽高比 ≥0.82（中文 0.83-1.00 / 英文 0.59-0.80）；墨迹密度（中文 0.18-0.30 / 英文 0.12-0.19）与英文行重叠不可用；unsharp / contrast / upscale 预处理对结果无改善
- 章节罗马序号还原的边界：tesseract 把竖线序列 III 并成一个字符，必须按连通域计数窄竖条；裁剪右边界取 `x_stop - 2`，多切进中文字形 1 像素就把 IV 读成 II 或空；前缀必须「笔画互不横向重叠 + 宽笔画填充率 ≤0.32」，否则「听」字切出的笔画会被误判成 V

## 项目依赖

- 完整依赖清单在 `requirements.txt`（已按 scripts/*.py 的实际 import 审计过）；系统包需 apt 安装 ffmpeg / poppler-utils / tesseract-ocr(+chi-sim,eng)
- 一键安装: `bash install.sh`（建 .venv 并装全部依赖）；启动时 `FLOWCONVERT_PYTHON=.venv/bin/python ./flowConvert`
- 密钥不编译进二进制：`internal/config/config.go` 的 `loadDotEnv()` 启动时读工作目录下 `.env`，模板见 `.env.example`
- 可选增强（缺失自动降级，不需要装）: paddleocr（版面/表格检测→启发式）、easyocr（备用引擎→tesseract/PP-OCR）；`mtcnnruntime` 在 PyPI 上不存在，证件照人脸检测缺失时降级为 OpenCV 级联（cv2.data.haarcascades 自带 xml，零新增依赖）
- 审计时移除的未使用依赖: pdfminer.six、pdf2image、pdf2docx（scripts/ 里没有任何 import）
- ONNX Runtime (用于 PP-OCR 与证件照抠图模型)
- Tesseract OCR (chi_sim+eng) 用于 OCR 主遍
- 统一 OCR 模块: scripts/pp_ocr_onnx.py（引擎实现）；对外入口 scripts/ocr.py，供 /api/ocr 调用
- PP-OCR ONNX 模型在 models/ocr/: det.onnx (检测), rec.onnx (识别，字符表内嵌模型元数据), cls.onnx (方向分类，未启用), ppocr_keys_v1.txt (词表 6623 行，兜底)
- PP-OCR rec 模型预处理: resize height=48、宽度按整批最大宽高比、右侧补零、normalize (x/255 - 0.5)/0.5；CTC decode 表为 ["blank"] + 字典 + [" "]，blank=0，置信度取每时间步最大概率均值
- 引擎优先级 get_available_engine: tesseract > easyocr > pp_ocr
- 翻译服务：translatepy (Google/DeepL/MyMemory/TranslateCom 自动切换，无需 API Key)
- Agnes API 未配置时的降级: 图片生成走本地合成、视频生成走本地 ffmpeg 合成（notice 字段提示「AI 生成失败，已降级为本地合成」）

## 依赖与环境排查（2026-09-05 审计）

- 页面整体报「处理失败/识别失败」时先查后台终端日志里的 Python traceback 全文，handler 的 error 字段是脱敏文案，不含真实原因。本轮定位到三个页面的根因都是缺依赖而非代码 bug: 证件照缺 rembg、矢量化缺 vtracer、视频降级缺 ffmpeg
- `requirements.txt` 曾长期与实际 import 脱节（缺 rembg/vtracer/python-pptx/reportlab/translatepy/jieba/opencc，多列了 pdfminer.six/pdf2image/pdf2docx），导致按它装依赖的机器必然坏页。判断依赖是否必需的正确做法是 grep scripts/ 下所有 `import`/`from`（含函数体内的），再逐个 `python3 -c "import X"` 验证
- pip 一条命令装多个包时，只要有一个包无法解析（例如 PyPI 上不存在的 `mtcnnruntime`），整批安装都会回滚、一个都装不上；错误只在尾部一行。拆批或先单独验证包名可解析能省大量时间
- `python3 -m venv` 在本环境报 ensurepip 不可用，需先 apt 安装 `python3-venv`（install.sh 已包含）
- vtracer 安装不会覆盖已装的 opencv-python-headless，装完务必回归验证 cv2 与 PP-OCR 引擎仍正常
- 判断 PDF 小字识别失败是质量原因还是引擎极限: 先看行级 conf 与 size（8pt 是最小档），再统计裁剪图灰度直方图，双峰且 min=0/max=255 说明是干净的数字渲染而非退化扫描件；两者都排除后基本可判定为引擎极限
- 试卷固定搭配修复支持多字误读了（`_phrase_diff` 后 `fix==1` 或 `n>=6 且 fix<=3 且 matched*2>=n`），可修「请根据」→「谢根回」这类低分辨率整串糊掉的情况；单字误读仍然走原来的 fix==1 分支
- OCR 通用纠错与考试固定搭配的职责边界: jieba 通用纠错处理「正确字不在模型 top-K」之外的形近错字，exam 模式固定搭配处理整串糊掉或需 2+ 字同改的情况

