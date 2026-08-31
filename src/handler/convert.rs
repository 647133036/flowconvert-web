use axum::extract::{Multipart, Query, State};
use axum::http::{header, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::Json;
use serde::Deserialize;

use crate::service;
use crate::util::{image_input_exts, new_id};
use crate::AppState;

pub const IMAGE_INPUT: [&str; 7] = ["jpg", "jpeg", "png", "bmp", "tiff", "webp", "gif"];
pub const VECTOR_OUTPUT: [&str; 7] = ["svg", "ai", "dxf", "eps", "fig", "sk", "pdf"];
pub const PDF_OUTPUT: [&str; 2] = ["docx", "xlsx"];

pub const MAX_PROMPT_LEN: usize = 2000;

#[derive(Deserialize)]
pub struct UrlVectorizeParams {
    pub url: Option<String>,
    pub output: Option<String>,
    pub mode: Option<String>,
    pub color_precision: Option<String>,
    pub filter_speckle: Option<String>,
    pub corner_threshold: Option<String>,
}

/// GET /api/formats — capability info for the frontend.
pub async fn formats() -> impl IntoResponse {
    (
        StatusCode::OK,
        Json(serde_json::json!({
            "success": true,
            "image_input": IMAGE_INPUT,
            "vector_output": VECTOR_OUTPUT,
            "pdf_output": PDF_OUTPUT,
            "max_upload_mb": 50,
            "max_url_mb": 20,
        })),
    )
}

/// POST /api/convert/upload — image to vector conversion via upload.
pub async fn handle_upload_vectorize(
    State(app): State<AppState>,
    mut multipart: Multipart,
) -> impl IntoResponse {
    let cfg = &app.config;
    let mut file_data: Option<Vec<u8>> = None;
    let mut file_name: Option<String> = None;
    let mut output: Option<String> = None;
    let mut mode: Option<String> = None;
    let mut color_precision: Option<String> = None;
    let mut filter_speckle: Option<String> = None;
    let mut corner_threshold: Option<String> = None;

    while let Ok(Some(field)) = multipart.next_field().await {
        match field.name().unwrap_or("") {
            "file" => {
                let fname = field.file_name().map(|s| s.to_string());
                if let Ok(data) = field.bytes().await {
                    if data.len() > 50 * 1024 * 1024 {
                        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                            "success": false,
                            "error": "文件超过 50MB 限制"
                        }))).into_response();
                    }
                    file_data = Some(data.to_vec());
                    file_name = fname;
                }
            }
            "output" => {
                if let Ok(text) = field.text().await {
                    output = Some(text);
                }
            }
            "mode" => {
                if let Ok(text) = field.text().await {
                    mode = Some(text);
                }
            }
            "color_precision" => {
                if let Ok(text) = field.text().await {
                    color_precision = Some(text);
                }
            }
            "filter_speckle" => {
                if let Ok(text) = field.text().await {
                    filter_speckle = Some(text);
                }
            }
            "corner_threshold" => {
                if let Ok(text) = field.text().await {
                    corner_threshold = Some(text);
                }
            }
            _ => {}
        }
    }

    let data = match file_data {
        Some(d) => d,
        None => {
            return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                "success": false,
                "error": "请选择要上传的文件"
            }))).into_response();
        }
    };

    let fname = file_name.unwrap_or_default();
    let ext = std::path::Path::new(&fname)
        .extension()
        .map(|e| e.to_string_lossy().to_lowercase())
        .unwrap_or_default();
    let ext = ext.strip_prefix('.').unwrap_or(&ext).to_string();
    if !image_input_exts().contains(&ext.as_str()) {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": format!("不支持的文件类型: .{}", ext)
        }))).into_response();
    }

    let preview = &data[..data.len().min(512)];
    let has_magic = is_valid_image_magic(preview);
    if !has_magic {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "不支持的文件类型"
        }))).into_response();
    }

    let output = output.unwrap_or_else(|| "svg".to_string());
    if valid_output(&output, "vector").is_none() {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "不支持的输出格式"
        }))).into_response();
    }

    let params = service::VecParams {
        mode: mode.unwrap_or_default(),
        color_precision: color_precision
            .as_deref()
            .and_then(|s| s.parse().ok())
            .unwrap_or(6),
        filter_speckle: filter_speckle
            .as_deref()
            .and_then(|s| s.parse().ok())
            .unwrap_or(4),
        corner_threshold: corner_threshold
            .as_deref()
            .and_then(|s| s.parse().ok())
            .unwrap_or(60),
    };

    let tmp_dir = cfg.tmp_dir.join(format!("vectorize_{}", new_id(8)));
    std::fs::create_dir_all(&tmp_dir).ok();

    let src_path = tmp_dir.join(format!("upload.{}", ext));
    if let Err(_) = std::fs::write(&src_path, &data) {
        return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
            "success": false,
            "error": "服务器错误"
        }))).into_response();
    }

    let src_str = src_path.to_string_lossy().to_string();
    let output_clone = output.clone();
    let result = match tokio::task::spawn_blocking(move || {
        service::vectorize(tmp_dir.to_str().unwrap(), &src_str, &output_clone, params)
    })
    .await {
        Ok(Ok(path)) => path,
        Ok(Err(e)) => {
            tracing::error!("矢量化失败: {}", e);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
        Err(e) => {
            tracing::error!("矢量化任务失败: {}", e);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
    };

    let fname_for_store = format!("converted.{}", output);
    let dl_url = match app.file_store.register(&result, &fname_for_store) {
        Ok(url) => url,
        Err(e) => {
            tracing::error!("保存矢量化文件失败: {}", e);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "保存文件失败"
            }))).into_response();
        }
    };

    (StatusCode::OK, Json(serde_json::json!({
        "success": true,
        "download_url": dl_url,
        "format": output,
    }))).into_response()
}

/// GET/POST /api/convert/url — vectorize from URL
pub async fn handle_url_vectorize(
    State(app): State<AppState>,
    Query(params): Query<UrlVectorizeParams>,
) -> impl IntoResponse {
    let cfg = &app.config;
    let url = match params.url {
        Some(u) if !u.is_empty() => u,
        _ => {
            return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                "success": false,
                "error": "请提供图片URL"
            }))).into_response();
        }
    };

    let output = match params.output {
        Some(o) if !o.is_empty() => o,
        _ => "svg".to_string(),
    };
    if valid_output(&output, "vector").is_none() {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "不支持的输出格式"
        }))).into_response();
    }

    let params = service::VecParams {
        mode: params.mode.unwrap_or_default(),
        color_precision: params
            .color_precision
            .as_deref()
            .and_then(|s| s.parse().ok())
            .unwrap_or(6),
        filter_speckle: params
            .filter_speckle
            .as_deref()
            .and_then(|s| s.parse().ok())
            .unwrap_or(4),
        corner_threshold: params
            .corner_threshold
            .as_deref()
            .and_then(|s| s.parse().ok())
            .unwrap_or(60),
    };

    let tmp_dir = cfg.tmp_dir.join(format!("url_vectorize_{}", new_id(8)));
    std::fs::create_dir_all(&tmp_dir).ok();

    let src = match service::fetch_image(tmp_dir.to_str().unwrap(), &url, cfg.max_size).await {
        Ok(s) => s,
        Err(e) => {
            return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                "success": false,
                "error": e
            }))).into_response();
        }
    };

    let output_clone = output.clone();
    let result = match tokio::task::spawn_blocking(move || {
        service::vectorize(tmp_dir.to_str().unwrap(), &src, &output_clone, params)
    })
    .await {
        Ok(Ok(path)) => path,
        Ok(Err(e)) => {
            tracing::error!("矢量化失败: {}", e);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
        Err(e) => {
            tracing::error!("矢量化任务失败: {}", e);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
    };

    let fname_for_store = format!("converted.{}", output);
    let dl_url = match app.file_store.register(&result, &fname_for_store) {
        Ok(url) => url,
        Err(e) => {
            tracing::error!("保存矢量化文件失败: {}", e);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "保存文件失败"
            }))).into_response();
        }
    };

    (StatusCode::OK, Json(serde_json::json!({
        "success": true,
        "download_url": dl_url,
        "format": output,
    }))).into_response()
}

/// POST /api/convert/pdf-to-office
pub async fn handle_pdf_to_office(
    State(app): State<AppState>,
    mut multipart: Multipart,
) -> impl IntoResponse {
    let cfg = &app.config;
    let mut file_data: Option<Vec<u8>> = None;
    let mut output: Option<String> = None;

    while let Ok(Some(field)) = multipart.next_field().await {
        match field.name().unwrap_or("") {
            "file" => {
                if let Ok(data) = field.bytes().await {
                    if data.len() > 50 * 1024 * 1024 {
                        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                            "success": false,
                            "error": "文件超过 50MB 限制"
                        }))).into_response();
                    }
                    file_data = Some(data.to_vec());
                }
            }
            "output" => {
                if let Ok(text) = field.text().await {
                    output = Some(text);
                }
            }
            _ => {}
        }
    }

    let data = match file_data {
        Some(d) => d,
        None => {
            return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                "success": false,
                "error": "请选择要上传的PDF文件"
            }))).into_response();
        }
    };

    if data.len() < 5 || !data.starts_with(b"%PDF-") {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "仅支持PDF文件"
        }))).into_response();
    }

    let output = output.unwrap_or_else(|| "docx".to_string());
    if valid_output(&output, "pdf").is_none() {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "不支持的输出格式"
        }))).into_response();
    }

    let tmp_dir = cfg.tmp_dir.join(format!("pdf2office_{}", new_id(8)));
    std::fs::create_dir_all(&tmp_dir).ok();
    let tmp_dir_clone = tmp_dir.clone();

    let src_path = tmp_dir.join("upload.pdf");
    if let Err(_) = std::fs::write(&src_path, &data) {
        return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
            "success": false,
            "error": "服务器错误"
        }))).into_response();
    }

    let output_clone = output.clone();
    let result = match tokio::task::spawn_blocking(move || {
        service::pdf_to_office(tmp_dir.to_str().unwrap(), src_path.to_str().unwrap(), &output_clone)
    })
    .await {
        Ok(Ok(path)) => path,
        Ok(Err(e)) => {
            tracing::error!("PDF转换失败: {}", e);
            let _ = std::fs::remove_dir_all(&tmp_dir_clone);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
        Err(e) => {
            tracing::error!("PDF转换任务失败: {}", e);
            let _ = std::fs::remove_dir_all(&tmp_dir_clone);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
    };

    let dl_url = match app.file_store.register(&result, &format!("converted.{}", output)) {
        Ok(url) => url,
        Err(e) => {
            tracing::error!("保存PDF转换文件失败: {}", e);
            let _ = std::fs::remove_dir_all(&tmp_dir_clone);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "保存文件失败"
            }))).into_response();
        }
    };

    let _ = std::fs::remove_dir_all(&tmp_dir_clone);

    (StatusCode::OK, Json(serde_json::json!({
        "success": true,
        "download_url": dl_url,
        "format": output,
    }))).into_response()
}

/// POST /api/convert/pdf-to-markdown
pub async fn handle_pdf_to_markdown(
    State(app): State<AppState>,
    mut multipart: Multipart,
) -> impl IntoResponse {
    let cfg = &app.config;
    let mut file_data: Option<Vec<u8>> = None;

    while let Ok(Some(field)) = multipart.next_field().await {
        match field.name().unwrap_or("") {
            "file" => {
                if let Ok(data) = field.bytes().await {
                    if data.len() > 50 * 1024 * 1024 {
                        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                            "success": false,
                            "error": "文件超过 50MB 限制"
                        }))).into_response();
                    }
                    file_data = Some(data.to_vec());
                }
            }
            _ => {}
        }
    }

    let data = match file_data {
        Some(d) => d,
        None => {
            return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                "success": false,
                "error": "请选择要上传的PDF文件"
            }))).into_response();
        }
    };

    if data.len() < 5 || !data.starts_with(b"%PDF-") {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "仅支持PDF文件"
        }))).into_response();
    }

    let tmp_dir = cfg.tmp_dir.join(format!("pdf2md_{}", new_id(8)));
    std::fs::create_dir_all(&tmp_dir).ok();
    let tmp_dir_clone = tmp_dir.clone();

    let src_path = tmp_dir.join("upload.pdf");
    if let Err(_) = std::fs::write(&src_path, &data) {
        return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
            "success": false,
            "error": "服务器错误"
        }))).into_response();
    }

    let result = match tokio::task::spawn_blocking(move || {
        service::pdf_to_markdown(tmp_dir.to_str().unwrap(), src_path.to_str().unwrap())
    })
    .await {
        Ok(Ok(path)) => path,
        Ok(Err(e)) => {
            tracing::error!("PDF转Markdown失败: {}", e);
            let _ = std::fs::remove_dir_all(&tmp_dir_clone);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
        Err(e) => {
            tracing::error!("PDF转Markdown任务失败: {}", e);
            let _ = std::fs::remove_dir_all(&tmp_dir_clone);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
    };

    let dl_url = match app.file_store.register(&result, "output.md") {
        Ok(url) => url,
        Err(e) => {
            tracing::error!("保存Markdown文件失败: {}", e);
            let _ = std::fs::remove_dir_all(&tmp_dir_clone);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "保存文件失败"
            }))).into_response();
        }
    };

    let _ = std::fs::remove_dir_all(&tmp_dir_clone);

    (StatusCode::OK, Json(serde_json::json!({
        "success": true,
        "download_url": dl_url,
    }))).into_response()
}

/// POST /api/convert/sketch
pub async fn handle_sketch(
    State(app): State<AppState>,
    mut multipart: Multipart,
) -> impl IntoResponse {
    let cfg = &app.config;
    let mut file_data: Option<Vec<u8>> = None;
    let mut sigma: Option<f64> = None;

    while let Ok(Some(field)) = multipart.next_field().await {
        match field.name().unwrap_or("") {
            "file" => {
                if let Ok(data) = field.bytes().await {
                    if data.len() > 50 * 1024 * 1024 {
                        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                            "success": false,
                            "error": "文件超过 50MB 限制"
                        }))).into_response();
                    }
                    file_data = Some(data.to_vec());
                }
            }
            "sigma" => {
                if let Ok(text) = field.text().await {
                    if let Ok(s) = text.trim().parse::<f64>() {
                        sigma = Some(s);
                    }
                }
            }
            _ => {}
        }
    }

    let data = match file_data {
        Some(d) => d,
        None => {
            return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                "success": false,
                "error": "请选择要上传的图片"
            }))).into_response();
        }
    };

    if !is_valid_image_magic(&data) {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "仅支持PNG/JPG/GIF/WebP/BMP图片"
        }))).into_response();
    }

    let sigma = sigma.unwrap_or(3.0);

    let tmp_dir = cfg.tmp_dir.join(format!("sketch_{}", new_id(8)));
    std::fs::create_dir_all(&tmp_dir).ok();
    let tmp_dir_clone = tmp_dir.clone();

    let src_path = tmp_dir.join("upload.png");
    if let Err(_) = std::fs::write(&src_path, &data) {
        let _ = std::fs::remove_dir_all(&tmp_dir_clone);
        return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
            "success": false,
            "error": "服务器错误"
        }))).into_response();
    }

    let result = match tokio::task::spawn_blocking(move || {
        service::make_sketch(tmp_dir.to_str().unwrap(), src_path.to_str().unwrap(), sigma)
    })
    .await {
        Ok(Ok(path)) => path,
        Ok(Err(e)) => {
            tracing::error!("素描生成失败: {}", e);
            let _ = std::fs::remove_dir_all(&tmp_dir_clone);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
        Err(e) => {
            tracing::error!("素描生成任务失败: {}", e);
            let _ = std::fs::remove_dir_all(&tmp_dir_clone);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
    };

    let dl_url = match app.file_store.register(&result, "sketch.png") {
        Ok(url) => url,
        Err(e) => {
            tracing::error!("保存素描文件失败: {}", e);
            let _ = std::fs::remove_dir_all(&tmp_dir_clone);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "保存文件失败"
            }))).into_response();
        }
    };

    let _ = std::fs::remove_dir_all(&tmp_dir_clone);

    (StatusCode::OK, Json(serde_json::json!({
        "success": true,
        "download_url": dl_url,
        "format": "png",
    }))).into_response()
}

/// POST /api/convert/idphoto
pub async fn handle_id_photo(
    State(app): State<AppState>,
    mut multipart: Multipart,
) -> impl IntoResponse {
    let cfg = &app.config;
    let mut file_data: Option<Vec<u8>> = None;
    let mut size: Option<String> = None;
    let mut bg_color: Option<String> = None;

    while let Ok(Some(field)) = multipart.next_field().await {
        match field.name().unwrap_or("") {
            "file" => {
                if let Ok(data) = field.bytes().await {
                    if data.len() > 50 * 1024 * 1024 {
                        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                            "success": false,
                            "error": "文件超过 50MB 限制"
                        }))).into_response();
                    }
                    file_data = Some(data.to_vec());
                }
            }
            "size" => {
                if let Ok(text) = field.text().await {
                    size = Some(text);
                }
            }
            "bg_color" => {
                if let Ok(text) = field.text().await {
                    bg_color = Some(text);
                }
            }
            _ => {}
        }
    }

    let data = match file_data {
        Some(d) => d,
        None => {
            return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                "success": false,
                "error": "请选择要上传的图片"
            }))).into_response();
        }
    };

    if !is_valid_image_magic(&data) {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "仅支持PNG/JPG/GIF/WebP/BMP图片"
        }))).into_response();
    }

    let ext = match &data[..data.len().min(512)] {
        d if d.starts_with(&[0x89, 0x50, 0x4E, 0x47]) => "png",
        d if d.starts_with(&[0xFF, 0xD8, 0xFF]) => "jpg",
        d if d.starts_with(&[0x52, 0x49, 0x46, 0x46]) && &d[8..12] == b"WEBP" => "webp",
        d if d.starts_with(&[0x47, 0x49, 0x46]) => "gif",
        d if d.starts_with(&[0x42, 0x4D]) => "bmp",
        _ => "png",
    };

    let tmp_dir = cfg.tmp_dir.join(format!("idphoto_{}", new_id(8)));
    std::fs::create_dir_all(&tmp_dir).ok();
    let tmp_dir_clone = tmp_dir.clone();

    let src_path = tmp_dir.join(format!("upload.{}", ext));
    if let Err(_) = std::fs::write(&src_path, &data) {
        let _ = std::fs::remove_dir_all(&tmp_dir_clone);
        return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
            "success": false,
            "error": "服务器错误"
        }))).into_response();
    }

    let size = size.unwrap_or_default();
    let bg_color = bg_color.unwrap_or_default();

    let result = match tokio::task::spawn_blocking(move || {
        service::make_id_photo(tmp_dir.to_str().unwrap(), src_path.to_str().unwrap(), &size, &bg_color)
    })
    .await {
        Ok(Ok(path)) => path,
        Ok(Err(e)) => {
            tracing::error!("证件照生成失败: {}", e);
            let _ = std::fs::remove_dir_all(&tmp_dir_clone);
            return (StatusCode::OK, Json(serde_json::json!({
                "success": false,
                "error": e
            }))).into_response();
        }
        Err(e) => {
            tracing::error!("证件照生成任务失败: {}", e);
            let _ = std::fs::remove_dir_all(&tmp_dir_clone);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
    };

    let content = match std::fs::read(&result) {
        Ok(c) => c,
        Err(_) => {
            let _ = std::fs::remove_dir_all(&tmp_dir_clone);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "读取结果文件失败"
            }))).into_response();
        }
    };

    let _ = std::fs::remove_dir_all(&tmp_dir_clone);

    let mut res = Response::new(axum::body::Body::from(content));
    res.headers_mut().insert(
        header::CONTENT_TYPE,
        "image/png".parse().unwrap(),
    );
    res.headers_mut().insert(
        header::CONTENT_DISPOSITION,
        "attachment; filename=\"证件照.png\"".parse().unwrap(),
    );
    res
}

pub fn valid_output(format: &str, kind: &str) -> Option<String> {
    match kind {
        "vector" if VECTOR_OUTPUT.contains(&format) => Some(format.to_string()),
        "pdf" if PDF_OUTPUT.contains(&format) => Some(format.to_string()),
        _ => None,
    }
}

pub fn valid_prompt(p: &str) -> Result<&str, String> {
    if p.len() > MAX_PROMPT_LEN {
        return Err(format!("提示词长度不能超过{}个字符", MAX_PROMPT_LEN));
    }
    Ok(p)
}

fn is_valid_image_magic(data: &[u8]) -> bool {
    if data.len() < 4 {
        return false;
    }
    if data.starts_with(&[0x89, 0x50, 0x4E, 0x47]) { return true; }
    if data.starts_with(&[0xFF, 0xD8, 0xFF]) { return true; }
    if data.starts_with(&[0x47, 0x49, 0x46]) { return true; }
    if data.len() >= 12 && data.starts_with(&[0x52, 0x49, 0x46, 0x46]) && &data[8..12] == b"WEBP" { return true; }
    if data.starts_with(&[0x42, 0x4D]) { return true; }
    false
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn valid_output_whitelist() {
        assert_eq!(valid_output("svg", "vector"), Some("svg".to_string()));
        assert_eq!(valid_output("dxf", "vector"), Some("dxf".to_string()));
        assert_eq!(valid_output("exe", "vector"), None);
        assert_eq!(valid_output("docx", "pdf"), Some("docx".to_string()));
        assert_eq!(valid_output("svg", "pdf"), None);
        assert_eq!(valid_output("svg", "other"), None);
    }

    #[test]
    fn valid_prompt_length() {
        assert!(valid_prompt("hello").is_ok());
        assert!(valid_prompt("").is_ok());
        let long = "a".repeat(MAX_PROMPT_LEN + 1);
        assert!(valid_prompt(&long).is_err());
        let exact = "a".repeat(MAX_PROMPT_LEN);
        assert!(valid_prompt(&exact).is_ok());
    }

    #[test]
    fn test_is_valid_image_magic_png() {
        let data = &[0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A];
        assert!(is_valid_image_magic(data));
    }

    #[test]
    fn test_is_valid_image_magic_jpg() {
        let data = &[0xFF, 0xD8, 0xFF, 0xE0];
        assert!(is_valid_image_magic(data));
    }

    #[test]
    fn test_is_valid_image_magic_gif() {
        let data = &[0x47, 0x49, 0x46, 0x38, 0x39, 0x61];
        assert!(is_valid_image_magic(data));
    }

    #[test]
    fn test_is_valid_image_magic_webp() {
        let data = &[0x52, 0x49, 0x46, 0x46, 0x00, 0x00, 0x00, 0x00, b'W', b'E', b'B', b'P'];
        assert!(is_valid_image_magic(data));
    }

    #[test]
    fn test_is_valid_image_magic_bmp() {
        let data = &[0x42, 0x4D, 0x00, 0x00];
        assert!(is_valid_image_magic(data));
    }

    #[test]
    fn test_is_valid_image_magic_rejects_non_image() {
        assert!(!is_valid_image_magic(b"not an image"));
        assert!(!is_valid_image_magic(b""));
        assert!(!is_valid_image_magic(&[0x00, 0x00, 0x00]));
        assert!(!is_valid_image_magic(&[0xEF, 0xBB, 0xBF])); // UTF-8 BOM
    }
}
