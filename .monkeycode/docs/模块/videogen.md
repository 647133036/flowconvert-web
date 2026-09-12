# videogen 模块

## 概述

videogen 模块提供 AI 视频生成功能，支持文本生成、关键帧插值、参考图驱动三种模式。

## 端点

### POST /api/convert/video/text

**处理器**: `handle_text_video`

从文本描述生成视频。

**请求**:
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| prompt | string | 是 | 视频描述 (4-500 字符) |
| duration | int | 否 | 时长 (1-120秒, 默认 5) |
| ratio | string | 否 | 宽高比 (16:9/9:16/1:1/4:3/3:4/2:3/3:2) |

**短视频 vs 长视频**:
- `duration <= 12`: 同步处理，直接返回结果
- `duration > 12`: 异步任务，分段生成后拼接

### POST /api/convert/video/keyframe

**处理器**: `handle_keyframe_video`

基于首尾帧生成过渡视频。

**请求**:
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| first_image | file | 是 | 起始帧 |
| last_image | file | 是 | 结束帧 |
| duration | int | 否 | 时长 |

### POST /api/convert/video/ref

**处理器**: `handle_ref_video`

基于参考图生成视频。

**请求**:
| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| prompt | string | 是 | 视频描述 |
| ref_image | file | 是 | 参考图像 |
| duration | int | 否 | 时长 |

### GET /api/convert/video/task/{id}

**处理器**: `handle_video_task_status`

查询异步视频任务状态。

**响应**:
```json
{
  "task_id": "xxxx",
  "status": "completed",
  "download_url": "/api/download/vid_xxx.mp4",
  "error": null
}
```

## 服务层

### 短视频处理

```rust
pub async fn make_text_video_ai(
    client: &AIClient,
    tmp_dir: &str,
    prompt: &str,
    duration: i32,
) -> Result<String, String>
```

### 长视频处理

```rust
pub async fn make_long_text_video_ai(
    client: &AIClient,
    tmp_dir: &str,
    prompt: &str,
    total_duration: i32,
    aspect_ratio: &str,
) -> Result<String, String>
```

**流程**:
1. `clamp_seconds(total_duration)` → 钳位到 4-12
2. `split_duration(total)` → 分段数组 [12, 12, 10, ...]
3. `split_prompt_clauses(prompt)` → 提示词分句
4. 每段调用 `generate_video_segment()`:
   - `segment_stage_prompt(prompt, i, n)` → 添加阶段标签
   - Agnes/SenseNova API 调用
   - 失败重试 (segmentAttempts=3)
5. `concat_videos(tmp_dir, seg_paths, dest)` → ffmpeg concat
6. 部分成功也返回结果

### 关键帧视频

```rust
pub async fn make_keyframe_video_ai(
    client: &AIClient,
    tmp_dir: &str,
    first_frame_url: &str,
    last_frame_url: &str,
    duration: i32,
) -> Result<String, String>
```

### 参考图视频

```rust
pub async fn make_ref_video_ai(
    client: &AIClient,
    tmp_dir: &str,
    prompt: &str,
    ref_url: &str,
    duration: i32,
) -> Result<String, String>
```

## 工具函数

### clamp_seconds

```rust
pub fn clamp_seconds(d: i32) -> String
```

将时长钳位到 4-12 秒范围（AI 服务的限制）。

### split_duration

```rust
pub fn split_duration(total: i32) -> Vec<i32>
```

将总时长分配为多个 4-12 秒的片段：
- `total <= 4` → `[4]`
- `total <= 12` → `[total]`
- `total > 12` → 均匀分段

示例：`split_duration(25)` → `[9, 8, 8]` (3段，每段平均约 8.3 秒)

### split_prompt_clauses

```rust
pub fn split_prompt_clauses(prompt: &str) -> Vec<String>
```

按中英文标点分割提示词为子句列表。

### segment_stage_prompt

```rust
pub fn segment_stage_prompt(prompt: &str, i: usize, n: usize) -> String
```

为分段添加阶段标签：
- 第 0 段 → "故事开端"
- 中间段 → "第N阶段"
- 最后段 → "故事结尾"

### probe_resolution

```rust
pub fn probe_resolution(path: &str) -> Result<(i32, i32), String>
```

使用 ffprobe 获取视频分辨率。

### concat_videos

```rust
pub fn concat_videos(tmp_dir: &str, seg_paths: &[String], dest: &str) -> Result<String, String>
```

使用 ffmpeg concat demuxer 拼接视频片段。

### generate_video_segment

```rust
pub async fn generate_video_segment(
    client: &AIClient,
    tmp_dir: &str,
    prompt: &str,
    duration: i32,
    aspect_ratio: &str,
    attempt: u32,
) -> Option<String>
```

生成单个视频片段，包含重试逻辑和瞬时错误检测。

### is_transient_video_err

```rust
pub fn is_transient_video_err(err: &str) -> bool
```

检测是否为可重试的瞬时错误（如 429、503、rate_limit 等）。

## 宽高比常量

```rust
pub const ASPECT_RATIOS: &[&str] = &[
    "16:9", "9:16", "1:1", "4:3", "3:4", "2:3", "3:2",
];
```

## 测试

```bash
cargo test service::videogen::tests
cargo test handler::videogen::tests
```
