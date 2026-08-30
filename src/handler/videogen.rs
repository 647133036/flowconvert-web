use axum::extract::{Multipart, Path, State};
use axum::http::StatusCode;
use axum::response::IntoResponse;
use axum::Json;

use crate::service;
use crate::store::JobStatus;
use crate::util::new_id;
use crate::AppState;

const ASPECT_RATIOS: &[&str] = &["16:9", "9:16", "1:1", "4:3", "3:4", "21:9"];
const MAX_DURATION: i32 = 120;
const MIN_DURATION: i32 = 1;

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
            if let Ok(video_path) = service::make_text_video_ai(
                &ai_client,
                tmp_dir.to_str().unwrap(),
                &prompt,
                duration,
                &aspect_ratio,
            ).await {
                if let Ok(dl_url) = app.file_store.register(&video_path, "video.mp4") {
                    app.video_jobs.set_complete(&job_id, &dl_url);
                } else {
                    app.video_jobs.set_error(&job_id, "保存视频失败");
                }
                app.video_jobs.release_one_slot();
                return;
            }
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
                    if data.len() <= 20 * 1024 * 1024 {
                        first_frame = Some(data.to_vec());
                        first_name = fname;
                    }
                }
            }
            "last_frame" => {
                let fname = field.file_name().map(|s| s.to_string());
                if let Ok(data) = field.bytes().await {
                    if data.len() <= 20 * 1024 * 1024 {
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

        // Save uploaded frames
        let first_ext = extract_ext(&first_name);
        let last_ext = extract_ext(&last_name);
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

        // Try AI path first
        if !agnes_key.is_empty() {
            let ai_client = service::AIClient::new(
                &agnes_base,
                &agnes_key,
                &sensenova_base,
                &sensenova_key,
                Some(app.video_jobs.clone()),
            );
            if let Ok(video_path) = service::make_keyframe_video_ai(
                &ai_client,
                tmp_dir.to_str().unwrap(),
                first_path.to_string_lossy().as_ref(),
                last_path.to_string_lossy().as_ref(),
                &prompt,
                duration,
                &aspect_ratio,
            ).await {
                if let Ok(dl_url) = app.file_store.register(&video_path, "keyframe_video.mp4") {
                    app.video_jobs.set_complete(&job_id, &dl_url);
                } else {
                    app.video_jobs.set_error(&job_id, "保存视频失败");
                }
                app.video_jobs.release_one_slot();
                return;
            }
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
                    if data.len() <= 20 * 1024 * 1024 {
                        let ext = extract_ext_opt(fname.as_deref());
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

        // Save reference images
        let mut ref_paths: Vec<String> = Vec::new();
        for (i, (data, ext)) in ref_images.iter().enumerate() {
            let ref_path = tmp_dir.join(format!("ref_{}.{}", i, ext));
            if tokio::fs::write(&ref_path, data).await.is_ok() {
                ref_paths.push(ref_path.to_string_lossy().to_string());
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
            if let Ok(video_path) = service::make_ref_video_ai(
                &ai_client,
                tmp_dir.to_str().unwrap(),
                &prompt,
                &ref_paths,
                duration,
                &aspect_ratio,
            ).await {
                if let Ok(dl_url) = app.file_store.register(&video_path, "ref_video.mp4") {
                    app.video_jobs.set_complete(&job_id, &dl_url);
                } else {
                    app.video_jobs.set_error(&job_id, "保存视频失败");
                }
                app.video_jobs.release_one_slot();
                return;
            }
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

    let resp = serde_json::json!({
        "id": job.id,
        "status": status_str,
    });
    let resp = if let Some(ref url) = job.download_url {
        serde_json::json!({
            "id": job.id,
            "status": status_str,
            "download_url": url,
        })
    } else if let Some(ref err) = job.error {
        serde_json::json!({
            "id": job.id,
            "status": status_str,
            "error": err,
        })
    } else if let Some(ref notice) = job.notice {
        serde_json::json!({
            "id": job.id,
            "status": status_str,
            "notice": notice,
        })
    } else {
        resp
    };

    (StatusCode::OK, Json(resp)).into_response()
}

fn extract_ext(filename: &Option<String>) -> String {
    filename
        .as_ref()
        .and_then(|f| std::path::Path::new(f).extension())
        .map(|e| e.to_string_lossy().to_lowercase())
        .unwrap_or_default()
}

fn extract_ext_opt(filename: Option<&str>) -> String {
    filename
        .and_then(|f| std::path::Path::new(f).extension())
        .map(|e| e.to_string_lossy().to_lowercase())
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_aspect_ratio_whitelist() {
        for ratio in ASPECT_RATIOS {
            assert!(ASPECT_RATIOS.contains(ratio));
        }
        assert!(!ASPECT_RATIOS.contains(&"3:2"));
        assert!(!ASPECT_RATIOS.contains(&"1:2"));
        assert!(!ASPECT_RATIOS.contains(&"invalid"));
    }

    #[test]
    fn test_extract_ext() {
        assert_eq!(extract_ext(&Some("image.png".to_string())), "png");
        assert_eq!(extract_ext(&Some("photo.JPEG".to_string())), "jpeg");
        assert_eq!(extract_ext(&None), "");
        assert_eq!(extract_ext(&Some("noext".to_string())), "");
    }

    #[test]
    fn test_extract_ext_opt() {
        assert_eq!(extract_ext_opt(Some("image.svg")), "svg");
        assert_eq!(extract_ext_opt(None), "");
    }
}
