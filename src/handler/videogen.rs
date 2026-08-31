use axum::extract::{Multipart, Path, State};
use axum::http::StatusCode;
use axum::response::IntoResponse;
use axum::Json;

use crate::service;
use crate::store::JobStatus;
use crate::util::new_id;
use crate::AppState;

const ASPECT_RATIOS: &[&str] = &["16:9", "9:16", "1:1", "4:3", "3:4", "2:3", "3:2", "21:9"];
const MAX_DURATION: i32 = 120;
const MIN_DURATION: i32 = 1;
const MAX_FRAME_SIZE: usize = 20 * 1024 * 1024;
const ALLOWED_IMAGE_EXTS: &[&str] = &["jpg", "jpeg", "png", "bmp", "tiff", "webp", "gif"];

fn sanitize_image_ext(filename: &Option<String>) -> String {
    let ext = filename
        .as_ref()
        .and_then(|f| std::path::Path::new(f).extension())
        .map(|e| e.to_string_lossy().to_lowercase())
        .unwrap_or_default();
    if ALLOWED_IMAGE_EXTS.contains(&ext.as_str()) {
        ext
    } else {
        String::new()
    }
}

pub async fn handle_text_video(
    State(app): State<AppState>,
    mut multipart: Multipart,
) -> impl IntoResponse {
    let cfg = &app.config;
    let mut prompt: Option<String> = None;
    let mut duration: Option<i32> = None;
    let mut aspect_ratio: Option<String> = None;

    while let Ok(Some(field)) = multipart.next_field().await {
        match field.name().unwrap_or("") {
            "prompt" => {
                if let Ok(text) = field.text().await {
                    prompt = Some(text);
                }
            }
            "duration" => {
                if let Ok(text) = field.text().await {
                    if let Ok(d) = text.trim().parse::<i32>() {
                        duration = Some(d);
                    }
                }
            }
            "aspect_ratio" => {
                if let Ok(text) = field.text().await {
                    aspect_ratio = Some(text.trim().to_string());
                }
            }
            _ => {}
        }
    }

    let prompt = match prompt {
        Some(p) if !p.trim().is_empty() => p,
        _ => {
            return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                "success": false,
                "error": "提示词不能为空"
            }))).into_response();
        }
    };

    let duration = duration.unwrap_or(5).clamp(MIN_DURATION, MAX_DURATION);
    let aspect_ratio = aspect_ratio
        .or_else(|| Some("16:9".to_string()))
        .filter(|r| ASPECT_RATIOS.contains(&r.as_str()))
        .unwrap_or("16:9".to_string());

    let job = app.video_jobs.create();
    let job_id = job.id.clone();
    let cfg = cfg.clone();
    let job_id_for_resp = job_id.clone();

    if !app.video_jobs.acquire_one_slot() {
        app.video_jobs.delete(&job_id);
        return (StatusCode::SERVICE_UNAVAILABLE, Json(serde_json::json!({
            "success": false,
            "error": "服务器繁忙，请稍后重试"
        }))).into_response();
    }

    let agnes_key = cfg.agnes_api_key.clone();
    let agnes_base = cfg.agnes_base_url.clone();
    let sensenova_key = cfg.sensenova_key.clone();
    let sensenova_base = cfg.sensenova_base.clone();

    tokio::spawn(async move {
        let tmp_dir = cfg.tmp_dir.join(format!("video_{}_{}", job_id, new_id(4)));
        if let Err(e) = tokio::fs::create_dir_all(&tmp_dir).await {
            app.video_jobs.set_error(&job_id, &format!("创建临时目录失败: {}", e));
            app.video_jobs.release_one_slot();
            return;
        }

        // Try AI path first if Agnes is configured
        if !agnes_key.is_empty() {
            let ai_client = service::AIClient::new(
                &agnes_base,
                &agnes_key,
                &sensenova_base,
                &sensenova_key,
                Some(app.video_jobs.clone()),
            );
            let video_path = if duration > 12 {
                service::make_long_text_video_ai(
                    &ai_client,
                    tmp_dir.to_str().unwrap(),
                    &prompt,
                    duration,
                    &aspect_ratio,
                ).await.ok()
            } else {
                service::make_text_video_ai(
                    &ai_client,
                    tmp_dir.to_str().unwrap(),
                    &prompt,
                    duration,
                    &aspect_ratio,
                ).await.ok()
            };
            if let Some(video_path) = video_path {
                if let Ok(dl_url) = app.file_store.register(&video_path, "video.mp4") {
                    app.video_jobs.set_complete(&job_id, &dl_url);
                } else {
                    app.video_jobs.set_error(&job_id, "保存视频失败");
                }
                app.video_jobs.release_one_slot();
                return;
            }
            app.video_jobs.set_notice(&job_id, "AI 生成失败，已降级为本地合成视频");
        } else {
            app.video_jobs.set_notice(&job_id, "AI 不可用，已降级为本地合成视频");
        }

        // Fallback to Python script
        let result = tokio::task::spawn_blocking(move || {
            service::make_text_video(tmp_dir.to_str().unwrap(), &prompt, duration)
        })
        .await;

        match result {
            Ok(Ok(path)) => {
                if let Ok(dl_url) = app.file_store.register(&path, "video.mp4") {
                    app.video_jobs.set_complete(&job_id, &dl_url);
                } else {
                    app.video_jobs.set_error(&job_id, "保存视频失败");
                }
            }
            Ok(Err(e)) => {
                tracing::error!("视频生成失败: {}", e);
                app.video_jobs.set_error(&job_id, &format!("视频生成失败: {}", e));
            }
            Err(e) => {
                tracing::error!("视频生成任务失败: {}", e);
                app.video_jobs.set_error(&job_id, "视频生成任务失败");
            }
        }
        app.video_jobs.release_one_slot();
    });

    (StatusCode::OK, Json(serde_json::json!({
        "success": true,
        "task_id": job_id_for_resp,
    }))).into_response()
}

pub async fn handle_keyframe_video(
    State(app): State<AppState>,
    
    mut multipart: Multipart,
) -> impl IntoResponse {
    let cfg = &app.config;
    let mut first_frame: Option<Vec<u8>> = None;
    let mut last_frame: Option<Vec<u8>> = None;
    let mut first_name: Option<String> = None;
    let mut last_name: Option<String> = None;
    let mut prompt: Option<String> = None;
    let mut duration: Option<i32> = None;
    let mut aspect_ratio: Option<String> = None;

    while let Ok(Some(field)) = multipart.next_field().await {
        match field.name().unwrap_or("") {
            "first_frame" => {
                let fname = field.file_name().map(|s| s.to_string());
                if let Ok(data) = field.bytes().await {
                    if data.len() <= MAX_FRAME_SIZE {
                        first_frame = Some(data.to_vec());
                        first_name = fname;
                    }
                }
            }
            "last_frame" => {
                let fname = field.file_name().map(|s| s.to_string());
                if let Ok(data) = field.bytes().await {
                    if data.len() <= MAX_FRAME_SIZE {
                        last_frame = Some(data.to_vec());
                        last_name = fname;
                    }
                }
            }
            "prompt" => {
                if let Ok(text) = field.text().await {
                    prompt = Some(text);
                }
            }
            "duration" => {
                if let Ok(text) = field.text().await {
                    if let Ok(d) = text.trim().parse::<i32>() {
                        duration = Some(d);
                    }
                }
            }
            "aspect_ratio" => {
                if let Ok(text) = field.text().await {
                    aspect_ratio = Some(text.trim().to_string());
                }
            }
            _ => {}
        }
    }

    let prompt = match prompt {
        Some(p) if !p.trim().is_empty() => p,
        _ => {
            return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                "success": false,
                "error": "提示词不能为空"
            }))).into_response();
        }
    };

    let (first_frame, last_frame) = match (first_frame, last_frame) {
        (Some(f), Some(l)) => (f, l),
        _ => {
            return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                "success": false,
                "error": "请上传首帧和尾帧图片"
            }))).into_response();
        }
    };

    let duration = duration.unwrap_or(5).clamp(MIN_DURATION, MAX_DURATION);
    let aspect_ratio = aspect_ratio
        .or_else(|| Some("16:9".to_string()))
        .filter(|r| ASPECT_RATIOS.contains(&r.as_str()))
        .unwrap_or("16:9".to_string());

    let job = app.video_jobs.create();
    let job_id = job.id.clone();
    let cfg = cfg.clone();
    let job_id_for_resp = job_id.clone();

    if !app.video_jobs.acquire_one_slot() {
        app.video_jobs.delete(&job_id);
        return (StatusCode::SERVICE_UNAVAILABLE, Json(serde_json::json!({
            "success": false,
            "error": "服务器繁忙，请稍后重试"
        }))).into_response();
    }

    let agnes_key = cfg.agnes_api_key.clone();
    let agnes_base = cfg.agnes_base_url.clone();
    let sensenova_key = cfg.sensenova_key.clone();
    let sensenova_base = cfg.sensenova_base.clone();

    tokio::spawn(async move {
        let tmp_dir = cfg.tmp_dir.join(format!("kf_video_{}_{}", job_id, new_id(4)));
        if let Err(e) = tokio::fs::create_dir_all(&tmp_dir).await {
            app.video_jobs.set_error(&job_id, &format!("创建临时目录失败: {}", e));
            app.video_jobs.release_one_slot();
            return;
        }

        // Save uploaded frames with sanitized extensions
        let first_ext = sanitize_image_ext(&first_name);
        let last_ext = sanitize_image_ext(&last_name);
        let first_path = tmp_dir.join(format!("first.{}", first_ext));
        let last_path = tmp_dir.join(format!("last.{}", last_ext));
        if let Err(e) = tokio::fs::write(&first_path, &first_frame).await {
            app.video_jobs.set_error(&job_id, &format!("保存首帧失败: {}", e));
            app.video_jobs.release_one_slot();
            return;
        }
        if let Err(e) = tokio::fs::write(&last_path, &last_frame).await {
            app.video_jobs.set_error(&job_id, &format!("保存尾帧失败: {}", e));
            app.video_jobs.release_one_slot();
            return;
        }

        // Upload frames to public dir so AI API can fetch them
        let first_url = match app.file_store.register(first_path.to_string_lossy().as_ref(), "first.png") {
            Ok(url) => cfg.base_url.clone() + &url,
            Err(_) => String::new(),
        };
        let last_url = match app.file_store.register(last_path.to_string_lossy().as_ref(), "last.png") {
            Ok(url) => cfg.base_url.clone() + &url,
            Err(_) => String::new(),
        };

        // Try AI path first
        if !agnes_key.is_empty() {
            let ai_client = service::AIClient::new(
                &agnes_base,
                &agnes_key,
                &sensenova_base,
                &sensenova_key,
                Some(app.video_jobs.clone()),
            );
            let video_path = if duration > 12 {
                service::make_long_keyframe_video_ai(
                    &ai_client,
                    tmp_dir.to_str().unwrap(),
                    &first_url,
                    &last_url,
                    &prompt,
                    duration,
                    &aspect_ratio,
                ).await.ok()
            } else {
                service::make_keyframe_video_ai(
                    &ai_client,
                    tmp_dir.to_str().unwrap(),
                    &first_url,
                    &last_url,
                    &prompt,
                    duration,
                    &aspect_ratio,
                ).await.ok()
            };
            if let Some(video_path) = video_path {
                if let Ok(dl_url) = app.file_store.register(&video_path, "keyframe_video.mp4") {
                    app.video_jobs.set_complete(&job_id, &dl_url);
                } else {
                    app.video_jobs.set_error(&job_id, "保存视频失败");
                }
                app.video_jobs.release_one_slot();
                return;
            }
            app.video_jobs.set_notice(&job_id, "AI 生成失败，已降级为本地合成视频");
        } else {
            app.video_jobs.set_notice(&job_id, "AI 不可用，已降级为本地合成视频");
        }

        // Fallback to Python
        let first_path_s = first_path.to_string_lossy().to_string();
        let last_path_s = last_path.to_string_lossy().to_string();
        let result = tokio::task::spawn_blocking(move || {
            service::make_keyframe_video(
                tmp_dir.to_str().unwrap(),
                &first_path_s,
                &last_path_s,
                &prompt,
                duration,
            )
        })
        .await;

        match result {
            Ok(Ok(path)) => {
                if let Ok(dl_url) = app.file_store.register(&path, "keyframe_video.mp4") {
                    app.video_jobs.set_complete(&job_id, &dl_url);
                } else {
                    app.video_jobs.set_error(&job_id, "保存视频失败");
                }
            }
            Ok(Err(e)) => {
                tracing::error!("关键帧视频生成失败: {}", e);
                app.video_jobs.set_error(&job_id, &format!("视频生成失败: {}", e));
            }
            Err(e) => {
                tracing::error!("关键帧视频生成任务失败: {}", e);
                app.video_jobs.set_error(&job_id, "视频生成任务失败");
            }
        }
        app.video_jobs.release_one_slot();
    });

    (StatusCode::OK, Json(serde_json::json!({
        "success": true,
        "task_id": job_id_for_resp,
    }))).into_response()
}

pub async fn handle_ref_video(
    State(app): State<AppState>,
    
    mut multipart: Multipart,
) -> impl IntoResponse {
    let cfg = &app.config;
    let mut ref_images: Vec<(Vec<u8>, String)> = Vec::new();
    let mut prompt: Option<String> = None;
    let mut duration: Option<i32> = None;
    let mut aspect_ratio: Option<String> = None;

    while let Ok(Some(field)) = multipart.next_field().await {
        match field.name().unwrap_or("") {
            name if name.starts_with("ref_") => {
                let fname = field.file_name().map(|s| s.to_string());
                if let Ok(data) = field.bytes().await {
                    if data.len() <= MAX_FRAME_SIZE {
                        let ext = sanitize_image_ext(&fname);
                        ref_images.push((data.to_vec(), ext));
                    }
                }
            }
            "prompt" => {
                if let Ok(text) = field.text().await {
                    prompt = Some(text);
                }
            }
            "duration" => {
                if let Ok(text) = field.text().await {
                    if let Ok(d) = text.trim().parse::<i32>() {
                        duration = Some(d);
                    }
                }
            }
            "aspect_ratio" => {
                if let Ok(text) = field.text().await {
                    aspect_ratio = Some(text.trim().to_string());
                }
            }
            _ => {}
        }
    }

    let prompt = match prompt {
        Some(p) if !p.trim().is_empty() => p,
        _ => {
            return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                "success": false,
                "error": "提示词不能为空"
            }))).into_response();
        }
    };

    if ref_images.is_empty() {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "请至少上传一张参考图片"
        }))).into_response();
    }
    if ref_images.len() > 5 {
        ref_images.truncate(5);
    }

    let duration = duration.unwrap_or(5).clamp(MIN_DURATION, MAX_DURATION);
    let aspect_ratio = aspect_ratio
        .or_else(|| Some("16:9".to_string()))
        .filter(|r| ASPECT_RATIOS.contains(&r.as_str()))
        .unwrap_or("16:9".to_string());

    let job = app.video_jobs.create();
    let job_id = job.id.clone();
    let cfg = cfg.clone();
    let job_id_for_resp = job_id.clone();

    if !app.video_jobs.acquire_one_slot() {
        app.video_jobs.delete(&job_id);
        return (StatusCode::SERVICE_UNAVAILABLE, Json(serde_json::json!({
            "success": false,
            "error": "服务器繁忙，请稍后重试"
        }))).into_response();
    }

    let agnes_key = cfg.agnes_api_key.clone();
    let agnes_base = cfg.agnes_base_url.clone();
    let sensenova_key = cfg.sensenova_key.clone();
    let sensenova_base = cfg.sensenova_base.clone();

    tokio::spawn(async move {
        let tmp_dir = cfg.tmp_dir.join(format!("ref_video_{}_{}", job_id, new_id(4)));
        if let Err(e) = tokio::fs::create_dir_all(&tmp_dir).await {
            app.video_jobs.set_error(&job_id, &format!("创建临时目录失败: {}", e));
            app.video_jobs.release_one_slot();
            return;
        }

        // Save reference images and upload to public dir
        let mut ref_paths: Vec<String> = Vec::new();
        let mut ref_urls: Vec<String> = Vec::new();
        for (i, (data, ext)) in ref_images.iter().enumerate() {
            let ref_path = tmp_dir.join(format!("ref_{}.{}", i, ext));
            if tokio::fs::write(&ref_path, data).await.is_ok() {
                ref_paths.push(ref_path.to_string_lossy().to_string());
                if let Ok(url) = app.file_store.register(ref_path.to_string_lossy().as_ref(), &format!("ref_{}.png", i)) {
                    ref_urls.push(cfg.base_url.clone() + &url);
                }
            }
        }

        if ref_paths.is_empty() {
            app.video_jobs.set_error(&job_id, "无有效参考图片");
            app.video_jobs.release_one_slot();
            return;
        }

        // Try AI path first
        if !agnes_key.is_empty() {
            let ai_client = service::AIClient::new(
                &agnes_base,
                &agnes_key,
                &sensenova_base,
                &sensenova_key,
                Some(app.video_jobs.clone()),
            );
            let video_path = if duration > 12 {
                service::make_long_ref_video_ai(
                    &ai_client,
                    tmp_dir.to_str().unwrap(),
                    &prompt,
                    &ref_urls,
                    duration,
                    &aspect_ratio,
                ).await.ok()
            } else {
                service::make_ref_video_ai(
                    &ai_client,
                    tmp_dir.to_str().unwrap(),
                    &prompt,
                    &ref_urls,
                    duration,
                    &aspect_ratio,
                ).await.ok()
            };
            if let Some(video_path) = video_path {
                if let Ok(dl_url) = app.file_store.register(&video_path, "ref_video.mp4") {
                    app.video_jobs.set_complete(&job_id, &dl_url);
                } else {
                    app.video_jobs.set_error(&job_id, "保存视频失败");
                }
                app.video_jobs.release_one_slot();
                return;
            }
            app.video_jobs.set_notice(&job_id, "AI 生成失败，已降级为本地合成视频");
        } else {
            app.video_jobs.set_notice(&job_id, "AI 不可用，已降级为本地合成视频");
        }

        // Fallback to Python
        let ref_strings: Vec<String> = ref_paths.clone();
        let result = tokio::task::spawn_blocking(move || {
            service::make_ref_video(
                tmp_dir.to_str().unwrap(),
                &prompt,
                &ref_strings,
                duration,
            )
        })
        .await;

        match result {
            Ok(Ok(path)) => {
                if let Ok(dl_url) = app.file_store.register(&path, "ref_video.mp4") {
                    app.video_jobs.set_complete(&job_id, &dl_url);
                } else {
                    app.video_jobs.set_error(&job_id, "保存视频失败");
                }
            }
            Ok(Err(e)) => {
                tracing::error!("参考视频生成失败: {}", e);
                app.video_jobs.set_error(&job_id, &format!("视频生成失败: {}", e));
            }
            Err(e) => {
                tracing::error!("参考视频生成任务失败: {}", e);
                app.video_jobs.set_error(&job_id, "视频生成任务失败");
            }
        }
        app.video_jobs.release_one_slot();
    });

    (StatusCode::OK, Json(serde_json::json!({
        "success": true,
        "task_id": job_id_for_resp,
    }))).into_response()
}

pub async fn handle_video_task_status(
    State(app): State<AppState>,
    Path(id): Path<String>,
) -> impl IntoResponse {
    if id.is_empty() {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "缺少任务ID"
        }))).into_response();
    }
    let job = match app.video_jobs.get(&id) {
        Some(j) => j,
        None => {
            return (StatusCode::NOT_FOUND, Json(serde_json::json!({
                "success": false,
                "error": "任务不存在或已过期"
            }))).into_response();
        }
    };

    let status_str = match job.status {
        JobStatus::Running => "running",
        JobStatus::Completed => "completed",
        JobStatus::Failed => "failed",
    };

    let mut resp = serde_json::json!({
        "id": job.id,
        "status": status_str,
    });
    if let Some(ref url) = job.download_url {
        resp["download_url"] = serde_json::Value::String(url.clone());
    }
    if let Some(ref err) = job.error {
        resp["error"] = serde_json::Value::String(err.clone());
    }
    if let Some(ref notice) = job.notice {
        resp["notice"] = serde_json::Value::String(notice.clone());
    }

    (StatusCode::OK, Json(resp)).into_response()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_aspect_ratio_whitelist() {
        for ratio in ASPECT_RATIOS {
            assert!(ASPECT_RATIOS.contains(ratio));
        }
        assert!(ASPECT_RATIOS.contains(&"2:3"));
        assert!(ASPECT_RATIOS.contains(&"3:2"));
        assert!(!ASPECT_RATIOS.contains(&"1:2"));
        assert!(!ASPECT_RATIOS.contains(&"invalid"));
    }

    #[test]
    fn test_sanitize_image_ext() {
        assert_eq!(sanitize_image_ext(&Some("image.png".to_string())), "png");
        assert_eq!(sanitize_image_ext(&Some("photo.jpg".to_string())), "jpg");
        assert_eq!(sanitize_image_ext(&Some("webcam.jpeg".to_string())), "jpeg");
        assert_eq!(sanitize_image_ext(&Some("anim.gif".to_string())), "gif");
        assert_eq!(sanitize_image_ext(&Some("anim.webp".to_string())), "webp");
        assert_eq!(sanitize_image_ext(&Some("bitmap.bmp".to_string())), "bmp");
        assert_eq!(sanitize_image_ext(&Some("script.sh".to_string())), "");
        assert_eq!(sanitize_image_ext(&Some("shell.exe".to_string())), "");
        assert_eq!(sanitize_image_ext(&Some("malicious.png/.sh".to_string())), "");
        assert_eq!(sanitize_image_ext(&Some("../../../etc/passwd".to_string())), "");
        assert_eq!(sanitize_image_ext(&None), "");
        assert_eq!(sanitize_image_ext(&Some("noext".to_string())), "");
    }
}
