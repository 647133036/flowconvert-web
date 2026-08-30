use std::io::Write;
use std::time::{Duration, Instant};
use std::sync::Mutex;

use futures_util::stream::StreamExt;
use reqwest::Client;
use serde::{Deserialize, Serialize};
use serde_json::json;

use crate::store::VideoJobStore;

const AGNES_VIDEO_MODEL: &str = "agnes-video-2.5-flash";
const SEGMENT_ATTEMPTS: usize = 3;

#[derive(Debug, Clone)]
pub struct VideoTaskParams {
    pub prompt: String,
    pub mode: String,
    pub seconds: String,
    pub aspect_ratio: String,
    pub first_frame: String,
    pub last_frame: String,
    pub images: Vec<String>,
}

pub struct AIClient {
    pub agnes_base: String,
    pub agnes_key: String,
    pub sense_base: String,
    pub sense_key: String,
    pub http: Client,
    rate_limiter: Mutex<RateLimiterState>,
    job_store: Option<std::sync::Arc<VideoJobStore>>,
}

struct RateLimiterState {
    agnes_since: Instant,
    agnes_count: usize,
}

impl AIClient {
    pub fn new(
        agnes_base: &str,
        agnes_key: &str,
        sense_base: &str,
        sense_key: &str,
        job_store: Option<std::sync::Arc<VideoJobStore>>,
    ) -> Self {
        Self {
            agnes_base: agnes_base.to_string(),
            agnes_key: agnes_key.to_string(),
            sense_base: sense_base.to_string(),
            sense_key: sense_key.to_string(),
            http: Client::builder()
                .timeout(Duration::from_secs(180))
                .build()
                .unwrap_or_default(),
            rate_limiter: Mutex::new(RateLimiterState {
                agnes_since: Instant::now(),
                agnes_count: 0,
            }),
            job_store,
        }
    }

    pub fn has_agnes(&self) -> bool {
        !self.agnes_key.is_empty()
    }

    pub fn has_sensenova(&self) -> bool {
        !self.sense_key.is_empty()
    }

    pub async fn gen_image_agnes(
        &self,
        model: &str,
        prompt: &str,
        size: &str,
        ratio: &str,
        images: &[String],
    ) -> Result<(String, String), String> {
        let mut body = json!({
            "model": model,
            "prompt": prompt,
            "size": size,
        });
        if !ratio.is_empty() {
            body["ratio"] = json!(ratio);
        }
        let mut extra = json!({
            "response_format": "url",
        });
        if !images.is_empty() {
            extra["image"] = json!(images);
        }
        body["extra_body"] = extra;

        let resp = self.post_json(
            &format!("{}/images/generations", self.agnes_base),
            &self.agnes_key,
            &body,
        ).await?;

        #[derive(Deserialize)]
        struct ImageGenResponse {
            data: Vec<ImageData>,
            error: Option<ApiError>,
        }
        #[derive(Deserialize)]
        struct ImageData {
            url: String,
            b64_json: String,
        }
        #[derive(Deserialize)]
        struct ApiError {
            message: String,
            #[allow(dead_code)]
            r#type: String,
        }

        let res: ImageGenResponse =
            serde_json::from_slice(&resp).map_err(|e| format!("agnes图片API响应解析失败: {}", e))?;
        if let Some(err) = res.error {
            return Err(format!("agnes图片API错误: {}", err.message));
        }
        let first = res
            .data
            .first()
            .ok_or("agnes图片API返回空数据".to_string())?;
        Ok((first.url.clone(), first.b64_json.clone()))
    }

    pub async fn gen_image_sense_nova(
        &self,
        model: &str,
        prompt: &str,
        size: &str,
        ratio: &str,
        images: &[String],
    ) -> Result<(String, String), String> {
        let mut body = json!({
            "model": model,
            "prompt": prompt,
            "watermark": false,
        });
        if !size.is_empty() {
            body["size"] = json!(size);
        }
        if !ratio.is_empty() {
            body["ratio"] = json!(ratio);
        }
        if !images.is_empty() {
            body["image"] = json!(images);
        }

        let resp = self.post_json(
            &format!("{}/images/generations", self.sense_base),
            &self.sense_key,
            &body,
        ).await?;

        #[derive(Deserialize)]
        struct ImageGenResponse {
            data: Vec<ImageData>,
            error: Option<ApiError>,
        }
        #[derive(Deserialize)]
        struct ImageData {
            url: String,
            b64_json: String,
        }
        #[derive(Deserialize)]
        struct ApiError {
            message: String,
        }

        let res: ImageGenResponse =
            serde_json::from_slice(&resp).map_err(|e| format!("sensenova图片API响应解析失败: {}", e))?;
        if let Some(err) = res.error {
            return Err(format!("sensenova图片API错误: {}", err.message));
        }
        let first = res
            .data
            .first()
            .ok_or("sensenova图片API返回空数据".to_string())?;
        Ok((first.url.clone(), first.b64_json.clone()))
    }

    pub async fn download_image(&self, img_url: &str, b64: &str, dest: &str) -> Result<(), String> {
        if !b64.is_empty() {
            let data = base64_decode(b64).map_err(|e| format!("base64解码失败: {}", e))?;
            std::fs::write(dest, data).map_err(|e| format!("保存失败: {}", e))?;
            return Ok(());
        }
        if img_url.is_empty() {
            return Err("无图片URL或base64数据".to_string());
        }
        validate_download_url(img_url)?;

        let resp = self.http.get(img_url).send().await.map_err(|e| format!("下载图片失败: {}", e))?;
        if resp.status() != 200 {
            return Err(format!("下载图片失败: HTTP {}", resp.status()));
        }
        let bytes = resp.bytes().await.map_err(|e| format!("读取图片失败: {}", e))?;
        if bytes.len() > 100 * 1024 * 1024 {
            return Err("图片过大".to_string());
        }
        std::fs::write(dest, bytes).map_err(|e| format!("保存失败: {}", e))?;
        Ok(())
    }

    pub fn file_to_data_uri(path: &str) -> Result<String, String> {
        let data = std::fs::read(path).map_err(|e| format!("读取文件失败: {}", e))?;
        let ext = std::path::Path::new(path)
            .extension()
            .map(|e| e.to_string_lossy().to_string())
            .unwrap_or_default();
        let mime = match ext.as_str() {
            "jpg" | "jpeg" => "image/jpeg",
            "webp" => "image/webp",
            _ => "image/png",
        };
        let b64 = base64_encode(&data);
        Ok(format!("data:{};base64,{}", mime, b64))
    }

    pub async fn create_video_task(&self, params: &VideoTaskParams) -> Result<String, String> {
        let mut body = json!({
            "model": AGNES_VIDEO_MODEL,
            "prompt": &params.prompt,
            "mode": &params.mode,
            "size": "720P",
        });
        body["seconds"] = if params.seconds.is_empty() {
            json!("5")
        } else {
            json!(&params.seconds)
        };
        if !params.aspect_ratio.is_empty() {
            body["aspect_ratio"] = json!(&params.aspect_ratio);
        }
        if params.mode == "keyframe" {
            if !params.first_frame.is_empty() {
                body["first_frame"] = json!(&params.first_frame);
            }
            if !params.last_frame.is_empty() {
                body["last_frame"] = json!(&params.last_frame);
            }
        }
        if params.mode == "reference" && !params.images.is_empty() {
            body["images"] = json!(&params.images);
        }

        let max_retries = 10;
        let mut last_err: Option<String> = None;
        let mut resp_result: Result<Vec<u8>, String> = Err("no attempt made".to_string());

        for attempt in 0..max_retries {
            self.acquire_agnes_token();
            resp_result = self.post_json(
                &format!("{}/videos", self.agnes_base),
                &self.agnes_key,
                &body,
            ).await;

            if resp_result.is_ok() {
                break;
            }
            let err_str = resp_result.as_ref().unwrap_err();

            if err_str.contains("503") && err_str.contains("video_queue_full") {
                let backoff = Duration::from_secs(std::cmp::min(10 * (attempt + 1) as u64, 300));
                eprintln!("[Agnes] 503 queue full, retry {}/{} after {:?}", attempt + 1, max_retries, backoff);
                std::thread::sleep(backoff);
                continue;
            }
            if err_str.contains("429") || err_str.contains("rate_limit") || err_str.contains("rate limit") {
                let backoff = Duration::from_secs(std::cmp::min(30 * (attempt + 1) as u64, 120));
                eprintln!("[Agnes] 429 rate limited, retry {}/{} after {:?}", attempt + 1, max_retries, backoff);
                std::thread::sleep(backoff);
                continue;
            }
            last_err = Some(resp_result.as_ref().unwrap_err().clone());
            break;
        }

        let resp = resp_result.map_err(|e| format!("agnes视频API创建任务失败: {}", e))?;

        #[derive(Deserialize)]
        struct VideoTaskResponse {
            id: String,
            task_id: String,
            video_id: String,
            detail: String,
            error: Option<ApiErrorDetail>,
        }
        #[derive(Deserialize)]
        struct ApiErrorDetail {
            message: String,
        }

        let res: VideoTaskResponse = serde_json::from_slice(&resp)
            .map_err(|e| format!("agnes视频API响应解析失败: {}", e))?;
        if !res.detail.is_empty() {
            return Err(format!("agnes视频API参数错误: {}", res.detail));
        }
        if let Some(err) = res.error {
            return Err(format!("agnes视频API错误: {}", err.message));
        }
        let vid = if !res.video_id.is_empty() {
            res.video_id
        } else if !res.task_id.is_empty() {
            res.task_id
        } else {
            res.id
        };
        if vid.is_empty() {
            return Err("agnes视频API未返回任务ID".to_string());
        }
        Ok(vid)
    }

    pub async fn poll_video_task(&self, video_id: &str, timeout: Duration) -> Result<String, String> {
        let base = self.agnes_base.trim_end_matches("/v1");
        let poll_url = format!(
            "{}/agnesapi?video_id={}&model_name={}",
            base,
            urlencoding::encode(video_id),
            AGNES_VIDEO_MODEL,
        );

        let deadline = std::time::Instant::now() + timeout;
        let interval = Duration::from_secs(3);

        while std::time::Instant::now() < deadline {
            let resp = match self.http.get(&poll_url).send().await {
                Ok(r) => r,
                Err(_) => {
                    std::thread::sleep(interval);
                    continue;
                }
            };
            let body = match resp.text().await {
                Ok(b) => b,
                Err(_) => {
                    std::thread::sleep(interval);
                    continue;
                }
            };

            #[derive(Deserialize)]
            struct VideoResultResponse {
                status: String,
                progress: i32,
                url: String,
                metadata: Metadata,
                error: Option<ApiErrorDetail>,
            }
            #[derive(Deserialize)]
            struct Metadata {
                url: String,
            }
            #[derive(Deserialize)]
            struct ApiErrorDetail {
                message: String,
            }

            let res: VideoResultResponse = match serde_json::from_str(&body) {
                Ok(r) => r,
                Err(_) => {
                    std::thread::sleep(interval);
                    continue;
                }
            };

            if res.status == "completed" {
                if !res.url.is_empty() {
                    return Ok(res.url);
                }
                if !res.metadata.url.is_empty() {
                    return Ok(res.metadata.url);
                }
                return Err("视频已完成但未返回URL".to_string());
            }
            if res.status == "failed" {
                let msg = res
                    .error
                    .as_ref()
                    .map(|e| e.message.clone())
                    .unwrap_or_else(|| "生成失败".to_string());
                return Err(format!("视频生成失败: {}", msg));
            }
            std::thread::sleep(interval);
        }
        Err("视频生成超时".to_string())
    }

    pub async fn generate_video_segment(
        &self,
        seg_path: &str,
        params: &VideoTaskParams,
        label: &str,
    ) -> Result<(), String> {
        let mut last_err: Option<String> = None;
        for attempt in 0..SEGMENT_ATTEMPTS {
            if attempt > 0 {
                eprintln!("[Agnes] 段{}第{}次重试", label, attempt + 1);
                std::thread::sleep(Duration::from_secs(5));
            }
            let video_id = match self.create_video_task(params).await {
                Ok(id) => id,
                Err(e) => {
                    last_err = Some(e.clone());
                    if is_transient_video_err(&e) {
                        continue;
                    }
                    return Err(e);
                }
            };
            let video_url = match self.poll_video_task(&video_id, Duration::from_secs(1800)).await {
                Ok(u) => u,
                Err(e) => {
                    last_err = Some(e.clone());
                    if is_transient_video_err(&e) {
                        continue;
                    }
                    return Err(e);
                }
            };
            if let Err(e) = self.download_video(&video_url, seg_path).await {
                last_err = Some(e);
                continue;
            }
            return Ok(());
        }
        Err(last_err.unwrap_or("未知错误".to_string()))
    }

    pub async fn download_video(&self, video_url: &str, dest: &str) -> Result<(), String> {
        validate_download_url(video_url)?;
        let resp = self.http.get(video_url).send().await.map_err(|e| format!("下载视频失败: {}", e))?;
        if resp.status() != 200 {
            return Err(format!("下载视频失败: HTTP {}", resp.status()));
        }
        let mut stream = resp.bytes_stream();
        let mut out = std::fs::File::create(dest).map_err(|e| format!("创建文件失败: {}", e))?;
        let mut downloaded: u64 = 0;
        while let Some(chunk) = stream.next().await {
            let bytes = chunk.map_err(|e| format!("读取视频流失败: {}", e))?;
            if downloaded + bytes.len() as u64 > 500 * 1024 * 1024 {
                return Err("视频过大".to_string());
            }
            out.write_all(&bytes)
                .map_err(|e| format!("写入失败: {}", e))?;
            downloaded += bytes.len() as u64;
        }
        out.flush().map_err(|e| format!("刷新失败: {}", e))?;
        Ok(())
    }

    async fn post_json(&self, full_url: &str, api_key: &str, body: &serde_json::Value) -> Result<Vec<u8>, String> {
        let json_body = serde_json::to_vec(body).map_err(|e| e.to_string())?;
        let resp = self.http.post(full_url)
            .header("Content-Type", "application/json")
            .header("Authorization", format!("Bearer {}", api_key))
            .body(json_body)
            .send()
            .await
            .map_err(|e| e.to_string())?;

        let status = resp.status();
        let bytes = resp.bytes().await.map_err(|e| e.to_string())?;
        if !status.is_success() {
            return Err(format!("HTTP {}: {}", status, String::from_utf8_lossy(&bytes)));
        }
        Ok(bytes.to_vec())
    }

    fn acquire_agnes_token(&self) {
        let mut state = self.rate_limiter.lock().unwrap();
        let now = Instant::now();
        if now.duration_since(state.agnes_since) > Duration::from_secs(60) {
            state.agnes_count = 0;
            state.agnes_since = now;
        }
        while state.agnes_count >= 6 {
            let wait = Duration::from_secs(60) - now.duration_since(state.agnes_since);
            if wait.as_secs() > 0 {
                std::thread::sleep(wait);
            }
            let now = Instant::now();
            if now.duration_since(state.agnes_since) > Duration::from_secs(60) {
                state.agnes_count = 0;
                state.agnes_since = now;
            }
        }
        state.agnes_count += 1;
    }
}

pub fn validate_download_url(raw: &str) -> Result<(), String> {
    let parsed = raw.parse::<reqwest::Url>()
        .map_err(|e| format!("无效URL: {}", e))?;
    if parsed.scheme() != "http" && parsed.scheme() != "https" {
        return Err("仅支持 http/https 协议".to_string());
    }
    let host = parsed.host_str().ok_or("URL 缺少 host")?;
    if host.is_empty() {
        return Err("URL 缺少 host".to_string());
    }
    // Guard against path-like hosts (e.g. "http:///path" parses host="path")
    if host.contains('/') || host.contains('\\') || host.contains(':') {
        return Err("URL 格式无效：host 包含非法字符".to_string());
    }
    if host == "localhost" {
        return Err("禁止下载内网/回环地址资源".to_string());
    }
    if let Ok(ip) = host.parse::<std::net::IpAddr>() {
        let is_loopback = ip.is_loopback() || ip.is_unspecified();
        let is_private_or_link_local = match ip {
            std::net::IpAddr::V4(v4) => v4.is_private() || v4.is_link_local(),
            std::net::IpAddr::V6(v6) => v6.is_loopback() || v6.is_unicast_link_local(),
        };
        if is_loopback || is_private_or_link_local {
            return Err("禁止下载内网/回环地址资源".to_string());
        }
    }
    Ok(())
}

pub fn is_transient_video_err(err: &str) -> bool {
    err.contains("DiffGenerator returned no result")
        || err.contains("no result")
        || err.contains("429")
        || err.contains("rate_limit")
        || err.contains("rate limit")
        || err.contains("503")
        || err.contains("video_queue_full")
}

fn base64_encode(data: &[u8]) -> String {
    use base64::{engine::general_purpose::STANDARD, Engine};
    STANDARD.encode(data)
}

fn base64_decode(data: &str) -> Result<Vec<u8>, String> {
    use base64::{engine::general_purpose::STANDARD, Engine};
    STANDARD.decode(data).map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_validate_download_url_accepts_public() {
        assert!(validate_download_url("https://example.com/image.jpg").is_ok());
        assert!(validate_download_url("http://cdn.example.com/v.mp4").is_ok());
        assert!(validate_download_url("https://images.unsplash.com/photo.jpg").is_ok());
    }

    #[test]
    fn test_validate_download_url_rejects_private() {
        assert!(validate_download_url("http://127.0.0.1/image.jpg").is_err());
        assert!(validate_download_url("http://192.168.1.1/image.jpg").is_err());
        assert!(validate_download_url("http://localhost/image.jpg").is_err());
        assert!(validate_download_url("http://10.0.0.1/image.jpg").is_err());
        // 172.16-31.x range
        assert!(validate_download_url("http://172.16.0.1/image.jpg").is_err());
        assert!(validate_download_url("http://172.31.255.255/image.jpg").is_err());
        assert!(validate_download_url("http://172.15.0.1/image.jpg").is_ok()); // not in private range
        // ::1 IPv6 loopback
        assert!(validate_download_url("http://::1/image.jpg").is_err());
        // fe80:: link-local (bare format)
        assert!(validate_download_url("http://fe80::1/image.jpg").is_err());
    }

    #[test]
    fn test_validate_download_url_rejects_non_http() {
        assert!(validate_download_url("ftp://example.com/file").is_err());
        assert!(validate_download_url("").is_err());
        assert!(validate_download_url("file:///etc/passwd").is_err());
        assert!(validate_download_url("data:text/plain;base64,SGVsbG8=").is_err());
        assert!(validate_download_url("javascript:alert(1)").is_err());
    }

    #[test]
    fn test_is_transient_video_err() {
        // DiffGenerator branch
        assert!(is_transient_video_err("DiffGenerator returned no result"));
        assert!(is_transient_video_err("some error DiffGenerator returned no result here"));
        // no result branch
        assert!(is_transient_video_err("no result found"));
        assert!(is_transient_video_err("task failed with no result"));
        // 429 branch
        assert!(is_transient_video_err("429 Too Many Requests"));
        // rate_limit branch
        assert!(is_transient_video_err("error: rate_limit exceeded"));
        // rate limit branch (space)
        assert!(is_transient_video_err("API rate limit hit"));
        // 503 branch
        assert!(is_transient_video_err("503 Service Unavailable"));
        // queue_full branch
        assert!(is_transient_video_err("video_queue_full, retry later"));
        // Non-transient
        assert!(!is_transient_video_err("permanent failure"));
        assert!(!is_transient_video_err("500 internal error"));
        assert!(!is_transient_video_err("invalid API key"));
        assert!(!is_transient_video_err("model not found"));
    }
}
