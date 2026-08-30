use std::io::Write;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use futures_util::stream::StreamExt;
use reqwest::Client;

use super::aiclient::{validate_download_url, AIClient};
use crate::util::{new_id, script_path, python_path};

const MAX_FETCH_BYTES: i64 = 20 * 1024 * 1024;

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
}
