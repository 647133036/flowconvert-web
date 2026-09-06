# User Instruction Memory

This file records user instructions, preferences, and teachings for reference in future interactions.

## Format

### User Instruction Entry
[User Instruction Summary]
- Date: [YYYY-MM-DD]
- Context: [Mentioned scenario or time]
- Instructions:
  - [Content of user teaching or instruction]

### Project Knowledge Entry
[Project Knowledge Summary]
- Date: [YYYY-MM-DD]
- Context: Discovered by Agent while performing [specific task description]
- Category: [Operations & Deployment|Build Methods|Testing Methods|Troubleshooting & Debugging|Workflow & Collaboration|Environment Configuration]
- Instructions:
  - [Specific knowledge points]

## Deduplication Strategy
- Before adding a new entry, check for similar or identical instructions.
- If a duplicate is found, skip it or merge it with the existing one.
- When merging, update the context or date.
- If the file exceeds ~150 lines, merge entries of the same module/category instead of appending.

## Entries

[Project Knowledge Summary]
- Date: 2026-09-06
- Context: 构建与运行约定（Go 主实现 + Rust axum 并行重写）
- Category: Build Methods
- Instructions:
  - 构建 `go build -o flowConvert .`；发布版可加 `-ldflags="-s -w"`（约 7.2MB）。运行 `./flowConvert`，默认监听 8080
  - 测试 `go test ./...`；改完必跑 `go vet ./...`
  - 项目结构：main.go（入口），internal/{config,handler,service}，web/{static,html}，scripts/{python}
  - Rust 侧（src/，与 Go 并行的 axum 重写）：`cargo check --lib`、`cargo test --lib`、`cargo test --test integration`、`cargo build`。工具链不在 PATH，装在 /root/.cargo/bin/cargo 与 /root/.rustup，须用全路径；首次 `rustup default stable` 需联网下载
  - tests/integration.rs 自建 AppState，给 AppState 加字段后必须同步更新该构造，否则集成测试编译失败；handler/service 单元测试只在 `cargo test --lib` 下执行，`--test integration` 不会跑
  - 单元测试里 service.ScriptPath 以 cwd 解析 scripts/，handler 集成测试须先 os.Chdir 到仓库根；FileStore.Register 不创建 OutDir，测试 cfg 须先 os.MkdirAll(cfg.OutDir)
  - Go 1.25.6：Request.ParseForm 不解析 multipart 请求体（仅 x-www-form-urlencoded），multipart 须先 ParseMultipartForm 再从 r.Form 读字段

[Project Knowledge Summary]
- Date: 2026-09-06
- Context: 依赖与环境审计（发现多个页面「报处理失败」的真因都是缺依赖而非代码 bug）
- Category: Environment Configuration
- Instructions:
  - Python 依赖见 requirements.txt（已按 scripts/*.py 实际 import 审计）；一键安装 `bash install.sh`（建 .venv 装全部），启动 `FLOWCONVERT_PYTHON=.venv/bin/python ./flowConvert`
  - 系统二进制需 apt 装：ffmpeg / poppler-utils / tesseract-ocr(+chi-sim,eng) / **inkscape** / **potrace**。装 tesseract 前必须先 `apt-get update`（apt 源默认缺包，否则报 Unable to locate package）；容器内无任何字体，PIL 渲染中文需 `apt-get install -y fonts-wqy-zenhei`
  - **矢量输出 AI/EPS/PDF 靠 inkscape、DXF 靠 potrace，是系统二进制不是 Python 依赖**：缺它们时页面报「inkscape 未安装」而非转换出错。scripts/vectorize.py 只有 svg/sketch/fig/topng/topbm/gray/pdf_import_ok，不含 dxf/eps，无法纯 Python 兜底，必须装系统包
  - inkscape 1.x **不支持 `--export-type=ai`**（允许列表里没有 ai），代码走 `--export-type=pdf` 再复制成 .ai——Illustrator 8+ 的 .ai 本就是 PDF 兼容格式，属现代标准；`--export-type=ps` 可用，产出真正 PostScript
  - DXF 输出是 potrace 单色描摹（`-b dxf`）走 PBM 中间格式，彩色信息丢失属预期。fig 输出是应用自定义 JSON 包裹（build_fig_file），不是真实 Figma .fig 容器（protobuf/zip），环境内没有公开的 SVG→FIG 转换器
  - 密钥不编译进二进制：internal/config/config.go 的 loadDotEnv() 启动时读工作目录下 .env，模板见 .env.example。**本环境的 .venv 是空壳**（site-packages 为空、bin/python 只是指向 /usr/bin/python3 的符号链），全部 Python 依赖装在系统 python3 上，因此 `.env` 里 `FLOWCONVERT_PYTHON` 必须留空（走 PATH），填 `.venv/bin/python` 会导致所有脚本 ImportError。新部署才有 .venv 需填绝对路径
  - AI 已配置在 `.env`：`AGNES_API_KEY` + `AGNES_BASE_URL=https://apihub.agnes-ai.com/v1`。模型名集中在 internal/service/aiclient.go 的常量 `agnesVideoModel`（agnes-video-2.5-flash）与 `agnesImageModel`（agnes-image-2.5-flash），改模型只改这两处，别再散落到 imagegen.go / videogen.go 里硬编码
  - Agnes API 未配置时的降级：图片生成走本地合成、视频生成走本地 ffmpeg 合成。判断是否降级看 notice 字段是否为空——降级会写入「AI 不可用/生成失败，已降级为本地合成视频」，为空即走了真实 AI；本地合成的图很小且色彩单一（<100KB、颜色簇极少），真实 AI 图 1.4MB+、颜色簇 32
  - 判断依赖是否必需的正确做法是 grep scripts/ 下所有 import/from（含函数体内），再逐个 `python3 -c "import X"` 验证。requirements.txt 曾长期与实际 import 脱节（缺 rembg/vtracer/python-pptx/reportlab/translatepy/jieba/opencc，多列 pdfminer.six/pdf2image/pdf2docx），按它装依赖的机器必然坏页
  - pip 一条命令装多个包时只要有一个包无法解析（如 PyPI 上不存在的 mtcnnruntime），整批安装都回滚、一个都装不上，错误只在尾部一行。拆批或先单独验证包名可解析能省大量时间
  - `python3 -m venv` 在本环境报 ensurepip 不可用，需先 apt 装 python3-venv（install.sh 已包含）。vtracer 安装不会覆盖已装的 opencv-python-headless，装完务必回归验证 cv2 与 PP-OCR 引擎仍正常
  - 可选增强（缺失自动降级，不需装）：paddleocr（版面/表格检测→启发式）、easyocr（备用引擎→tesseract/PP-OCR）
  - 统一 OCR 模块 scripts/pp_ocr_onnx.py（引擎实现），对外入口 scripts/ocr.py 供 /api/ocr 调用。PP-OCR ONNX 模型在 models/ocr/：det.onnx、rec.onnx、cls.onnx（方向分类未启用）、ppocr_keys_v1.txt（词表 6623 行兜底）。引擎优先级 get_available_engine: tesseract > easyocr > pp_ocr
  - reportlab 要求 TrueType 字体（不支持 TTC），用 wqy-zenhei.ttc 或 DroidSansFallbackFull.ttf。PDF 翻译对图像 PDF 用 OCR、文本 PDF 用 pdfminer 兜底
  - 翻译服务 translatepy（Google/Yandex/DeepL/LibreTranslate(需key)/TranslateCom/MyMemory 自动切换，无需 API Key）。MyMemory 默认用 IE7 UA，直连测试须换浏览器 UA；detect_language 必须对拉丁文字返回 None 而非 en，日文检查（假名）要排在汉字之前
  - 「链接翻译」POST /api/translate/url：复用 fetch.go 的 checkHost + ssrfSafeDialer（SSRF 防护，禁止内网/回环）下载网页 → ExtractWebText 提取正文（正则去 script/style/注释/标签 + html.UnescapeString，块级标签换行）→ TranslateText 翻译；前端 web/translate.html 有 URL 输入区。Go 侧无第三方 HTML 解析库，纯标准库正则提取

[Project Knowledge Summary]
- Date: 2026-09-06
- Context: 证件照「生成成功但预览不显示」与真实大照片必失败的排查（scripts/idphoto.py, internal/service/idphoto.go）
- Category: Troubleshooting & Debugging
- Instructions:
  - **「生成成功但网页没有预览图」的头号嫌疑是 CSP，不是生成逻辑**：middleware.go 的 CSP `img-src 'self' data: https:` 缺 `blob:` 时，`URL.createObjectURL(blob)` 的 `blob:` URL 被浏览器直接拒载、`<img>` 永远空白。三条证据要一起看才能定位：①上传预览正常（走 FileReader.readAsDataURL，`data:` 在白名单内）②下载正常（`<a download href=blob:...>` 是用户触发的导航，不受 img-src 约束）③下载与预览共用同一个 blobUrl，所以下载能用即证明数据正确、纯前端加载被拦。诊断动作是 `curl -sI` 看响应头 img-src 白名单有没有 `blob:`。修在 csp 常量加 `blob:`；ocr.html 也用 createObjectURL，同修生效
  - 抠图必须用 `u2net_human_seg`（人像分割专用），通用 u2net/u2netp/silueta 会把躯干四肢局部误判成背景、换底色时身体被吃掉（躯干保留率 human_seg 68.7% > u2netp 50.5% > silueta 42.9%）。alpha 合成前硬阈值 <0.15→0、>0.85→255 避免边缘光晕
  - rembg 2.x 模型缓存目录是 `~/.rembg/models/<name>/`（不是旧版 ~/.u2net）。每次请求 Go 都新建 Python 进程重载模型，**必须给 new_session 传 sess_opts 且 graph_optimization_level=ORT_ENABLE_BASIC**，否则 170MB 的 u2net_human_seg 全图优化加载要 ~36s、页面看起来卡死（基础优化后 ~2s）；install.sh 有预热步骤避免新部署首请求下载 170MB
  - rembg 的 u2net 系列 predict 内部固定 `normalize(..., (320,320))`，推理恒在 320×320 进行；输入分辨率超过 1280 不提升抠图质量只放大数组内存（本环境总内存 8GB、可用常只剩 ~1GB，2000px 输入把单请求拖到 20-60s）。make_idphoto 的 max_dim 已改为 `min(2000, max(1280, 目标长边×2))`
  - YuNet（cv2.FaceDetectorYN）在全分辨率大图上会整图漏检：2000×2800 直接检测 0 张人脸，缩到最长边 1280 可检出（score 0.768）。**必须多尺度重试**：大图先缩到 `_YUNET_MAX_SIDE=1280` 检测、坐标除以缩放比还原，失败再对原图重试一次。MTCNN 旧代码有 scale=2 重试而 YuNet 移植时漏掉了，这是「上传真实大照片必失败」的根因。mtcnnruntime 在 PyPI 上不存在，模型用 models/face/face_detection_yunet_2023mar.onnx（OpenCV 官方，精度优于 MTCNN，已随仓库入库；非降级方案，勿退回 Haar 级联——OpenCV 5.x 已移除 CascadeClassifier）
  - 证件照接口不能复用 RunCmd 的共享 60s 超时：实测小图 0.9s、大图冷加载 27s、内存吃紧时单次抠图可达 60s+，60s 会误杀有效请求。MakeIdPhoto 已单独用 RunCmdTimeout(180s, ...)
  - 用户可读错误要透传而不是脱敏：Python 失败时输出 `{"error":"未检测到清晰人脸，请换一张正面照重试"}` 但退出码为 0（不产文件），Go 侧 UserFacingError 类型承载该消息、handler 用 errors.As 识别后原样返回。前端 catch 分支另需复位上次错误态（红字/红叉图标/隐藏下载按钮/残留旧结果图），否则首次失败、二次成功后结果不可见
  - 接口契约：POST /api/convert/idphoto 返回原始 PNG（不是 JSON）。几何：head_total = shoulder_y - head_top，crop_h = head_total/0.80，头部占 7.7%-77.7%，双线性插值缩到输出画布，save dpi=(300,300)
  - 手写测试 PNG 的正确做法：IDAT 用 zlib stored block（`78 01 01` + LEN/NLEN 小端 + 原始扫描线），CRC 对「块类型名 + 数据」计算，见 append_png_chunk；非法 deflate 流（如 `08 90 03 00 00`）能存盘但 PIL 报 cannot identify image file

[Project Knowledge Summary]
- Date: 2026-09-06
- Context: 视频生成降级与 >12s 视频分段拼接跳帧排查（internal/service/videogen.go concatVideos）
- Category: Troubleshooting & Debugging
- Instructions:
  - >12s 视频分段生成后用 ffmpeg concat demuxer 拼接。分段来自独立生成任务，分辨率/帧率/GOP/起始 PTS 各不相同，直接 `-c copy` 流拷贝会把这些差异原样写进文件：播放器只能从最近关键帧解码，接缝处出现时间戳回退与跳帧。实测 30fps/180 帧 + 24fps/144 帧的拼接产生 **15 处负 PTS 间隔**，时长还被报成 9.6s（实际 12s）
  - 只对 concat 输出做 `-r 30 -fps_mode cfr` 也不行：输入帧率不一致时 CFR 滤镜会**丢帧**，324 帧输入只剩 290、时长缩水到 9.667s。正确做法是**先逐段归一化**（`scale=W:H,setsar=1`、`-r`、cfr、`-start_time 0`、统一 libx264 参数），再对归一化后的分段重编码拼接，实测 360 帧、12.000000s、0 负 PTS
  - 接缝连续性验证法：`ffprobe -show_entries frame=pts_time -of csv=p=0` 全量导出后算相邻间隔，负间隔数必须为 0、间隔应恒为 1/fps；同时校验总帧数等于各段帧数按输出帧率折算之和（用 `-count_frames -show_entries stream=nb_read_frames`）
  - `-fps_mode cfr` 需 ffmpeg 5.0+、`-vsync cfr` 在 ffmpeg 7.0 已移除，两者都不是通用可用。cfrArgs() 按 `ffmpeg -version` 解析主版本号选择拼写，结果 sync.Once 缓存
  - concat demuxer 的相对路径按**列表文件**所在目录解析（不是进程 cwd），写列表前必须 filepath.Abs，否则得到 `data/tmp/vid_x/data/tmp/vid_x/seg.mp4` 这种双前缀路径
  - Agnes API 返回 1280x704(16:9) / 704x1280(9:16)、h264 24fps、aac 音频；偶发「DiffGenerator returned no result」，重试同一请求即可成功；多段生成应对每段重试而不是静默丢弃。真实样本存 /tmp/opencode/concat_probe/
  - PTS 修复只解决播放跳帧，不解决**内容断裂**：每段独立 text 生成会让 Agnes 重新采样人物外貌、服装、场景，接缝处像硬切。解法是分段串联——第 1 段走 `Mode:"text"`，其后每段走 `Mode:"keyframe"` 并把上一段末帧作 `FirstFrame`
  - Agnes 的 `first_frame` 接受 **data URI**（实测 107KB JPEG → 142KB data URI，HTTP 200 且任务正常 completed），末帧用 `ffmpeg -sseof -0.2 -i in -frames:v 1 -q:v 3 out.jpg` 抽取后 base64 即可，无需先上传公网。keyframe 实测：末帧与生成视频首帧平均绝对差 2.63/255、相似度 0.9897、0% 像素差超过 30
  - 陷阱：`ensurePublicURL` 遇到 data URI 或私有地址会**重新调图片 API 生成一张新图**，原帧内容被丢弃。做末帧锚定不能用它，它只适合「用户给的是本地路径/内网地址且必须换公网 URL」的场景
  - 只锚定首帧还不够，`segmentStagePrompt` 对 i>0 追加「严格延续上一段画面：保持同一人物、同一服装、同一场景、同一光影与同一色调」，否则模型仍会重采样
  - 20s 端到端验收（红裙女子/枫树林，[10,10] 两段）：487 帧、**0 负 PTS**、间隔恒为 0.041667s、接缝 9.958333→10.000000→10.041667 完美等距；接缝帧差 MAD **1.26，低于**段内帧差 1.47 / 15.59；红裙主色全片 (134-150,51-63,27-36)、占比 10.4-13.2%，跨接缝 9.8s(149,62,34) / 10.1s(150,61,34) 几乎相同
  - 视频接口是异步 job：POST /api/convert/video/text 立刻返回 `task_id`（**不是 job_id**），用 GET /api/convert/video/task/{task_id} 轮询 status=running/completed。20s 多段任务实测约 3.5 分钟
  - 视频接口必须接受两种表单编码：原代码直接调 `r.ParseMultipartForm` 会拒绝 urlencoded 请求（`curl -d`、脚本），返回 http.ErrNotMultipart 并被误报成「请求体过大」。已抽成 `parseForm(r, maxMem)` 按 Content-Type 分流（multipart → ParseMultipartForm，其余 → ParseForm）。新增端点若用 ParseMultipartForm 记得复用它
  - 验证表单解析是否通路的省钱办法：提交空 prompt 请求，返回「请输入提示词」说明表单已解析成功，返回「请求体过大」说明被 Content-Type 拦掉——不消耗 AI 配额
  - 429 降级的根因是本地 token 桶与服务器窗口不同步：分段间隔只有 5s 且 429 退避 30s/60s 会再次撞进同一限速窗口，重试全烧完才转 EOF。已改为 `agnesInterSegmentDelay=12s`（Agnes 限额 6 请求/分钟）；429 时整窗等待 60s 并 `resetAgnesTokenBucket()` 重新同步；轮询**不**计入桶（PollVideoTask 不取 token）
  - 另一类失败是上游任务 status=failed（日志串 `视频生成失败: upload failed`）：旧 `isTransientVideoErr` 只认限速/超时，不认它 → 段级重试不生效、直接降级。已把 `upload failed`/`EOF` 加入瞬时错误集合，`segmentAttempts=3→4`、新增 `segmentRetryDelay=10*time.Second`（段间重试）；只有瞬时错误才段级重试，耗尽才降级
  - 本地 text 降级原先把 prompt 只当 hash 种子、渲染随机渐变/圆点，与提示词完全无关（这就是「本地合成不达标」）。已重写 `scripts/video.py` 的 `render_text_video` 为动态标题卡：关键词映射场景配色 + 矢量渐变 + 漂移光斑 + 暗角 + 循环噪声 + 进度条，prompt 用文泉驿正黑渲成带圆角暗底的字幕（`render_caption` 一次渲染、逐帧 alpha 合成，字体按 68→30 自适应）；payload 新增 `aspect_ratio`
  - 本地降级原来 40s 要跑 8 分钟：旧路径每帧写 PNG 再交 ffmpeg 读回，PNG 压缩 ~0.3s/帧。已改 `VideoWriter` 原始 RGB 帧经 stdin 直管道进 ffmpeg（rawvideo/rgb24），40s=960 帧降到 35-37s（约 13 倍）；字幕改**逐行揭示**（`render_caption_lines`）+ 顶部「本地合成预览 · AI 服务暂不可用」横幅（`render_label`），定位是「诚实的动态标题卡」而非伪造真实视频。陷阱：`VideoWriter.__exit__` 成功路径若也 kill ffmpeg，输出缺 moov atom 无法播放，须 exc_type is None 时正常 `close()`
  - 配色词表**顺序即优先级**：「秋/枫/落日/火」必须排在「森林/树/绿」之前，否则「枫树林」里的「树」会抢先命中绿色系。加新关键词时注意它是否会被更泛化的词遮蔽
  - 验证画面文字最可靠的手段是 OCR：本机有 `tesseract` 且装了 `chi_sim`。抽帧后先做 `灰度>215` 二值化再 `tesseract img - -l chi_sim --psm 6`，小字误读率低（26 字对 25 字）；用「近白像素占比」判断字幕是否渲染**不可靠**，亮色光斑也会被算进去

[Project Knowledge Summary]
- Date: 2026-09-05
- Context: OCR 表格还原、中文路由与通用纠错（scripts/ocr.py）
- Category: Troubleshooting & Debugging
- Instructions:
  - PP-OCR 乱码根因：解码表布局必须为 `["blank"] + 字典 + [" "]`（blank 在 0 号位、空格在最后一位）；写成 dict + [" ", "?", "blank"] 会让每个汉字偏移 1 并产生 0.97+ 高置信度乱码。rec 输出已是 softmax 概率（各帧和=1），conf 直接取帧内字符概率，不能再套 softmax
  - PP-OCR 模型与字典必须同源：官方模型把字符表写在元数据 custom_metadata_map["character"]，优先从元数据读。本地 rec.onnx（10.8MB）与 ppocr_keys_v1.txt（6623 行）完全对齐：输出 6625 = 1(blank)+6623(字典)+1(空格)，空格位 6624 正常输出。rec_v5.onnx（18385 通道）与 v6_rec.onnx（18710 通道）是多语言词表模型、仓库无对应字典，不可使用
  - 中文识别率两条路已实测：PP-OCR 中文远强于 tesseract（`长对话理解`/`每小题1分`/`三个选项`/`读两遍` 原生全对、conf 0.88-0.98、零修复）；tesseract 这些行全错（`再解`/`1分`/`锭项`/`两允`）靠固定搭配硬修。PP-OCR ch 模型与 lw.PPOCR.OpenCVDNN 的 4.4MB Tiny 同档（13 行同图对比互有胜负，无净提升）；该 repo 的 rec.onnx（6906=1+6904+1）字典配它自己的模型、我们 6625 通道用不了，且要求 OpenCV 5.0+（环境 cv2 4.x）
  - CJK 路由架构：tesseract 主遍保英文/块结构/表格，CJK 占比过半的行在 analyze_page 的 group_blocks 前用该行 gray crop 重跑 PP-OCR rec（`ppocr_rec_line_chars` 逐字符 top-K）+ 通用纠错原位替换。领域无关，不依赖任何固定搭配
  - 通用纠错评分：纯 jieba 词频会把正确的「每小题」误判为 OOV（它是单 token 但 FREQ=0），反而奖励错误词「小器/小腿」（真实词）。正确做法按 token 长度平方奖励连贯长词、词频仅作等长平局、对词典外孤字重罚；经验阈值增益≥4.0 能切分强修复（逸→选 6.6、飓→题 4.7）与误改（题→盟 3.4、话→活 1.1），宁可漏改不可误改
  - 考试固定搭配（EXAM_IDIOMS/repair_exam_phrases）降级为 exam 模式（默认关），叠加在路由结果之后，处理整串糊掉或需 2+ 字同改（`_phrase_diff` 后 `fix==1` 或 `n>=6 且 fix<=3 且 matched*2>=n`）。要求「其余字符完全一致且仅一字不同」、数字位必须原样匹配（否则「共5小题」被改成「每小题」），且要先登记已正确读出的搭配区间（「三个选项/四个选项」彼此只差一字会互相覆盖）
  - image_words 坐标修复（关键 bug）：page_gray 经 _prepare 会把小图（宽<max_width）放大 2-4 倍，词框因此是放大后 gray 的像素坐标，而图像路径 pw=img.width 未回缩 → analyze_page 里 sx=gray.width/pw 再乘一次造成**双重缩放**，路由裁剪与表格补识别全跑偏（小图症状：截断/重复/乱码）。修复：image_words 末尾按 `image.width/gray.shape[1]` 还原回输入图像素坐标。PDF 路径 150/300dpi 不触发放大（fx=1）故行为不变
  - 带边框表格靠墨迹线段还原比词框聚类可靠：tesseract 主遍会整列漏词（本例 3 列表格只剩第 3 列），表格线不依赖文字。检测用形态学开运算 + 连通域取长条（connectedComponentsWithStats 后按 w/h 过滤），按中心坐标 6px 合并同类线段。不要用整页投影覆盖率找表格线（表格只占版面一部分宽度时投影天生达不到整页 50%，会把所有表格线判掉），也不要假设表格四边都有框线（横线端点与竖线中心差 1-2px，须先合并再当同一根线）
  - 脚本里 sx/sy 约定为「每页面单位对应多少像素」，因此像素→页面要**除**、页面→像素要**乘**。换算写反的症状是裁剪框全为空、逐格补识别静默返回空串（fill_cells 生效但输出和没生效一模一样），排查时先打印裁剪框尺寸
  - 表格与相邻行的归属按**行中心**判定，不按包围盒重叠：词框高度能超出真实行框数倍，包围盒再加外扩比例会把表格上方紧贴的说明行一起吞掉，两个不相邻的标题会合并成一个块
  - OCR 偶发 1x1 退化词框会成为独立行，又因框太窄过不了任何重叠判定而残留在输出里；行构造按 `w>=2 and h>=2` 丢弃。行尾标点行（只含标点且 `ln.y <= prev.y + prev.h`）并回上一行
  - 「整串是否只有标点」的正则判断必须用 `fullmatch`：`match` 遇 `+` 只要首字符命中就返回成功，导致 `(__)14.When` 被当标点、后面的真空格全被吃掉，英文行读成 `(__)14.Whendoes`
  - Tesseract 预处理只做灰度+放大（<1200px 宽按 1200 缩放），不要加自适应二值化，二值化明显降低准确率。多语言组合 chi_sim+chi_tra+eng 会把简体读成繁体并混入拉丁乱码，对形态判为中文的行改用 chi_sim 单语言重识别才准；形态判据是连通域 median 宽高比 ≥0.82（中文 0.83-1.00 / 英文 0.59-0.80），墨迹密度不可用
  - 章节罗马序号：tesseract 把竖线序列 III 并成一个字符，必须按连通域计数窄竖条；裁剪右边界取 `x_stop - 2`，多切进 1 像素就把 IV 读成 II 或空；前缀须「笔画互不横向重叠 + 宽笔画填充率 ≤0.32」，否则「听」字切出的笔画被误判成 V。章节标题字号与说明句/选项行接近无法分离，有效判据是「罗马序号 + 句尾 `)`/`）` 括注」，说明句以「。」/「，」/`.` 收尾，选项行（`A. … B. …`）单独排除
  - OCR 慢的根因是 detect_orientation 而非识别本身：它对 0/90/180/270 各跑一次全分辨率 tesseract OSD，单这一项占 47.7s（通用模式总 132.9s），image_words 逐个 PSM 试到 15.8s。提速（已验证）：采样图缩到 max_side=760、按宽高比排序候选方向、单方向平均 conf≥0.6 即收敛；image_words 首个 PSM score≥0.6 且词数≥20 提前返回。改后通用模式 22.2s、方向检测 7.2s。760px 采样图对 90° 旋转仍稳定判对，不能用「分辨率换准确率」的直觉否定它
  - 但「conf≥0.6 即收敛」是量纲 bug：tesseract conf 是 0-100，0.6 阈值几乎永远达标 → landscape 图（候选顺序 90→270→0→180）在 90° 就早停，横向英文文档被误判旋转 90°、整页读成中文乱码（英文审计报告图实测 0°=91.8 / 90°=34.6，却返回 90）。已把 accept_score 默认改 60 并加 `accept_score<=1.0 → 60` 防御，英文图返回 0°、650.pdf 仍返回 90°。这是「英文图片 OCR 不识别/误识别」的根因，与 _refine_latin_lines 无关
  - 英文选项两级后处理（650.pdf 第31题冠词答案）：cluster_lines 里 text 统一走 `finalize_line_text(_join_words(items), conf)`，顺序「_repair_english（词边界 rt's/lt's→It's，始终）→ _repair_option_block（EXAM_OPTION_FIXES 完整选项段混淆，仅 exam+选项块）→ _normalize_options（选项头 A.x→A. x、,，→；、清填空横线、低置信度<0.6 才 0→/ :→；）→ repair_exam_phrases」。_is_option_block 用 `(?:^|\s)[A-D][.．,，]` 计数≥3 判选项块，容忍 C→@、D.→D, 误读；EXAM_OPTION_FIXES 的 key 是含选项头的完整乱码段（A.aia→A.a；a、B./:a→B./；a、@,.n%/→C.a；/、D,//→D./；/），普通英文句子不命中故误伤低；0→/、:→； 只 exam 模式 + conf<0.6 才启用，避免把数学/时间选项合法的 0 与 : 改坏
  - 撇号陷阱（第31题题干「rt's cold...」修不掉的真因）：tesseract/PP-OCR 输出的撇号是**弯撇 ’ (U+2019)** 而非直撇 ' (U+0027)，正则只写直撇 `\brt's\b` 会整体匹配失败，看起来像「修复没启用」。英文词修正的正则里撇号必须用字符类 `['’]` 兼容两种；同理句首小写 it's 大写用 `(^|[.!?]\s+)it['’]s\b`。第31题题干正确输出 `()31.—It's cold and wet day today.`（`—` 是对话引语破折号，非误读，勿删）
  - 免费快速通用 OCR 不追求零错字：正确字不在 PP-OCR top-K（潤/遍/请/据 不在各自 top-6）或模型过度自信（conf≥0.85 被安全闸拦）时通用纠错修不了；10pt 中文单字救不回来（孤立字框在 PSM 6/7/8/10/13 下全读拉丁乱码，整行裁剪放大后 conf 掉到 0-22 全乱），唯一有效兜底是固定搭配修复。英文 c/e 混淆是 PP-OCR ch 模型通病，换模型修不掉
  - 判断 PDF 小字失败是质量还是引擎极限：先看行级 conf 与 size（8pt 是最小档），再统计裁剪图灰度直方图，双峰且 min=0/max=255 说明是干净数字渲染而非退化扫描件；两者都排除后基本可判定为引擎极限
  - PDF 文本层乱码检测（text_layer_is_garbled）不能用 zlib 压缩率或字符频率类指标：非重复中文正文 zlib 比 0.68 与乱码 0.71 几乎相同、二元组唯一性 0.93 也高于乱码 0.68，会误判。有效判据是「私用区字符占比 >1%」或「占比≥5% 的字母体系平均连续片段长度过短且单字片段占比>30%」（正常英文平均 run 长度约 5.5，乱码约 1-2.3）
  - scripts/ocr.py 的 stdout 首行会混入 PyMuPDF fitz 弃用告警，解析其 JSON 输出须从第一个 `{` 开始截取
  - 验证与测试资源：650.pdf（考试，路由+exam 全对）；/tmp/nonexam2.png（竖版非试卷，通用模式全对零误改）；/tmp/ocr_cn.png（中英混排图）；/tmp/ocr_pdf_text.pdf、ocr_pdf_scan.pdf、ocr_multipage.pdf、ocr_table.pdf、ocr_pdf_garbled_layer.pdf；回归脚本 /tmp/t_garbled.py（判据）与 /tmp/test_ocr.py（期望 FAILS: 0）
  - 排查「秒失败」先看服务端日志里的 Python traceback 全文，OCR 与转换接口的 error 字段是脱敏文案（「识别失败，请稍后重试」），不含真实原因

[Project Knowledge Summary]
- Date: 2026-08-25
- Context: FlowConvert 静态资源与前端排查
- Category: Troubleshooting & Debugging
- Instructions:
  - Go embed.FS 的 fs.ReadFile 需要精确路径匹配，不做双前缀拼接。用 `strings.TrimPrefix(path, "/")` 取相对路径后，ReadFile 时**不要**再前置 "static/"。错误模式 `fs.ReadFile(assets, "static/" + name)`（name 已含 static/ 前缀）导致 404；正确写法 `fs.ReadFile(assets, name)`
  - vectorize 输入类型 imageExts = ["jpg","jpeg","png","bmp","tiff","tif","webp","gif"]，不接受 .ai 作为输入
