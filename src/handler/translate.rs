use axum::extract::{Form, Multipart, State};
use axum::http::{header, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::Json;
use serde::Deserialize;

use crate::service;
use crate::util::new_id;
use crate::AppState;

#[derive(Deserialize)]
pub struct TranslateForm {
    pub text: String,
    pub source: String,
    pub target: String,
}

const LANGUAGES: &[&str] = &[
    "auto", "zh", "en", "ja", "ko", "fr", "de", "es", "ru", "pt",
    "it", "nl", "pl", "tr", "vi", "th", "ar", "hi", "id", "ms",
];

const ALLOWED_FILE_EXTS: &[&str] = &["txt", "pdf", "docx", "html", "htm", "xlsx", "pptx"];
const MAX_FILE_SIZE: u64 = 50 * 1024 * 1024;
const MAX_TEXT_LEN: usize = 5000;

/// Extract validation logic into a pure function for testing.
pub fn validate_translate_params(text: &str, source: &str, target: &str) -> Result<(), String> {
    if text.trim().is_empty() {
        return Err("请输入要翻译的文本".to_string());
    }
    if text.chars().count() > MAX_TEXT_LEN {
        return Err(format!("文本长度不能超过{}个字符", MAX_TEXT_LEN));
    }
    let source = if source.is_empty() || source == "auto" {
        "auto".to_string()
    } else {
        source.to_lowercase()
    };
    if !LANGUAGES.contains(&source.as_str()) && source != "auto" {
        return Err("不支持的源语言".to_string());
    }
    let target = target.to_lowercase();
    if !LANGUAGES.contains(&target.as_str()) {
        return Err("不支持的目标语言".to_string());
    }
    Ok(())
}

pub async fn handle_translate(
    State(app): State<AppState>,
    Form(form): Form<TranslateForm>,
) -> impl IntoResponse {
    let cfg = &app.config;
    if form.text.trim().is_empty() {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "请输入要翻译的文本"
        }))).into_response();
    }
    if form.text.chars().count() > MAX_TEXT_LEN {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": format!("文本长度不能超过{}个字符", MAX_TEXT_LEN)
        }))).into_response();
    }
    let source = if form.source.is_empty() || form.source == "auto" {
        "auto".to_string()
    } else {
        form.source.to_lowercase()
    };
    if !LANGUAGES.contains(&source.as_str()) && source != "auto" {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "不支持的源语言"
        }))).into_response();
    }
    let target = form.target.to_lowercase();
    if !LANGUAGES.contains(&target.as_str()) {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": "不支持的目标语言"
        }))).into_response();
    }

    let tmp_dir = cfg.tmp_dir.join(format!("translate_{}", new_id(8)));
    std::fs::create_dir_all(&tmp_dir).ok();

    let result = match tokio::task::spawn_blocking(move || {
        service::translate_text(&form.text, &source, &target)
    })
    .await {
        Ok(Ok(r)) => r,
        Ok(Err(e)) => {
            tracing::error!("翻译失败: {}", e);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
        Err(e) => {
            tracing::error!("翻译任务失败: {}", e);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
    };

    drop(tmp_dir);
    (StatusCode::OK, Json(serde_json::json!({
        "success": true,
        "text": result.text,
        "detected": result.detected,
        "engine": result.engine,
    }))).into_response()
}

pub async fn handle_translate_file(
    State(app): State<AppState>,
    mut multipart: Multipart,
) -> impl IntoResponse {
    let cfg = &app.config;
    let mut file_data: Option<Vec<u8>> = None;
    let mut file_name: Option<String> = None;
    let mut source: Option<String> = None;
    let mut target: Option<String> = None;

    while let Ok(Some(field)) = multipart.next_field().await {
        let name = field.name().unwrap_or("").to_string();
        if name == "file" {
            let fname = field.file_name().map(|s| s.to_string());
            let data = field.bytes().await.unwrap_or_default();
            if data.len() > MAX_FILE_SIZE as usize {
                return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                    "success": false,
                    "error": "文件超过 50MB 限制"
                }))).into_response();
            }
            file_data = Some(data.to_vec());
            file_name = fname;
        } else if name == "source" {
            if let Ok(text) = field.text().await {
                source = Some(text.trim().to_lowercase());
            }
        } else if name == "target" {
            if let Ok(text) = field.text().await {
                target = Some(text.trim().to_lowercase());
            }
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
    if !ALLOWED_FILE_EXTS.contains(&ext.as_str()) {
        return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
            "success": false,
            "error": format!("不支持的文件类型: .{}", ext)
        }))).into_response();
    }

    let tmp_dir = cfg.tmp_dir.join(format!("translate_file_{}", new_id(8)));
    std::fs::create_dir_all(&tmp_dir).ok();

    let src_path = tmp_dir.join(format!("upload.{}", ext));
    if let Err(_) = std::fs::write(&src_path, &data) {
        return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
            "success": false,
            "error": "服务器错误"
        }))).into_response();
    }

    let src_str = src_path.to_string_lossy().to_string();

    let source = match source {
        Some(s) if !s.is_empty() && s != "auto" => {
            if !LANGUAGES.contains(&s.as_str()) {
                return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                    "success": false,
                    "error": "不支持的源语言"
                }))).into_response();
            }
            s
        }
        _ => "auto".to_string(),
    };
    let target = match target {
        Some(t) if !t.is_empty() => {
            if !LANGUAGES.contains(&t.as_str()) {
                return (StatusCode::BAD_REQUEST, Json(serde_json::json!({
                    "success": false,
                    "error": "不支持的目标语言"
                }))).into_response();
            }
            t
        }
        _ => "zh".to_string(),
    };

    let result = match tokio::task::spawn_blocking(move || {
        service::translate_file(tmp_dir.to_str().unwrap(), &src_str, &source, &target)
    })
    .await {
        Ok(Ok(path)) => path,
        Ok(Err(e)) => {
            tracing::error!("文档翻译失败: {}", e);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
        Err(e) => {
            tracing::error!("文档翻译任务失败: {}", e);
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "处理失败，请稍后重试"
            }))).into_response();
        }
    };

    let content = match std::fs::read(&result) {
        Ok(c) => c,
        Err(_) => {
            return (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({
                "success": false,
                "error": "读取结果文件失败"
            }))).into_response();
        }
    };

    let content_type = mime_guess::from_path(&result).first_or_octet_stream().to_string();
    let disp = format!("attachment; filename=\"translated.{}\"", ext);

    let mut res = Response::new(axum::body::Body::from(content));
    res.headers_mut().insert(header::CONTENT_TYPE, content_type.parse().unwrap());
    res.headers_mut().insert(
        header::CONTENT_DISPOSITION,
        disp.parse().unwrap(),
    );
    res
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn validate_translate_empty_text() {
        assert!(validate_translate_params("", "en", "zh").is_err());
        assert!(validate_translate_params("   ", "en", "zh").is_err());
    }

    #[test]
    fn validate_translate_over_max_length() {
        let long_text = "a".repeat(MAX_TEXT_LEN + 1);
        assert!(validate_translate_params(&long_text, "en", "zh").is_err());
    }

    #[test]
    fn validate_translate_unsupported_source() {
        assert!(validate_translate_params("hello", "xx", "zh").is_err());
    }

    #[test]
    fn validate_translate_unsupported_target() {
        assert!(validate_translate_params("hello", "en", "xx").is_err());
    }

    #[test]
    fn validate_translate_valid_auto_source() {
        assert!(validate_translate_params("hello", "auto", "zh").is_ok());
    }

    #[test]
    fn validate_translate_valid_params() {
        assert!(validate_translate_params("hello world", "en", "zh").is_ok());
        assert!(validate_translate_params("你好", "zh", "en").is_ok());
        let exact = "a".repeat(MAX_TEXT_LEN);
        assert!(validate_translate_params(&exact, "en", "zh").is_ok());
    }
}
