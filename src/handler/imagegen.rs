use axum::extract::{Form, Multipart, State};
use axum::http::{header, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::Json;
use serde::Deserialize;

use crate::config::Config;
use crate::service;
use crate::store::VideoJobStore;
use crate::util::{image_input_exts, new_id};
use crate::AppState;

#[derive(Deserialize)]
pub struct TextImageForm {
    prompt: String,
    width: Option<i32>,
    height: Option<i32>,
}

pub async fn handle_text_image(
    State(app): State<AppState>,
    Form(form): Form<TextImageForm>,
) -> impl IntoResponse {
    let cfg = &app.config;
    if form.prompt.trim().is_empty() {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "提示词不能为空"
        }))).into_response();
    }
    if form.prompt.len() > 2000 {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "提示词长度不能超过2000个字符"
        }))).into_response();
    }

    let width = form.width.unwrap_or(1024).clamp(1, 4096);
    let height = form.height.unwrap_or(1024).clamp(1, 4096);

    let tmp_dir = cfg.tmp_dir.join(format!("imggen_{}", new_id(8)));
    std::fs::create_dir_all(&tmp_dir).ok();

    let result = match tokio::task::spawn_blocking(move || {
        service::make_image(tmp_dir.to_str().unwrap(), &form.prompt, width, height)
    })
    .await {
        Ok(Ok(path)) => path,
        Ok(Err(e)) => {
            tracing::error!("图片生成失败: {}", e);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
        Err(e) => {
            tracing::error!("图片生成任务失败: {}", e);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
    };

    serve_image_file(&result)
}

pub async fn handle_edit_image(
    State(app): State<AppState>,
    mut multipart: Multipart,
) -> impl IntoResponse {
    let cfg = &app.config;
    let mut file_data: Option<Vec<u8>> = None;
    let mut file_name: Option<String> = None;
    let mut size_param: Option<String> = None;

    while let Ok(Some(field)) = multipart.next_field().await {
        let name = field.name().unwrap_or("").to_string();
        match name.as_str() {
            "image" => {
                let fname = field.file_name().map(|s| s.to_string());
                let data = field.bytes().await.unwrap_or_default();
                if data.len() > 20 * 1024 * 1024 {
                    return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                        "success": false,
                        "error": "图片超过 20MB 限制"
                    }))).into_response();
                }
                file_data = Some(data.to_vec());
                file_name = fname;
            }
            "size" => {
                size_param = field.text().await.ok();
            }
            _ => {}
        }
    }

    let data = file_data.unwrap_or_default();
    let fname = file_name.unwrap_or_default();
    let ext = std::path::Path::new(&fname)
        .extension()
        .map(|e| e.to_string_lossy().to_lowercase())
        .unwrap_or_default();
    let ext = ext.strip_prefix('.').unwrap_or(&ext).to_string();
    if !image_input_exts().contains(&ext.as_str()) {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": format!("不支持的图片类型: .{}", ext)
        }))).into_response();
    }

    // Validate it's actually an image
    if image::load_from_memory(&data).is_err() {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "图片解码失败"
        }))).into_response();
    }

    // Parse size
    let (width, height) = parse_size_param(&size_param);

    let tmp_dir = cfg.tmp_dir.join(format!("imgedit_{}", new_id(8)));
    std::fs::create_dir_all(&tmp_dir).ok();

    let src_path = tmp_dir.join(format!("upload.{}", ext));
    if let Err(_) = std::fs::write(&src_path, &data) {
        return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
            "success": false,
            "error": "服务器错误"
        }))).into_response();
    }

    let src_str = src_path.to_string_lossy().to_string();
    let result = match tokio::task::spawn_blocking(move || {
        service::make_edited_image(tmp_dir.to_str().unwrap(), &src_str, "", width, height)
    })
    .await {
        Ok(Ok(path)) => path,
        Ok(Err(e)) => {
            tracing::error!("图片编辑失败: {}", e);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
        Err(e) => {
            tracing::error!("图片编辑任务失败: {}", e);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
    };

    serve_image_file(&result)
}

pub async fn handle_compose_image(
    State(app): State<AppState>,
    mut multipart: Multipart,
) -> impl IntoResponse {
    let cfg = &app.config;
    let mut ref_paths: Vec<String> = Vec::new();
    let mut prompt: Option<String> = None;
    let tmp_dir = cfg.tmp_dir.join(format!("compose_{}", new_id(8)));
    std::fs::create_dir_all(&tmp_dir).ok();

    while let Ok(Some(field)) = multipart.next_field().await {
        let name = field.name().unwrap_or("");
        if name.starts_with("ref_") {
            let fname = field.file_name().map(|s| s.to_string());
            let data = match field.bytes().await {
                Ok(d) => d.to_vec(),
                Err(_) => continue,
            };
            if data.len() > 20 * 1024 * 1024 {
                continue;
            }
            let ext = match fname.as_deref() {
                Some(f) => {
                    std::path::Path::new(f)
                        .extension()
                        .map(|e| e.to_string_lossy().to_lowercase())
                        .unwrap_or_default()
                }
                None => String::new(),
            };
            let ext = ext.strip_prefix('.').unwrap_or(&ext).to_string();
            if !image_input_exts().contains(&ext.as_str()) {
                continue;
            }
            let ref_path = tmp_dir.join(format!("ref_{}.{}", ref_paths.len(), ext));
            std::fs::write(&ref_path, &data).ok();
            ref_paths.push(ref_path.to_string_lossy().to_string());
        } else if name == "prompt" {
            if let Ok(text) = field.text().await {
                prompt = Some(text);
            }
        }
    }

    if ref_paths.is_empty() {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "请至少上传一张参考图片"
        }))).into_response();
    }
    if ref_paths.len() > 4 {
        ref_paths.truncate(4);
    }

    let prompt = prompt.unwrap_or_default();

    let ref_refs: Vec<String> = ref_paths.iter().cloned().collect();
    let result = match tokio::task::spawn_blocking(move || {
        service::make_compose_image(
            tmp_dir.to_str().unwrap(),
            &prompt,
            &ref_refs.iter().map(|s| s.as_str()).collect::<Vec<_>>(),
            1024,
            1024,
        )
    })
    .await {
        Ok(Ok(path)) => path,
        Ok(Err(e)) => {
            tracing::error!("图片合成失败: {}", e);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
        Err(e) => {
            tracing::error!("图片合成任务失败: {}", e);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
    };

    serve_image_file(&result)
}

pub fn parse_size_param(s: &Option<String>) -> (i32, i32) {
    match s.as_deref() {
        Some("1k") | Some("1K") => (1024, 1024),
        Some("2k") | Some("2K") => (1792, 1792),
        Some("4k") | Some("4K") => (2048, 2048),
        _ => (1024, 1024),
    }
}

fn serve_image_file(path: &str) -> Response {
    let content = match std::fs::read(path) {
        Ok(c) => c,
        Err(_) => {
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "读取结果文件失败"
            }))).into_response();
        }
    };
    let content_type = mime_guess::from_path(path)
        .first_or_octet_stream()
        .to_string();
    (StatusCode::OK, [(header::CONTENT_TYPE, content_type)], content).into_response()
}
