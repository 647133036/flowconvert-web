use std::net::{IpAddr, SocketAddr};
use std::path::Path;
use std::sync::LazyLock;

use futures_util::stream::StreamExt;
use regex::Regex;
use reqwest::Client;

use super::aiclient::validate_download_url;
use crate::util::new_id;


/// Check if an IpAddr is a safe public address (not loopback, private, link-local, or unspecified).
fn is_safe_public_ip(ip: IpAddr) -> bool {
    match ip {
        IpAddr::V4(v4) => {
            !v4.is_loopback()
                && !v4.is_private()
                && !v4.is_link_local()
                && !v4.is_unspecified()
                && !v4.is_multicast()
                // CGNAT (100.64.0.0/10): first octet = 100, second = 64..=127
                && !({ let o = v4.octets(); o[0] == 100 && (o[1] as u8) >= 64 && (o[1] as u8) <= 127 })
                // 192.0.0.0/24
                && !({ let o = v4.octets(); o[0] == 192 && o[1] == 0 && o[2] == 0 })
        }
        IpAddr::V6(v6) => {
            // Reject IPv4-mapped (::ffff:x.x.x.x)
            if v6.segments()[0] == 0 && v6.segments()[1] == 0
                && v6.segments()[2] == 0 && v6.segments()[3] == 0
                && v6.segments()[4] == 0 && v6.segments()[5] == 0xFFFF {
                return false;
            }
            !v6.is_loopback()
                && !v6.is_unspecified()
                && !(v6.segments()[0] & 0xFC00 == 0xFC00)
                && !(v6.segments()[0] == 0x2001 && v6.segments()[1] == 0xDB8)
                && !v6.is_multicast()
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
    let port = parsed.port_or_known_default().unwrap_or(80);
    let pre_resolved: Vec<SocketAddr> = tokio::net::lookup_host((host.as_str(), port))
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

/// Download an image or PDF from a public URL into tmp_dir and return its path.
/// Mirrors Go service.FetchFile for the OCR endpoint.
pub async fn fetch_doc(
    tmp_dir: &str,
    raw_url: &str,
    max_bytes: u64,
    allowed_exts: &[&str],
) -> Result<(String, String), String> {
    if raw_url.is_empty() {
        return Err("无效或不允许访问的 URL".to_string());
    }
    let parsed = raw_url.parse::<reqwest::Url>()
        .map_err(|_| "无效或不允许访问的 URL".to_string())?;
    if parsed.scheme() != "http" && parsed.scheme() != "https" {
        return Err("无效或不允许访问的 URL".to_string());
    }
    validate_download_url(raw_url)?;

    // DNS pre-resolution blocks rebinding between lookup and connect.
    let host = parsed.host_str().ok_or("URL 缺少 host")?.to_string();
    let port = parsed.port_or_known_default().unwrap_or(80);
    let pre_resolved: Vec<SocketAddr> = tokio::net::lookup_host((host.as_str(), port))
        .await
        .map_err(|_| "DNS 解析失败".to_string())?
        .collect();

    if pre_resolved.is_empty() {
        return Err("DNS 解析无结果".to_string());
    }
    if !pre_resolved.iter().any(|sa| is_safe_public_ip(sa.ip())) {
        return Err("禁止访问内网/回环地址资源".to_string());
    }

    let client = Client::builder()
        .timeout(std::time::Duration::from_secs(30))
        .redirect(reqwest::redirect::Policy::limited(5))
        .build()
        .map_err(|e| format!("下载失败: {}", e))?;

    let resp = client
        .get(raw_url)
        .send()
        .await
        .map_err(|e| format!("下载失败: {}", e))?;
    if resp.status() != 200 {
        return Err(format!("下载失败: HTTP {}", resp.status()));
    }
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
    let ct = content_type.split(';').next().unwrap_or("").trim().to_lowercase();
    let is_doc_ct = ct.starts_with("image/")
        || ct == "application/pdf"
        || ct == "application/x-pdf"
        || ct == "application/octet-stream";
    if !is_doc_ct {
        return Err("链接内容不是图片或 PDF".to_string());
    }

    let ext_from_url = parsed
        .path_segments()
        .and_then(|mut s| s.next_back())
        .and_then(|name| std::path::Path::new(name).extension())
        .map(|e| e.to_string_lossy().to_lowercase())
        .unwrap_or_default();
    let ext = if allowed_exts.contains(&ext_from_url.as_str()) {
        ext_from_url
    } else if ct == "application/pdf" || ct == "application/x-pdf" {
        "pdf".to_string()
    } else if ct.starts_with("image/jpeg") {
        "jpg".to_string()
    } else if ct.starts_with("image/png") {
        "png".to_string()
    } else if ct.starts_with("image/gif") {
        "gif".to_string()
    } else if ct.starts_with("image/webp") {
        "webp".to_string()
    } else if ct.starts_with("image/bmp") {
        "bmp".to_string()
    } else if ct.starts_with("image/tiff") {
        "tiff".to_string()
    } else {
        return Err("链接内容不是图片或 PDF".to_string());
    };

    let limit = if max_bytes > 0 { max_bytes } else { 50 * 1024 * 1024 };
    let mut reader = resp.bytes_stream();
    let mut data = Vec::new();
    let mut total = 0u64;
    while let Some(chunk) = reader.next().await {
        let chunk = chunk.map_err(|e| format!("下载失败: {}", e))?;
        if total + chunk.len() as u64 > limit {
            return Err("链接文件超过大小限制".to_string());
        }
        data.extend_from_slice(&chunk);
        total += chunk.len() as u64;
    }

    let tmp_name = format!("url_{}.{}", new_id(10), ext);
    let tmp_path = Path::new(tmp_dir).join(tmp_name);
    std::fs::write(&tmp_path, &data).map_err(|e| format!("保存失败: {}", e))?;
    Ok((tmp_path.to_string_lossy().to_string(), ext))
}

/// Download a web page as text for link translation, with SSRF and size protections.
/// Mirrors Go service.FetchWebPage.
pub async fn fetch_web_page(raw_url: &str, max_bytes: u64) -> Result<String, String> {
    if raw_url.is_empty() {
        return Err("无效或不允许访问的 URL".to_string());
    }
    let parsed = raw_url
        .parse::<reqwest::Url>()
        .map_err(|_| "无效或不允许访问的 URL".to_string())?;
    if parsed.scheme() != "http" && parsed.scheme() != "https" {
        return Err("无效或不允许访问的 URL".to_string());
    }
    validate_download_url(raw_url)?;

    let host = parsed.host_str().ok_or("URL 缺少 host")?.to_string();
    let port = parsed.port_or_known_default().unwrap_or(80);
    let pre_resolved: Vec<SocketAddr> = tokio::net::lookup_host((host.as_str(), port))
        .await
        .map_err(|_| "DNS 解析失败".to_string())?
        .collect();

    if pre_resolved.is_empty() {
        return Err("DNS 解析无结果".to_string());
    }
    if !pre_resolved.iter().any(|sa| is_safe_public_ip(sa.ip())) {
        return Err("禁止访问内网/回环地址资源".to_string());
    }

    let client = Client::builder()
        .timeout(std::time::Duration::from_secs(30))
        .redirect(reqwest::redirect::Policy::limited(5))
        .build()
        .map_err(|e| format!("下载失败: {}", e))?;

    let resp = client
        .get(raw_url)
        .send()
        .await
        .map_err(|e| format!("下载失败: {}", e))?;
    if resp.status() != 200 {
        return Err(format!("下载失败: HTTP {}", resp.status()));
    }
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
    let ct = content_type.split(';').next().unwrap_or("").trim().to_lowercase();
    if !ct.is_empty() && !is_web_content_type(&ct) {
        return Err("URL 内容不是网页".to_string());
    }

    let limit = if max_bytes > 0 {
        max_bytes
    } else {
        5 * 1024 * 1024
    };
    let mut reader = resp.bytes_stream();
    let mut data = Vec::new();
    let mut total = 0u64;
    while let Some(chunk) = reader.next().await {
        let chunk = chunk.map_err(|e| format!("下载失败: {}", e))?;
        if total + chunk.len() as u64 > limit + 1 {
            return Err("网页内容过大".to_string());
        }
        data.extend_from_slice(&chunk);
        total += chunk.len() as u64;
    }
    if total > limit {
        return Err("网页内容过大".to_string());
    }

    String::from_utf8(data).map_err(|_| "网页编码异常".to_string())
}

fn is_web_content_type(ct: &str) -> bool {
    ct == "text/html"
        || ct == "text/plain"
        || ct == "application/xhtml+xml"
        || ct == "application/xml"
}

static RE_SCRIPT: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"(?is)<script[^>]*>.*?</script>").unwrap());
static RE_STYLE: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"(?is)<style[^>]*>.*?</style>").unwrap());
static RE_COMMENT: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"(?s)<!--.*?-->").unwrap());
static RE_BR: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"(?i)<br\s*/?>").unwrap());
static RE_BLOCK: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(
        r"(?is)</?(p|div|h[1-6]|li|tr|td|th|section|article|header|footer|ul|ol|table|blockquote|pre)[^>]*>",
    )
    .unwrap()
});
static RE_TAG: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"(?s)<[^>]+>").unwrap());
static RE_SPACE: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"[ \t]+").unwrap());

/// Heuristic HTML-to-text conversion for link translation input.
/// Mirrors Go service.ExtractWebText.
pub fn extract_web_text(page: &str) -> String {
    let mut s = RE_SCRIPT.replace_all(page, "").into_owned();
    s = RE_STYLE.replace_all(&s, "").into_owned();
    s = RE_COMMENT.replace_all(&s, "").into_owned();
    s = RE_BR.replace_all(&s, "\n").into_owned();
    s = RE_BLOCK.replace_all(&s, "\n").into_owned();
    s = RE_TAG.replace_all(&s, "").into_owned();
    s = html_escape::decode_html_entities(&s).into_owned();

    let mut out: Vec<String> = Vec::new();
    for line in s.lines() {
        let line = RE_SPACE.replace_all(line, " ").trim().to_string();
        if !line.is_empty() {
            out.push(line);
        }
    }
    out.join("\n")
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
