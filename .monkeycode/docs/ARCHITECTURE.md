# FlowConvert 系统架构

## 概述

FlowConvert 是一个基于 Rust 的高性能 Web 服务，作为 Go 原版 FlowConvert 的 Rust 重写版本。它提供文件转换、AI 图像生成、AI 视频生成和翻译等功能，采用前后端分离架构，后端服务端口默认为 8080。

系统定位为轻量级文件处理与 AI 内容生成的 API 网关，核心设计理念包括：
- 安全优先：严格的 SSRF 防护和路径遍历防护
- 高性能：异步 I/O 模型，多进程支持
- 可扩展：模块化服务设计，支持添加新的 AI 引擎
- 兼容性：保持与 Go 原版一致的 API 接口

## 技术架构

### 整体架构

```
                     ┌──────────────┐
                     │   Web 浏览器  │
                     └──────┬───────┘
                            │ HTTP/HTTPS
                     ┌──────▼───────┐
                     │   Nginx/CDN  │ (可选反向代理)
                     └──────┬───────┘
                            │
                     ┌──────▼───────┐
                     │  FlowConvert │
                     │  (Axum HTTP) │
                     └──────┬───────┘
                            │
          ┌─────────────────┼─────────────────┐
          │                 │                 │
   ┌──────▼──────┐  ┌──────▼──────┐  ┌──────▼──────┐
   │  文件转换    │  │   AI 生成    │  │   翻译服务   │
   │ ConvertSvc  │  │  ImageGen   │  │ Translate   │
   └──────┬──────┘  └──────┬──────┘  └──────┬──────┘
          │                │                │
   ┌──────▼────────────────▼────────────────▼──────┐
   │              Python 脚本层                      │
   │  vectorize.py, sketch.py, translate.py 等      │
   └───────────────────────────────────────────────┘
          │
   ┌──────▼──────┐
   │  FileStore  │
   │ (磁盘持久化) │
   └─────────────┘
```

### 分层设计

**第一层：HTTP 路由层 (handler/)**

Axum Router 根据 URL 路径分发请求到对应的处理器。所有处理器共享 `AppState`，包含配置、文件存储和 AI 客户端。

```rust
pub struct AppState {
    pub config: Arc<Config>,
    pub file_store: Arc<FileStore>,
    pub video_jobs: Arc<VideoJobStore>,
    pub client: Option<Arc<AIClient>>,
}
```

**第二层：业务逻辑层 (service/)**

服务层实现核心业务逻辑，与 HTTP 协议解耦：
- `aiclient.rs`：封装 Agnes 和 SenseNova AI API 调用
- `fetch.rs`：安全文件下载，包含 SSRF 防护
- `imagegen.rs`：图像处理与 AI 生成逻辑
- `videogen.rs`：视频生成与分段处理逻辑

**第三层：基础设施层**

- `store.rs`：FileStore 和 VideoJobStore 实现磁盘持久化
- `util.rs`：通用工具函数（命令执行、ID 生成、扩展名校验）
- `middleware.rs`：速率限制、安全头注入

**第四层：脚本层 (scripts/)**

Python 脚本提供重型计算任务（矢量化、PDF 转换、翻译），Rust 通过子进程调用。

## 关键组件

### AIClient (AI 客户端)

双引擎架构，支持 Agnes 和 SenseNova 两个 AI 提供商：

```rust
pub struct AIClient {
    agnes_base_url: String,
    agnes_api_key: String,
    sensenova_base: String,
    sensenova_key: String,
    http: Client,
    video_jobs: Arc<VideoJobStore>,
}
```

- Agnes 用于图像生成（text/edit/compose）和视频生成
- SenseNova 作为 Agnes 失败时的 fallback
- 长视频任务使用异步轮询模式（PollVideoTask）

### Fetch 服务 (安全下载)

fetch.rs 实现了多层 SSRF 防护：

1. **DNS 预解析**：在发起 HTTP 请求前解析所有 DNS 记录
2. **IP 白名单检查**：拒绝私有地址、回环地址、链路本地地址
3. **CGNAT 防护**：拒绝 100.64.0.0/10 和 192.0.0.0/24
4. **IPv4-mapped IPv6**：拒绝 `::ffff:x.x.x.x` 格式
5. **重绑定检测**：验证最终连接的 IP 在预解析集合中
6. **流式下载**：带大小限制的分块读取

### FileStore (文件存储)

基于磁盘的临时文件管理系统：

```rust
pub struct FileStore {
    base_dir: PathBuf,
    ttl_hours: u64,
}
```

- `register(src_path, base_name)` → 返回下载路径名
- 自动 TTL 清理（后台线程定期执行）
- 防止路径遍历：只允许字母数字和下划线

### VideoJobStore (视频任务存储)

用于异步视频生成任务的内存存储：

```rust
pub struct VideoJobStore {
    jobs: Mutex<HashMap<String, VideoJob>>,
    ttl_minutes: u64,
}
```

支持操作：create、get、set_status、set_error、acquire、release、gc

## 数据流

### 图像转换流程

```
客户端 → POST /api/convert/upload
       → handler::convert::handle_upload_vectorize
       → service::vectorize::vectorize (Python subprocess)
       → FileStore.register()
       → 返回 JSON { success, download_url }
```

### AI 图像生成流程

```
客户端 → POST /api/convert/image/text
       → handler::imagegen::handle_text_image
       → Multipart 解析 (兼容 FormData)
       → service::imagegen::make_image_ai (AI 路径)
         ├─ Agnes API 调用 (gen_image_agnes)
         └─ fallback: SenseNova API (gen_image_sense_nova)
       → 失败时 fallback 到 procedural 生成
       → FileStore.register()
       → 返回 JSON { success, download_url }
```

### 长视频生成流程

```
客户端 → POST /api/convert/video/text?duration=60
       → handler::videogen::handle_text_video
       → duration > 12 → 长视频路径
       → split_duration(60) = [12, 12, 12, 12, 12]
       → 每段并行生成 (generate_video_segment)
         ├─ make_text_video_ai (Agnes → SenseNova fallback)
         └─ 失败重试 (segmentAttempts=3)
       → concat_videos (ffmpeg concat demuxer)
       → 创建异步任务 (VideoJobStore)
       → 返回 { task_id, status: "processing" }
       → 客户端轮询 GET /api/convert/video/task/{id}
```

## 安全设计

详见 [安全设计](./专有概念/安全设计.md)

## 配置管理

详见 [配置管理](./专有概念/配置管理.md)

## 部署架构

### 单机部署

```bash
# 构建
cargo build --release

# 运行
FLOWCONVERT_DATA_DIR=/data ./target/release/flowconvert
```

### Docker 部署

```dockerfile
FROM rust:1.79 as builder
WORKDIR /app
COPY . .
RUN cargo build --release

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y python3 ffmpeg
COPY --from=builder /app/target/release/flowconvert /usr/local/bin/
COPY --from=builder /app/scripts /app/scripts
CMD ["flowconvert"]
```

### 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `FLOWCONVERT_DATA_DIR` | `./data` | 数据存储目录 |
| `FLOWCONVERT_PYTHON` | `python3` | Python 解释器路径 |
| `FLOWCONVERT_PORT` | `8080` | 服务监听端口 |
| `FLOWCONVERT_AGNES_API_KEY` | 空 | Agnes API 密钥 |
| `FLOWCONVERT_AGNES_BASE_URL` | 空 | Agnes API 端点 |
| `FLOWCONVERT_SENSENOVA_KEY` | 空 | SenseNova API 密钥 |
| `FLOWCONVERT_SENSENOVA_BASE` | 空 | SenseNova API 端点 |
