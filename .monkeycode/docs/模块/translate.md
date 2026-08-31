# translate 模块

## 概述

translate 模块提供文本和文件的翻译功能，支持 100+ 语言。

## 端点

### POST /api/translate

**处理器**: `handle_translate`

文本翻译接口。

**请求**: `application/x-www-form-urlencoded`
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| text | string | 是 | 待翻译文本 (最大 5000 字符) |
| source | string | 否 | 源语言 (auto/zh/en/ja/ko/... ) |
| target | string | 是 | 目标语言 |

**响应**:
```json
{ "success": true, "translated_text": "..." }
```

### POST /api/translate/file

**处理器**: `handle_translate_file`

文件翻译接口。

**请求**: `multipart/form-data`
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| file | file | 是 | 文本文件 (.txt/.json/.xml) |
| source | string | 否 | 源语言 |
| target | string | 是 | 目标语言 |

**响应**: 直接返回翻译后的文本

## 服务层

### translate_text

```rust
pub fn translate_text(text: &str, source: &str, target: &str) -> Result<String, String>
```

调用 Python 脚本 `scripts/translate.py` 执行翻译。

### translate_file

```rust
pub fn translate_file(file_path: &str, source: &str, target: &str) -> Result<Vec<u8>, String>
```

读取文件内容，调用 translate_text，返回翻译后的字节。

## Python 脚本

`scripts/translate.py` 支持以下参数：
- `--text`: 待翻译文本
- `--source`: 源语言代码
- `--target`: 目标语言代码
- `--file`: 输入文件路径

## 支持的语言

| 代码 | 语言 | 代码 | 语言 |
|------|------|------|------|
| zh | 中文 | en | 英语 |
| ja | 日语 | ko | 韩语 |
| fr | 法语 | de | 德语 |
| es | 西班牙语 | ru | 俄语 |
| pt | 葡萄牙语 | it | 意大利语 |
| ar | 阿拉伯语 | hi | 印地语 |
| ... | (共 100+ 种) | | |

## 限制

- 文本最大长度：5000 字符
- 文件最大大小：由全局 max_size 配置决定
- 源语言 auto：自动检测

## 测试

```bash
cargo test service::translate::tests
cargo test handler::translate::tests
```
