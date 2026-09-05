# FlowConvert API 接口文档

## 基础信息

- **Base URL**: `http://localhost:8080`
- **Content-Type**: `application/x-www-form-urlencoded` 或 `multipart/form-data`
- **响应格式**: JSON (除图片直接返回)

---

## 健康检查

### GET /api/formats

返回支持的格式列表。

**响应示例**:
```json
{
  "success": true,
  "image_input": ["jpg", "jpeg", "png", "bmp", "tiff", "webp", "gif"],
  "vector_output": ["svg", "ai", "dxf", "eps", "fig", "sk", "pdf"],
  "pdf_output": ["docx", "xlsx"],
  "max_upload_mb": 50,
  "max_url_mb": 20
}
```

---

## 格式转换

### POST /api/convert/upload

图像矢量化上传接口。

**请求**: `multipart/form-data`
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| file | file | 是 | 输入的图像文件 |
| output | string | 否 | 输出格式 (默认 svg) |

**响应**:
```json
{ "success": true, "download_url": "/api/download/abc123.svg" }
```

### GET/POST /api/convert/url

通过 URL 矢量化的接口。

**查询参数**:
| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| url | string | 是 | 图像 URL (http/https) |
| output | string | 否 | 输出格式 |

### POST /api/convert/pdf-to-office

PDF 转 Office 文档。

**请求**: `multipart/form-data`
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| file | file | 是 | PDF 文件 (魔数校验 `%PDF-`) |
| output | string | 否 | 输出格式 (docx/xlsx) |

**响应**:
```json
{ "success": true, "download_url": "/api/download/xyz789.docx" }
```

### POST /api/convert/sketch

生成素描效果图片。

**请求**: `multipart/form-data`
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| file | file | 是 | 输入图像文件 |
| sigma | float | 否 | 高斯模糊半径 (默认 1.0) |
| reverse | int | 否 | 是否反转 (0/1) |

### POST /api/convert/idphoto

证件照生成（直接返回图片字节，非 JSON）。

**请求**: `multipart/form-data`
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| file | file | 是 | 输入图像文件 |
| width | int | 否 | 输出宽度 (默认 413) |
| height | int | 否 | 输出高度 (默认 531) |

**响应**: 直接返回 PNG 图片 (`Content-Type: image/png`)

---

## AI 图像生成

### POST /api/convert/image/text

文本生成图像。

**请求**: `multipart/form-data` 或 `application/x-www-form-urlencoded`
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| prompt | string | 是 | 生成描述 |
| width | int | 否 | 宽度 (默认 1024) |
| height | int | 否 | 高度 (默认 1024) |

**AI 路径**: 先尝试 Agnes，失败 fallback 到 SenseNova
**响应**:
```json
{ "success": true, "download_url": "/api/download/img_abc123.png" }
```

### POST /api/convert/image/edit

基于源图编辑生成新图像。

**请求**: `multipart/form-data`
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| image | file | 是 | 源图像 |
| prompt | string | 是 | 编辑描述 |
| width | int | 否 | 输出宽度 |
| height | int | 否 | 输出高度 |

### POST /api/convert/image/compose

多图合成生成图像（最多 4 张参考图）。

**请求**: `multipart/form-data`
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| prompt | string | 是 | 合成描述 |
| image[] | file[] | 否 | 参考图 (最多 4 张) |
| width | int | 否 | 输出宽度 |
| height | int | 否 | 输出高度 |

---

## AI 视频生成

### POST /api/convert/video/text

文本生成视频。

**请求**: `application/x-www-form-urlencoded`
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| prompt | string | 是 | 视频描述 (4-500字) |
| duration | int | 否 | 时长 (秒, 1-120, 默认 5) |
| ratio | string | 否 | 宽高比 (16:9/9:16/1:1/4:3/3:4/2:3/3:2) |

**短视频** (duration ≤ 12): 同步处理，直接返回视频路径
**长视频** (duration > 12): 异步任务，分段生成后拼接

**短视频响应**:
```json
{ "success": true, "download_url": "/api/download/vid_abc123.mp4" }
```

**长视频响应**:
```json
{ "task_id": "xxxx", "status": "processing", "message": "视频生成中" }
```

### POST /api/convert/video/keyframe

关键帧生成视频。

**请求**: `multipart/form-data`
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| first_image | file | 是 | 起始帧 |
| last_image | file | 是 | 结束帧 |
| duration | int | 否 | 时长 (秒) |

### POST /api/convert/video/ref

参考图生成视频。

**请求**: `multipart/form-data`
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| prompt | string | 是 | 视频描述 |
| ref_image | file | 是 | 参考图像 |
| duration | int | 否 | 时长 (秒) |

### GET /api/convert/video/task/{id}

查询异步视频任务状态。

**路径参数**:
| 参数 | 类型 | 说明 |
|------|------|------|
| id | string | 任务 ID |

**响应**:
```json
{
  "task_id": "xxxx",
  "status": "completed",
  "download_url": "/api/download/vid_xyz789.mp4",
  "error": null
}
```

**状态值**: `processing` | `completed` | `failed`

---

## 翻译服务

### POST /api/translate

文本翻译。

**请求**: `application/x-www-form-urlencoded`
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| text | string | 是 | 待翻译文本 (最大 5000 字符) |
| source | string | 否 | 源语言 (auto/zh/en/ja 等) |
| target | string | 是 | 目标语言 |

**响应**:
```json
{ "success": true, "translated_text": "..." }
```

### POST /api/translate/file

文件翻译。

**请求**: `multipart/form-data`
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| file | file | 是 | 文本文件 (.txt/.json/.xml) |
| source | string | 否 | 源语言 |
| target | string | 是 | 目标语言 |

**响应**: 直接返回翻译后的文本文件

---

## OCR 文字识别

### 页面

`GET /ocr` — OCR 文字识别页（导航位于「翻译」与「视频生成」之间）。

### POST /api/ocr

识别图片或 PDF 中的文字，可返回多种原版式导出文件与简体中文翻译。

**请求**: `multipart/form-data`（`file` 与 `url` 二选一）
| 字段 | 类型 | 必填 | 默认 | 说明 |
|------|------|------|------|------|
| file | file | 二选一 | - | 图片或 PDF（jpg/jpeg/png/bmp/tiff/tif/webp/gif/pdf，≤50MB）|
| url | string | 二选一 | - | 公网可访问的图片或 PDF 直链，≤50MB，最多 5 次重定向 |
| mode | string | 否 | `general` | `general` / `advanced`（多 PSM 取最优置信度）|
| quality | string | 否 | `normal` | `normal`（150 DPI）/ `high`（300 DPI）|
| charset | string | 否 | `auto` | `auto` / `zh_hans` / `zh_hant` |
| engine | string | 否 | `auto` | `auto` / `tesseract` / `pp_ocr`；auto 优先 Tesseract，不可用时回退 PP-OCR，engine=pp_ocr 则反向回退 Tesseract |
| redbox | bool | 否 | `false` | 输出文字位置红框标记图 |
| optimize | bool | 否 | `false` | 过滤页眉页脚等噪声文本 |
| translate | bool | 否 | `false` | 额外返回简体中文翻译 |
| formats | string[] | 否 | `txt` | `txt` / `docx` / `xlsx` / `json`，逗号分隔或重复字段 |

校验规则：
- 上传文件扩展名须在白名单内，且 `http.DetectContentType` 嗅探结果需与该扩展名注册的 MIME 一致，否则返回 400
- 链接经 `service.FetchFile` 校验：仅 http/https、禁止内网与保留地址、魔数嗅探扩展名、白名单校验
- `mode` / `quality` / `charset` / `engine` 取值非法返回 400

**响应**:
```json
{
  "success": true,
  "text": "识别到的完整文本",
  "translated_text": "简体中文翻译（translate=1 时）",
  "downloads": [
    {"name": "识别结果.txt", "type": "txt", "url": "/api/download/<id>"}
  ],
  "engine": "tesseract | pp_ocr | pdftext",
  "pages": 1,
  "chars": 123,
  "charset": "zh_hans",
  "from_text_layer": false,
  "layout": {"columns": 1, "tables": 0, "blocks": 5, "lines": 12},
  "blocks": [],
  "lines": [],
  "filtered_headers": ["过滤掉的页眉页脚文本"],
  "warnings": ["非致命告警"],
  "elapsed_ms": 1234,
  "options": {"mode": "general", "quality": "normal", "charset": "auto", "redbox": false, "optimize": false, "translate": false, "formats": ["txt"], "engine": "auto"},
  "source": "file | url",
  "ext": "png",
  "empty": false
}
```

- PDF 优先抽取内嵌文本层（`engine=pdftext`、`from_text_layer=true`），字符级 bbox 无损，无文本层才走图像 OCR
- 文本层乱码检测：子集字体缺少 ToUnicode 映射时，PDF 文本层会抽出乱码。判定条件为「私用区字符占比 > 1%」或「所有占比 ≥5% 的字母体系（拉丁/中日韩）平均连续片段长度过短且单字片段占比 > 30%」；命中则回退图像 OCR 并在 `warnings` 中提示
- 识别无结果时 `empty=true`、`text=""`、`lines=[]`，仍返回 200；`formats` 指定的导出文件照常生成（内容为空），供前端展示空结果提示
- 下载文件经 `FileStore.Register` 复制到输出目录，`/api/download/<id>` 带 TTL 自动清理
- 后端实现：`internal/handler/ocr.go` → `internal/service/ocr.go` → `scripts/ocr.py`（Tesseract 与 PP-OCR ONNX 双引擎互为回退，PP-OCR 用 models/ocr 下官方配套 det/rec 模型）
- 脚本超时：标准质量 300s、高清 540s，开启翻译再 +180s

---

## 文件下载

### GET /api/download/{name}

下载已生成的文件。

**路径参数**:
| 参数 | 类型 | 说明 |
|------|------|------|
| name | string | 文件标识 (仅允许字母数字下划线) |

**安全特性**:
- 路径遍历防护
- TTL 自动清理 (默认由配置决定)

---

## 静态页面

以下路径返回对应的 HTML 页面：

| 路径 | 页面 |
|------|------|
| `/` | 首页 (index.html) |
| `/about` | 关于页面 (about.html) |
| `/image` | 图像生成页 (image.html) |
| `/video` | 视频生成页 (video.html) |
| `/translate` | 翻译页 (translate.html) |
| `/donate` | 捐赠页 (donate.html) |
| `/embed` | 嵌入页 (embed.html) |

---

## 错误响应

所有 API 在错误时返回 JSON:
```json
{ "success": false, "error": "错误描述" }
```

常见 HTTP 状态码：
- `200`: 成功
- `400`: 参数无效或文件不符合要求
- `404`: 资源不存在
- `429`: 请求过于频繁
- `503`: 服务器繁忙
