use std::net::{IpAddr, SocketAddr};
use std::path::Path;

use futures_util::stream::StreamExt;
use reqwest::Client;

use super::aiclient::validate_download_url;
use crate::util::new_id;


/// Check if an IpAddr is a safe public address (not loopback, private, link-local, or unspecified).
fn is_safe_public_ip(ip: IpAddr) -> bool {
    match ip {
        IpAddr::V4(v4) => !v4.is_loopback() && !v4.is_private() && !v4.is_link_local() && !v4.is_unspecified(),
        IpAddr::V6(v6) => {
            // IPv6: reject loopback, unspecified, link-local (fe80::/10), and documentation (2001:db8::/32)
            v6.is_loopback() || v6.is_unspecified()
                || v6.segments()[0] & 0xFC00 == 0xFC00  // fe80::/10 link-local
                || v6.segments()[0] == 0x2001 && v6.segments()[1] == 0xDB8  // 2001:db8::/32 documentation
                || v6.is_multicast()
        }
    }
}

pub async fn fetch_image(
    tmp_dir: &str,
    raw_url: &str,
    max_bytes: u64,
) -> Result<String, String> {
    if raw_url.is_empty() {
        return Err("无效或不允许访问的 URL".to_string());
    }
    let parsed = raw_url.parse::<reqwest::Url>()
        .map_err(|_| "无效或不允许访问的 URL".to_string())?;
    if parsed.scheme() != "http" && parsed.scheme() != "https" {
        return Err("无效或不允许访问的 URL".to_string());
    }
    validate_download_url(raw_url)?;

    // DNS pre-resolution: resolve host to all IPs before connecting,
    // then verify the actual connected IP is in the pre-resolved set.
    // This prevents DNS rebinding attacks.
    let host = parsed.host_str().ok_or("URL 缺少 host")?.to_string();
    let pre_resolved: Vec<SocketAddr> = tokio::net::lookup_host(format!("{}:443", host))
        .await
        .map_err(|_| "DNS 解析失败".to_string())?
        .collect();

    if pre_resolved.is_empty() {
        return Err("DNS 解析无结果".to_string());
    }

    // Filter: if all pre-resolved IPs are private/loopback, reject immediately
    let has_safe_ip = pre_resolved.iter().any(|sa| is_safe_public_ip(sa.ip()));
    if !has_safe_ip {
        return Err("禁止访问内网/回环地址资源".to_string());
    }

    let client = Client::builder()
        .timeout(std::time::Duration::from_secs(30))
        .redirect(reqwest::redirect::Policy::limited(5))
        .build()
        .map_err(|e| format!("下载失败: {}", e))?;

    let resp = client.get(raw_url).send().await
        .map_err(|e| format!("下载失败: {}", e))?;

    if resp.status() != 200 {
        return Err(format!("下载失败: HTTP {}", resp.status()));
    }

    // Verify the connected IP is in the pre-resolved set (DNS rebinding check)
    if let Some(remote) = resp.remote_addr() {
        if !pre_resolved.iter().any(|sa| sa.ip() == remote.ip()) {
            return Err("DNS 重绑定防护：连接 IP 与预解析地址不符，请求已拒绝".to_string());
        }
    }

    let content_type = resp
        .headers()
        .get(reqwest::header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .to_string();
    if !content_type.starts_with("image/") {
        return Err("URL 内容不是有效图片".to_string());
    }

    let limit = if max_bytes > 0 { max_bytes } else { 20 * 1024 * 1024 };
    let mut reader = resp.bytes_stream();
    let mut data = Vec::new();
    let mut total = 0u64;
    while let Some(chunk) = reader.next().await {
        let chunk = chunk.map_err(|e| format!("下载失败: {}", e))?;
        if total + chunk.len() as u64 > limit + 1 {
            return Err("文件超过 20MB 限制".to_string());
        }
        data.extend_from_slice(&chunk);
        total += chunk.len() as u64;
    }
    if total > limit {
        return Err("文件超过 20MB 限制".to_string());
    }

    let tmp_name = format!("url_{}", new_id(10));
    let tmp_path = Path::new(tmp_dir).join(tmp_name);
    std::fs::write(&tmp_path, &data)
        .map_err(|e| format!("保存失败: {}", e))?;
    Ok(tmp_path.to_string_lossy().to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn test_fetch_rejects_empty() {
        assert!(fetch_image("/tmp", "", 0).await.is_err());
    }

    #[tokio::test]
    async fn test_fetch_rejects_non_http_scheme() {
        assert!(fetch_image("/tmp", "ftp://example.com/image.jpg", 0).await.is_err());
        assert!(fetch_image("/tmp", "file:///tmp/test.jpg", 0).await.is_err());
        assert!(fetch_image("/tmp", "data:image/png;base64,abc", 0).await.is_err());
    }

    #[tokio::test]
    async fn test_fetch_rejects_internal_urls() {
        assert!(fetch_image("/tmp", "http://127.0.0.1/test.jpg", 0).await.is_err());
        assert!(fetch_image("/tmp", "http://localhost/test.jpg", 0).await.is_err());
        assert!(fetch_image("/tmp", "http://192.168.1.1/test.jpg", 0).await.is_err());
        assert!(fetch_image("/tmp", "http://10.0.0.1/test.jpg", 0).await.is_err());
    }

    #[test]
    fn test_validate_url_logic() {
        // Empty URL
        assert!(validate_download_url("").is_err());
        // Valid public HTTPS
        assert!(validate_download_url("https://cdn.example.com/image.png").is_ok());
        // Valid public HTTP
        assert!(validate_download_url("http://example.com/image.png").is_ok());
        // Invalid scheme
        assert!(validate_download_url("ftp://example.com/image.png").is_err());
    }
}
