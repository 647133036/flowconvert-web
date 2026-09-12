# fetch 模块

## 概述

fetch 模块提供安全的网络文件下载功能，核心目标是防止 SSRF 攻击。

## 核心函数

### fetch_image

```rust
pub async fn fetch_image(
    tmp_dir: &str,
    raw_url: &str,
    max_bytes: u64,
) -> Result<String, String>
```

安全下载图像并保存到 tmp_dir，返回文件路径。

**流程**:
1. URL 格式校验 (`validate_download_url`)
2. DNS 预解析 → 收集所有 SocketAddr
3. IP 安全检查 (`is_safe_public_ip`)
4. 发起 HTTP 请求（带 30s 超时）
5. DNS 重绑定检测：验证 remote_addr 在预解析集合中
6. 流式下载 + 大小限制

## SSRF 防护机制

### DNS 预解析

```rust
let pre_resolved: Vec<SocketAddr> = tokio::net::lookup_host((host.as_str(), port))
    .await
    .map_err(|_| "DNS 解析失败".to_string())?
    .collect();
```

所有 DNS 记录在请求前解析并检查。

### IP 地址检查

```rust
fn is_safe_public_ip(ip: IpAddr) -> bool {
    match ip {
        IpAddr::V4(v4) => {
            !v4.is_loopback()
            && !v4.is_private()
            && !v4.is_link_local()
            && !v4.is_unspecified()
            && !v4.is_multicast()
            && !is_cgnat(v4)          // 100.64.0.0/10
            && !is_test_network(v4)   // 192.0.0.0/24
        }
        IpAddr::V6(v6) => {
            !is_ipv4_mapped(v6)       // ::ffff:x.x.x.x
            && !v6.is_loopback()
            && !v6.is_unspecified()
            && !is_documentation(v6)  // 2001:db8::/32
            && !v6.is_multicast()
        }
    }
}
```

### DNS 重绑定防护

```rust
if let Some(remote) = resp.remote_addr() {
    if !pre_resolved.iter().any(|sa| sa.ip() == remote.ip()) {
        return Err("DNS 重绑定防护：连接 IP 与预解析地址不符");
    }
}
```

## URL 校验

### validate_download_url

```rust
pub fn validate_download_url(raw: &str) -> Result<(), String>
```

校验 URL 格式和基本安全性：
- 仅允许 http/https 协议
- host 不能包含非法字符 (/, \, :)
- localhost 被拒绝
- IP 地址通过 is_safe_public_ip 检查

## 流式下载

```rust
let mut reader = resp.bytes_stream();
let mut data = Vec::new();
let mut total = 0u64;
while let Some(chunk) = reader.next().await {
    let chunk = chunk.map_err(|e| format!("下载失败: {}", e))?;
    if total + chunk.len() as u64 > limit + 1 {
        return Err("文件超过限制".to_string());
    }
    data.extend_from_slice(&chunk);
    total += chunk.len() as u64;
}
```

## 测试

```bash
cargo test service::fetch::tests
```
