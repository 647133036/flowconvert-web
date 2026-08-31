# FlowConvert 项目文档索引

## 概述

FlowConvert 是一个基于 Rust 的高性能文件转换与 AI 生成 Web 服务，提供格式转换、证件照生成、图片处理、视频生成、AI 图像生成和翻译等功能。

## 文档结构

| 文档 | 说明 |
|------|------|
| [ARCHITECTURE.md](./ARCHITECTURE.md) | 系统架构与设计 |
| [INTERFACES.md](./INTERFACES.md) | API 接口参考 |
| [DEVELOPER_GUIDE.md](./DEVELOPER_GUIDE.md) | 开发者指南 |
| [配置管理](./专有概念/配置管理.md) | 配置系统设计 |
| [安全设计](./专有概念/安全设计.md) | SSRF 防护与安全机制 |
| [文件存储](./专有概念/文件存储.md) | FileStore 和 VideoJobStore 设计 |
| [convert](./模块/convert.md) | 格式转换模块 |
| [imagegen](./模块/imagegen.md) | AI 图像生成模块 |
| [videogen](./模块/videogen.md) | 视频生成模块 |
| [translate](./模块/translate.md) | 翻译模块 |
| [fetch](./模块/fetch.md) | 网络下载模块 |

## 技术栈

- **语言**: Rust (edition 2021)
- **Web 框架**: Axum 0.8
- **异步运行时**: Tokio
- **图像处理**: image crate 0.25
- **HTTP 客户端**: reqwest 0.12 (rustls-tls)
- **序列化**: serde + serde_json

## 核心特性

1. **多格式转换**: 图像矢量化 (SVG/AI/DXF/... )、PDF 转 Office (DOCX/XLSX)
2. **AI 图像生成**: 文本生成、编辑、合成 (支持 Agnes/SenseNova 双引擎)
3. **AI 视频生成**: 文本视频、关键帧视频、参考图视频 (支持短/长视频分段)
4. **证件照生成**: 智能裁剪与背景处理
5. **多语言翻译**: 文本与文件翻译 (支持 100+ 语言)
6. **安全防护**: SSRF 防护、路径遍历防护、速率限制

## 目录结构

```
flowconvert/
├── src/                    # Rust 源码
│   ├── main.rs             # 入口点
│   ├── lib.rs              # 库入口与路由注册
│   ├── config.rs           # 配置加载
│   ├── store.rs            # 持久化存储
│   ├── middleware.rs        # 中间件 (速率限制、安全头)
│   ├── util.rs             # 工具函数
│   ├── handler/            # HTTP 处理器
│   │   ├── convert.rs      # 格式转换处理器
│   │   ├── imagegen.rs     # 图像生成处理器
│   │   ├── videogen.rs     # 视频生成处理器
│   │   ├── translate.rs    # 翻译处理器
│   │   ├── download.rs     # 文件下载处理器
│   │   └── pages.rs        # 静态页面处理器
│   └── service/            # 业务逻辑服务
│       ├── aiclient.rs     # AI 客户端 (Agnes/SenseNova)
│       ├── fetch.rs        # 安全网络下载
│       ├── imagegen.rs     # 图像处理逻辑
│       ├── videogen.rs     # 视频生成逻辑
│       ├── pdfoffice.rs    # PDF 转 Office
│       ├── sketch.rs       # 素描生成
│       ├── idphoto.rs      # 证件照生成
│       ├── translate.rs    # 翻译逻辑
│       └── vectorize.rs    # 图像矢量化
├── scripts/                # Python 辅助脚本
├── web/                    # 前端静态资源
├── tests/                  # 集成测试
└── .monkeycode/            # 项目元数据
    ├── docs/               # 文档
    └── MEMORY.md           # 记忆文件
```
