use axum::extract::{Multipart, State};
use axum::http::StatusCode;
use axum::response::IntoResponse;
use axum::Json;

use crate::service;
use crate::util::{image_input_exts, new_id};
use crate::AppState;

pub async fn handle_text_image(
    State(app): State<AppState>,
    mut multipart: Multipart,
) -> impl IntoResponse {
    let cfg = &app.config;
    let mut prompt: Option<String> = None;
    let mut width: Option<i32> = None;
    let mut height: Option<i32> = None;

    while let Ok(Some(field)) = multipart.next_field().await {
        match field.name().unwrap_or("") {
            "prompt" => {
                if let Ok(text) = field.text().await {
                    prompt = Some(text);
                }
            }
            "width" => {
                if let Ok(text) = field.text().await {
                    if let Ok(w) = text.trim().parse::<i32>() {
                        width = Some(w);
                    }
                }
            }
            "height" => {
                if let Ok(text) = field.text().await {
                    if let Ok(h) = text.trim().parse::<i32>() {
                        height = Some(h);
                    }
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
    if prompt.len() > 2000 {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "提示词长度不能超过2000个字符"
        }))).into_response();
    }

    let width = width.unwrap_or(1024).clamp(1, 4096);
    let height = height.unwrap_or(1024).clamp(1, 4096);

    let tmp_dir = cfg.tmp_dir.join(format!("imggen_{}", new_id(8)));
    std::fs::create_dir_all(&tmp_dir).ok();

    // Try AI path first
    if let Some(ref client) = app.client {
        if let Ok(path) = service::make_image_ai(&client, &tmp_dir.to_string_lossy(), &prompt, width, height).await {
            if let Ok(dl_url) = app.file_store.register(&path, "generated.png") {
                return (StatusCode::OK, Json(serde_json::json!({
                    "success": true,
                    "download_url": dl_url,
                    "format": "png",
                    "width": width,
                    "height": height,
                }))).into_response();
            }
        }
    }

    // Fallback to procedural
    let tmp_str = tmp_dir.to_string_lossy().to_string();
    let result = match tokio::task::spawn_blocking(move || {
        service::make_image(&tmp_str, &prompt, width, height)
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

    if let Ok(dl_url) = app.file_store.register(&result, "generated.png") {
        (StatusCode::OK, Json(serde_json::json!({
            "success": true,
            "download_url": dl_url,
            "format": "png",
            "width": width,
            "height": height,
        }))).into_response()
    } else {
        (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
            "success": false,
            "error": "保存失败"
        }))).into_response()
    }
}

pub async fn handle_edit_image(
    State(app): State<AppState>,
    mut multipart: Multipart,
) -> impl IntoResponse {
    let cfg = &app.config;
    let mut file_data: Option<Vec<u8>> = None;
    let mut file_name: Option<String> = None;
    let mut size_param: Option<String> = None;
    let mut prompt: Option<String> = None;

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
            "prompt" => {
                if let Ok(text) = field.text().await {
                    prompt = Some(text);
                }
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
    if data.is_empty() {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "请选择要编辑的图像"
        }))).into_response();
    }
    if image::load_from_memory(&data).is_err() {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "图片解码失败"
        }))).into_response();
    }

    let prompt = match prompt {
        Some(p) if !p.trim().is_empty() => p,
        _ => {
            return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                "success": false,
                "error": "请输入编辑描述"
            }))).into_response();
        }
    };
    if prompt.len() > 2000 {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "提示词长度不能超过2000个字符"
        }))).into_response();
    }

    let (width, height) = parse_size_param(&size_param);

    let tmp_dir = cfg.tmp_dir.join(format!("imgedit_{}", new_id(8)));
    std::fs::create_dir_all(&tmp_dir).ok();

    let src_path = tmp_dir.join(format!("upload.{}", ext));
    if std::fs::write(&src_path, &data).is_err() {
        return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
            "success": false,
            "error": "服务器错误"
        }))).into_response();
    }

    let src_str = src_path.to_string_lossy().to_string();
    let tmp_str = tmp_dir.to_string_lossy().to_string();

    // Try AI path first
    if let Some(ref client) = app.client {
        if let Ok(path) = service::make_edited_image_ai(&client, &tmp_str, &src_str, &prompt, width, height).await {
            if let Ok(dl_url) = app.file_store.register(&path, "edited.png") {
                return (StatusCode::OK, Json(serde_json::json!({
                    "success": true,
                    "download_url": dl_url,
                    "format": "png",
                }))).into_response();
            }
        }
    }

    // Fallback to procedural
    let result = match tokio::task::spawn_blocking(move || {
        service::make_edited_image(&tmp_str, &src_str, &prompt, width, height)
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

    if let Ok(dl_url) = app.file_store.register(&result, "edited.png") {
        (StatusCode::OK, Json(serde_json::json!({
            "success": true,
            "download_url": dl_url,
            "format": "png",
        }))).into_response()
    } else {
        (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
            "success": false,
            "error": "保存失败"
        }))).into_response()
    }
}

pub async fn handle_compose_image(
    State(app): State<AppState>,
    mut multipart: Multipart,
) -> impl IntoResponse {
    let cfg = &app.config;
    let mut ref_images: Vec<(Vec<u8>, String)> = Vec::new();
    let mut prompt: Option<String> = None;
    let mut width: Option<i32> = None;
    let mut height: Option<i32> = None;

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
            ref_images.push((data, ext));
        } else if name == "prompt" {
            if let Ok(text) = field.text().await {
                prompt = Some(text);
            }
        } else if name == "width" {
            if let Ok(text) = field.text().await {
                if let Ok(w) = text.trim().parse::<i32>() {
                    width = Some(w);
                }
            }
        } else if name == "height" {
            if let Ok(text) = field.text().await {
                if let Ok(h) = text.trim().parse::<i32>() {
                    height = Some(h);
                }
            }
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
    if prompt.len() > 2000 {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "提示词长度不能超过2000个字符"
        }))).into_response();
    }

    if ref_images.is_empty() {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "请至少上传一张参考图片"
        }))).into_response();
    }
    if ref_images.len() > 4 {
        ref_images.truncate(4);
    }

    let width = width.unwrap_or(1024).clamp(1, 4096);
    let height = height.unwrap_or(1024).clamp(1, 4096);

    let tmp_dir = cfg.tmp_dir.join(format!("compose_{}", new_id(8)));
    std::fs::create_dir_all(&tmp_dir).ok();

    let mut ref_paths: Vec<String> = Vec::new();
    for (i, (data, ext)) in ref_images.iter().enumerate() {
        let ref_path = tmp_dir.join(format!("ref_{}.{}", i, ext));
        if std::fs::write(&ref_path, data).is_ok() {
            ref_paths.push(ref_path.to_string_lossy().to_string());
        }
    }

    if ref_paths.is_empty() {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "无有效参考图片"
        }))).into_response();
    }

    let tmp_str = tmp_dir.to_string_lossy().to_string();
    let prompt = prompt.clone();

    // Try AI path first
    if let Some(ref client) = app.client {
        if let Ok(path) = service::make_compose_image_ai(&client, &tmp_str, &prompt, &ref_paths, width, height).await {
            if let Ok(dl_url) = app.file_store.register(&path, "composed.png") {
                return (StatusCode::OK, Json(serde_json::json!({
                    "success": true,
                    "download_url": dl_url,
                    "format": "png",
                    "width": width,
                    "height": height,
                }))).into_response();
            }
        }
    }

    // Fallback to procedural
    let ref_refs: Vec<String> = ref_paths.iter().cloned().collect();
    let result = match tokio::task::spawn_blocking(move || {
        service::make_compose_image(
            &tmp_str,
            &prompt,
            &ref_refs.iter().map(|s| s.as_str()).collect::<Vec<_>>(),
            width,
            height,
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

    if let Ok(dl_url) = app.file_store.register(&result, "composed.png") {
        (StatusCode::OK, Json(serde_json::json!({
            "success": true,
            "download_url": dl_url,
            "format": "png",
            "width": width,
            "height": height,
        }))).into_response()
    } else {
        (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
            "success": false,
            "error": "保存失败"
        }))).into_response()
    }
}

pub fn parse_size_param(s: &Option<String>) -> (i32, i32) {
    match s.as_deref() {
        Some("1k") | Some("1K") => (1024, 1024),
        Some("2k") | Some("2K") => (1792, 1024),
        Some("4k") | Some("4K") => (2048, 2048),
        _ => (1024, 1024),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_size_param() {
        assert_eq!(parse_size_param(&Some("1k".to_string())), (1024, 1024));
        assert_eq!(parse_size_param(&Some("2k".to_string())), (1792, 1024));
        assert_eq!(parse_size_param(&Some("4k".to_string())), (2048, 2048));
        assert_eq!(parse_size_param(&None), (1024, 1024));
    }
}
