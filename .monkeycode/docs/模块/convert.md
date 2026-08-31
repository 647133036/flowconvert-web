# convert 模块

## 概述

convert 模块处理文件格式转换任务，包括图像矢量化、PDF 转 Office、素描生成等。

## 端点

### /api/convert/upload

**处理器**: `handle_upload_vectorize`

将图像转换为矢量图形 (SVG/AI/DXF/EPS/FIG/SK/PDF)。

**流程**:
1. 解析 multipart 表单
2. 校验文件魔数（PNG/JPG/GIF/WebP/BMP/TIFF）
3. 调用 Python 脚本 `scripts/vectorize.py`
4. 注册输出文件到 FileStore
5. 返回 JSON `{ success, download_url }`

**错误处理**:
- 空文件 → 400
- 不支持的格式 → 400
- Python 脚本失败 → 500

### /api/convert/url

**处理器**: `handle_url_vectorize`

通过 URL 下载图像并矢量化。

**流程**:
1. 校验 URL 参数
2. 调用 `fetch::fetch_image` 安全下载
3. 执行矢量化处理
4. 返回下载 URL

### /api/convert/pdf-to-office

**处理器**: `handle_pdf_to_office`

PDF 转 DOCX/XLSX。

**流程**:
1. 校验 `%PDF-` 魔数
2. 调用 Python 脚本 `scripts/pdf2docx.py` 或 `scripts/pdf2xlsx.py`
3. 返回下载 URL

### /api/convert/sketch

**处理器**: `handle_sketch`

生成素描效果图片。

**参数**:
- `file`: 输入图像
- `sigma`: 高斯模糊半径 (默认 1.0)
- `reverse`: 是否反转 (0/1)

**流程**:
1. 调用 Python 脚本 `scripts/sketch.py`
2. 返回 PNG 图片

### /api/convert/idphoto

**处理器**: `handle_id_photo`

证件照生成（直接返回图片字节）。

**参数**:
- `file`: 输入图像
- `width`: 输出宽度 (默认 413)
- `height`: 输出高度 (默认 531)

**响应**: `Content-Type: image/png`

## Python 脚本依赖

| 脚本 | 功能 | 输入 | 输出 |
|------|------|------|------|
| `vectorize.py` | 图像矢量化 | 图像 + 格式 | SVG 等矢量文件 |
| `pdf2docx.py` | PDF 转 Word | PDF | DOCX |
| `pdf2xlsx.py` | PDF 转 Excel | PDF | XLSX |
| `sketch.py` | 素描效果 | 图像 + sigma | PNG |
| `idphoto.py` | 证件照 | 图像 | PNG |

## 配置

```rust
pub struct VecParams {
    pub tool: String,        // "potrace" | "autotracer" | "imagetracepp"
    pub brightness: i32,     // 亮度阈值
    pub detail: i32,         // 细节级别
}
```

## 测试

```bash
cargo test handler::convert::tests
```
