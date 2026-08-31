# imagegen 模块

## 概述

imagegen 模块提供 AI 图像生成功能，包括文本生成、图像编辑、多图合成。

## 端点

### POST /api/convert/image/text

**处理器**: `handle_text_image`

从文本描述生成图像。

**请求** (multipart 或 urlencoded):
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| prompt | string | 是 | 生成描述 (最大 500 字符) |
| width | int | 否 | 宽度 (默认 1024, 钳位到 256-4096) |
| height | int | 否 | 高度 (默认 1024) |

**流程**:
1. 解析请求参数
2. 尝试 AI 路径：`service::imagegen::make_image_ai`
   - Agnes API → SenseNova fallback
3. AI 失败则 fallback 到 procedural 生成
4. 注册文件到 FileStore
5. 返回 JSON `{ success, download_url }`

### POST /api/convert/image/edit

**处理器**: `handle_edit_image`

基于源图进行 AI 编辑。

**请求**:
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| image | file | 是 | 源图像 |
| prompt | string | 是 | 编辑指令 |
| width | int | 否 | 输出宽度 |
| height | int | 否 | 输出高度 |

### POST /api/convert/image/compose

**处理器**: `handle_compose_image`

多图合成生成图像（最多 4 张参考图）。

**请求**:
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| prompt | string | 是 | 合成描述 |
| image[] | file[] | 否 | 参考图 (最多 4 张) |
| width | int | 否 | 输出宽度 |
| height | int | 否 | 输出高度 |

## 服务层

### make_image_ai

```rust
pub async fn make_image_ai(
    client: &AIClient,
    tmp_dir: &str,
    prompt: &str,
    width: i32,
    height: i32,
) -> Result<String, String>
```

流程：
1. 计算 size tier (small/medium/large)
2. 计算 ratio (如 "16:9")
3. 尝试 Agnes: `gen_image_agnes("agnes-image-2.1-flash", ...)`
4. Fallback 到 SenseNova: `gen_image_sense_nova("sensenova-u1.5-lite", ...)`
5. 下载图片到 tmp_dir/generated.png

### make_edited_image_ai

类似 make_image_ai，但传入源图的 base64 data URI 作为参考。

### make_compose_image_ai

接收多个参考图的 data URI，调用 Agnes/SenseNova 合成。

### procedural 生成 (fallback)

当 AI 不可用时，使用 `image` crate 在内存中生成简单图像：
- 纯色背景 + 渐变
- 几何图形叠加
- 文字水印

## 尺寸约束

```rust
pub const MIN_IMAGE_SIZE: i32 = 256;
pub const MAX_IMAGE_SIZE: i32 = 4096;
```

无效的宽高会被钳位到有效范围。

## 测试

```bash
cargo test service::imagegen::tests
cargo test handler::imagegen::tests
```
