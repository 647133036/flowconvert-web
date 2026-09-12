# FlowConvert 开发者指南

## 开发环境搭建

### 系统要求

- Rust 1.79+ (edition 2021)
- Python 3.8+ (用于脚本层)
- ffmpeg (用于视频处理)
- Linux amd64 / macOS arm64

### 依赖安装

```bash
# Ubuntu/Debian
apt-get install -y python3 python3-pip ffmpeg

# macOS
brew install python3 ffmpeg
```

### 构建

```bash
# Debug 构建
cargo build

# Release 构建
cargo build --release

# 运行测试
cargo test

# 运行所有测试 (含集成测试)
cargo test --all
```

### 运行

```bash
# 基本启动
./target/release/flowconvert

# 指定配置
FLOWCONVERT_DATA_DIR=/data FLOWCONVERT_PORT=9000 ./target/release/flowconvert

# 配置 AI 服务
FLOWCONVERT_AGNES_API_KEY=xxx FLOWCONVERT_AGNES_BASE_URL=https://api.example.com \
FLOWCONVERT_SENSENOVA_KEY=xxx FLOWCONVERT_SENSENOVA_BASE=https://sensenova.example.com \
./target/release/flowconvert
```

## 代码结构

### 模块说明

```
src/
├── main.rs           # 入口点 (仅调用 main_inner)
├── lib.rs            # 库入口，路由注册，AppState 定义
├── config.rs         # Config 结构体，环境变量加载
├── store.rs          # FileStore, VideoJobStore
├── middleware.rs     # 速率限制, 安全头
├── util.rs           # 通用工具函数
└── handler/          # HTTP 处理器
│   ├── convert.rs    # 格式转换 (upload/url/pdf-to-office/sketch/idphoto)
│   ├── imagegen.rs   # AI 图像生成 (text/edit/compose)
│   ├── videogen.rs   # AI 视频生成 (text/keyframe/ref + task status)
│   ├── translate.rs  # 翻译 (文本/文件)
│   ├── download.rs   # 文件下载
│   └── pages.rs      # 静态页面服务
└── service/          # 业务逻辑
    ├── aiclient.rs   # AI API 客户端
    ├── fetch.rs      # 安全网络下载
    ├── imagegen.rs   # 图像处理 (尺寸约束、渲染)
    ├── videogen.rs   # 视频生成 (分段、拼接)
    ├── pdfoffice.rs  # PDF 转换
    ├── sketch.rs     # 素描效果
    ├── idphoto.rs    # 证件照
    ├── translate.rs  # 翻译逻辑
    └── vectorize.rs  # 矢量化
```

## 添加新功能

### 添加新的转换格式

1. 在 `scripts/` 中添加 Python 脚本
2. 在 `src/handler/convert.rs` 中添加路由
3. 在 `src/service/vectorize.rs` 中添加格式白名单
4. 在 `src/util.rs` 的 `safe_ext` 中添加扩展名

### 添加新的 AI 引擎

1. 在 `src/service/aiclient.rs` 中添加 API 调用方法
2. 在 `Config` 中添加相关配置字段
3. 在 `src/handler/imagegen.rs` 中更新 fallback 逻辑

### 添加新的 HTTP 端点

1. 在 `src/handler/` 下创建新模块
2. 在 `src/lib.rs` 的 `main_inner` 中注册路由
3. 添加集成测试

## 测试

### 单元测试

```bash
# 运行所有单元测试
cargo test --lib

# 运行特定模块测试
cargo test --lib service::videogen

# 运行特定测试
cargo test test_split_duration
```

### 集成测试

```bash
# 运行所有集成测试
cargo test --test integration

# 运行特定集成测试
cargo test test_post_convert_image_text_empty_prompt_returns_400
```

### 冒烟测试

```bash
# 启动服务
./target/release/flowconvert &

# 测试 API
curl http://localhost:8080/api/formats

# 测试文件转换
curl -X POST -F "file=@test.jpg" http://localhost:8080/api/convert/upload
```

## 性能优化

### 编译优化

```bash
# Release 模式 (已启用 LTO + strip)
cargo build --release

# 进一步优化 (需要 nightly)
RUSTFLAGS="-C target-cpu=native" cargo build --release
```

### 内存管理

- FileStore 使用后台线程定期清理过期文件
- VideoJobStore 使用 Mutex + HashMap，适合低频访问
- AI 请求使用流式下载避免大文件内存占用

## 安全注意事项

1. **SSRF 防护**: fetch.rs 已实现 DNS 预解析 + IP 校验
2. **路径遍历**: 所有文件名经过 sanitize_name_part 处理
3. **速率限制**: 全局 100 请求/分钟的令牌桶算法
4. **输入校验**: 文件魔数检查、扩展名白名单、大小限制

详见 [安全设计](./专有概念/安全设计.md)

## CI/CD

### GitHub Actions

```yaml
name: Build
on: [push, pull_request]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: cargo build --release
      - run: cargo test
```

### 二进制发布

```bash
# 构建 Linux amd64
cargo build --release --target x86_64-unknown-linux-gnu

# 发布到 GitHub Releases
gh release create v0.1.2 target/release/flowconvert
```

## 与 Go 原版对比

| 功能 | Go 原版 | Rust 重写版 |
|------|---------|-------------|
| Web 框架 | Gin | Axum 0.8 |
| 并发模型 | goroutine | tokio async |
| 向量转换 | 原生 | Python subprocess |
| PDF 转换 | 原生 | Python subprocess |
| 翻译 | 原生 | Python subprocess |
| AI 图像 | Agnes/SenseNova | 同左 |
| AI 视频 | Agnes/SenseNova | 同左 + 长视频分段 |
| SSRF 防护 | Go net.Dialer | DNS 预解析 + IP 校验 |

## 故障排查

### 常见问题

1. **Python 脚本找不到**
   ```bash
   # 确保 scripts/ 目录在可访问路径
   export FLOWCONVERT_DATA_DIR=/path/to/data
   ```

2. **ffmpeg 未找到**
   ```bash
   apt-get install -y ffmpeg
   ```

3. **端口被占用**
   ```bash
   FLOWCONVERT_PORT=9000 ./target/release/flowconvert
   ```

4. **AI 服务不可用**
   - 检查 API 密钥配置
   - 查看日志中的错误信息
   - 确认 fallback 引擎可用

### 日志级别

```bash
# 详细日志
RUST_LOG=debug ./target/release/flowconvert

# 仅错误
RUST_LOG=error ./target/release/flowconvert
```
