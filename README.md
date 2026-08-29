# FlowConvert

[![Go Version](https://img.shields.io/badge/go-1.25-blue.svg)](https://golang.org/)
[![License](https://img.shields.io/badge/license-Apache--3.0-green.svg)](./LICENSE)
[![Version](https://img.shields.io/badge/version-v0.1.2-brightgreen.svg)](#)

一个基于 Go 的轻量级文档与媒体转换服务，支持图片转矢量、PDF 转 Office、证件照制作、多语言翻译以及 AI 视频/图像生成。**内置 OCR 引擎，支持扫描版/照片式 PDF 的文字识别。**

## 功能特性

| 功能 | 接口 | 说明 |
|------|------|------|
| 图片 → 矢量图 | `POST /api/convert/upload` | 支持 JPG/PNG/BMP/TIFF/WebP/GIF，输出 SVG/AI/DXF/EPS |
| PDF → Office | `POST /api/convert/pdf-to-office` | PDF 转 Word/Excel，**内置 Tesseract OCR 识别扫描版/照片式 PDF** |
| 素描效果 | `POST /api/convert/sketch` | 图片转素描风格 |
| 证件照 | `POST /api/convert/idphoto` | 一寸/二寸，支持白/蓝/红背景 |
| 文本翻译 | `POST /api/translate` | 自动检测源语言，多引擎 fallback（Google/DeepL/TranslateCom），无需 API Key |
| 文件翻译 | `POST /api/translate/file` | 文档翻译下载（支持 PDF/Word/Excel/PPT，含 OCR）|
| AI 文生图 | `POST /api/convert/image/text` | 文本生成图像 |
| AI 视频生成 | `POST /api/convert/video/text` | 文本生成视频（最长 60s）|
| AI 视频（首尾帧）| `POST /api/convert/video/keyframe` | 首尾帧控制视频生成 |
| AI 视频（参考图）| `POST /api/convert/video/ref` | 多张参考图生成视频 |
| 下载管理 | `GET /api/download/{name}` | 文件下载与 TTL 自动清理 |

### OCR 说明

PDF 转 Word/Excel 时，若 PDF 无文本层（扫描件、照片式 PDF），自动启用 OCR 识别：

- **引擎**：Tesseract OCR（通过 `pytesseract` 调用）
- **语言**：`chi_sim+eng`（简体中文 + 英文）
- **渲染**：使用 PyMuPDF 将 PDF 页面渲染为图片，再以 300 DPI 送入 Tesseract 识别
- **翻译场景**：文件翻译同样支持 OCR，扫描版文档可完整识别后翻译

如需调整 OCR 语言或质量，可在脚本中修改 `ocr_pdf()` 的 `lang` 参数。

## 快速开始

### 环境要求

- Go 1.25+
- Python 3.8+
- **Tesseract OCR 引擎**（系统级安装，见下方）
- Go 依赖：仅标准库
- Python 依赖：`pdfminer.six`、`python-docx`、`openpyxl`、`Pillow`、`pymupdf`、`pytesseract`、`pdf2image`、`translatepy`、`beautifulsoup4`、`python-pptx`、`reportlab`、`VTracer` 或 `potrace`

### 安装 Tesseract OCR

```bash
# Ubuntu / Debian
sudo apt-get install -y tesseract-ocr tesseract-ocr-chi-sim tesseract-ocr-eng

# macOS
brew install tesseract tesseract-lang

# Windows
# 从 https://github.com/UB-Mannheim/tesseract/wiki 下载安装包，勾选中文语言包
```

验证安装：

```bash
tesseract --version
tesseract --list-langs  # 应包含 chi_sim + eng
```

### 安装 Python 依赖

```bash
pip install pdfminer.six python-docx openpyxl Pillow pymupdf pytesseract pdf2image translatepy beautifulsoup4 python-pptx reportlab
```

或创建 `requirements.txt` 后安装：

```bash
pip install -r requirements.txt
```

### 编译运行

```bash
# 克隆仓库
git clone https://github.com/your-org/flowconvert.git
cd flowconvert

# 编译并运行
go build -o flowConvert .
./flowConvert
```

### 配置

通过环境变量或 `.env` 文件配置：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `FLOWCONVERT_PORT` | `8080` | 服务监听端口 |
| `FLOWCONVERT_DATA` | `data` | 数据目录（tmp/output 在此下）|
| `FLOWCONVERT_BASE_URL` | `http://localhost:8080` | 公网访问地址（用于生成下载链接）|
| `AGNES_API_KEY` | - | Agnes AI 图像/视频生成 API Key |
| `AGNES_BASE_URL` | `https://apihub.agnes-ai.cn/v1` | Agnes API 端点 |
| `SENSENOVA_API_KEY` | - | SenseNova 备用图像生成 API Key |
| `FLOWCONVERT_PYTHON` | `python3` | Python 解释器路径 |

示例 `.env`：

```bash
AGNES_API_KEY=your-agnes-api-key-here
FLOWCONVERT_BASE_URL=https://your-domain.com
```

### Docker 部署

```bash
docker build -t flowconvert .
docker run -p 8080:8080 \
  -e AGNES_API_KEY=your-key \
  -e FLOWCONVERT_BASE_URL=https://your-domain.com \
  flowconvert
```

## 项目结构

```
flowconvert/
├── main.go                 # 入口与路由注册
├── middleware.go           # CORS + 限流 + 安全头中间件
├── go.mod                  # Go 模块定义
├── internal/
│   ├── config/             # 配置加载
│   ├── handler/            # HTTP 处理器（上传/转换/下载）
│   └── service/            # 业务逻辑（AI 客户端、脚本调用、SSRF 防护）
├── scripts/                # Python 转换脚本
│   ├── vectorize.py        # 图片转矢量（VTracer/Potrace）
│   ├── pdf2docx.py         # PDF 转 Word（含 OCR 支持）
│   ├── pdf2xlsx.py         # PDF 转 Excel
│   ├── sketch.py           # 素描效果
│   ├── idphoto.py          # 证件照生成
│   ├── translate.py        # 文本翻译（含 OCR 支持）
│   └── video.py            # AI 视频生成
└── web/                    # 前端页面与静态资源
    ├── index.html          # 首页
    ├── video.html          # 视频生成页
    ├── image.html          # 图像生成页
    └── ...
```

## API 示例

### 文本翻译

```bash
# 自动检测源语言，翻译成中文
curl -X POST http://localhost:8080/api/translate \
  -H "Content-Type: application/json" \
  -d '{"text":"Hello world","source":"auto","target":"zh"}'
```

响应：

```json
{
  "success": true,
  "translated_text": "你好世界",
  "detected_language": "en",
  "engine": "translatepy"
}
```

**翻译引擎**：基于 `translatepy`，自动按优先级尝试 Google → DeepL → LibreTranslate → TranslateCom → MyMemory。无需配置任何 API Key，首次请求时自动选路。支持 16 种语言（中/英/日/韩/法/德/西/葡/俄/阿/泰/越/意/荷/波/土耳其语）。

### 图片转矢量

```bash
curl -X POST http://localhost:8080/api/convert/upload \
  -F "file=@photo.jpg" \
  -F "output_format=svg"
```

响应：

```json
{
  "success": true,
  "download_url": "/api/download/1700000000000_a1b2c3d4_converted.svg"
}
```

### PDF 转 Word（含 OCR）

```bash
curl -X POST http://localhost:8080/api/convert/pdf-to-office \
  -F "file=@scan.pdf" \
  -F "output_format=docx"
```

扫描版 PDF 会自动触发 OCR 识别，输出带文字的 Word 文档。

### 文本生成视频

```bash
curl -X POST http://localhost:8080/api/convert/video/text \
  -F "prompt=a cat walking" \
  -F "duration=10" \
  -F "aspect_ratio=16:9"
```

响应：

```json
{
  "success": true,
  "task_id": "abc123..."
}
```

轮询状态：

```bash
curl http://localhost:8080/api/convert/video/task/abc123...
```

## 安全特性

- **SSRF 防护**：`FetchImage` 通过 DNS 解析校验 + 建连瞬间 IP 白名单；`DownloadImage/DownloadVideo` 拒绝内网/回环地址
- **上传校验**：文件扩展名与 MIME 双重匹配，`ParseMultipartForm` 限制请求体大小，拒绝伪装类型
- **文件大小限制**：上传默认 50MB，下载图片 100MB / 视频 500MB（`io.LimitReader`）
- **路径穿越防护**：文件名净化 + ServeMux 路径规范化
- **限流中间件**：每 IP 滑动窗口，bucket 上限 10000 自动清理，XFF 链仅信任有效公网 IP
- **并发控制**：视频生成任务最大 6 个并发，超出返回 503
- **错误信息脱敏**：内部错误仅记录 stderr，客户端收到通用提示
- **输入校验**：输出格式白名单、提示词 2000 字符上限、数值参数范围校验、JSON body 大小限制
- **安全响应头**：`X-Content-Type-Options: nosniff`、`X-Frame-Options: DENY`、`X-XSS-Protection: 1; mode=block`、`Content-Security-Policy`

## 开发

### 运行测试

```bash
# 全部测试
go test ./...

# 单测
go test ./internal/handler -v -run TestFileStore

# 系统测试
bash /tmp/opencode/flowconvert-systest/run.sh
```

### 本地开发

```bash
# 启动服务
go run .

# 或带调试输出
LOG_LEVEL=debug go run .
```

## 版本历史

- **v0.1.2** (2026-08) 代码审查修复
  - 视频生成参数：JSON 序列化改用 json.Marshal，杜绝引号/换行/控制字符导致的 payload 注入
  - 视频时长上限：60s → 120s，与前端滑杆一致（AI 路径自动分段）
  - API 请求体：新增 64MB BodyLimit，防止超大请求体缓冲耗尽磁盘
  - ffprobe 探测：增加 30 秒超时，避免挂起
  - ffmpeg 编码：增加 300 秒超时，超时返回明确错误
  - 中文字体探测：os.popen 改为 subprocess.run（超时 5 秒）
  - 会话 TTL：1 小时 → 2 小时
  - 前端链接安全：补全 rel=noopener noreferrer
  - 新增单元测试：payload JSON 序列化（引号/换行/控制字符/unicode 回环）、BodyLimit 中间件

- **v0.1.1** (2026-08) 安全加固与并发控制
  - 限流器：bucket 上限 10000，自动清理过期条目，XFF 信任逻辑修复
  - 视频生成：最大 6 个并发任务（信号量控制）
  - 错误信息脱敏：内部错误仅记录 stderr，不返回客户端
  - SSRF 防护：DownloadImage/DownloadVideo 拒绝内网/回环地址
  - 输出格式白名单：矢量/PDF 转换参数按白名单校验
  - 提示词长度上限：所有 handler 限制 2000 字符
  - 素描 sigma 范围：限定 0.5–10
  - 翻译请求体：限制 1MB
  - 安全响应头：新增 Content-Security-Policy
  - 前端 XSS 修复：file.name 通过 textContent 插入
  - 前端链接安全：外部链接添加 rel=noopener

- **v0.1.0** (2026-08) 初始版本
  - 基础格式转换（图片→矢量、PDF→Office）
  - **OCR 支持**：Tesseract 识别扫描版/照片式 PDF
  - 证件照与翻译功能
  - **多引擎翻译**：translatepy 自动切换 Google/DeepL/TranslateCom/MyMemory，无需 API Key，自动检测源语言
  - AI 视频/图像生成集成
  - 安全审查与测试覆盖

## 贡献

欢迎提交 Issue 和 Pull Request。

1. Fork 本仓库
2. 创建特性分支 (`git checkout -b feat/xxx`)
3. 提交更改 (`git commit -m 'feat: add xxx'`)
4. 推送到分支 (`git push origin feat/xxx`)
5. 创建 Pull Request

## 许可证

本项目采用 [Apache License 3.0](./LICENSE) 开源协议。

---

**注意**：AI 视频/图像生成功能需要配置有效的 API Key。OCR 功能需要系统级安装 Tesseract 及中文字体包。翻译功能无需任何 Key，自动使用免费引擎。免费额度有限，请合理使用。
