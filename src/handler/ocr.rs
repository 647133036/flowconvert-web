use std::collections::HashMap;
use std::path::PathBuf;

use axum::extract::{Multipart, Path, State};
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use axum::Json;
use serde_json::json;

use crate::service::ocr::{self, OcrOptions};
use crate::service::fetch_doc;
use crate::util::new_id;
use crate::AppState;

const OCR_EXTS: &[&str] = &["jpg", "jpeg", "png", "bmp", "tiff", "tif", "webp", "gif", "pdf"];
const DOWNLOAD_NAMES: [(&str, &str); 5] = [
    ("txt", "识别结果.txt"),
    ("docx", "识别结果.docx"),
    ("xlsx", "识别结果.xlsx"),
    ("json", "识别结果.json"),
    ("redbox", "红框标记图.png"),
];

fn is_true(v: &str) -> bool {
    matches!(
        v.trim().to_lowercase().as_str(),
        "1" | "true" | "yes" | "on"
    )
}

fn bad(msg: &str) -> Response {
    (
        StatusCode::BAD_REQUEST,
        Json(json!({ "success": false, "error": msg })),
    )
        .into_response()
}

fn server_error() -> Response {
    (
        StatusCode::INTERNAL_SERVER_ERROR,
        Json(json!({ "success": false, "error": "服务器错误" })),
    )
        .into_response()
}

/// Read multipart fields: at most one file plus repeatable text fields.
async fn read_multipart(
    multipart: &mut Multipart,
) -> Result<(Option<(Vec<u8>, String)>, HashMap<String, Vec<String>>), String> {
    let mut file: Option<(Vec<u8>, String)> = None;
    let mut fields: HashMap<String, Vec<String>> = HashMap::new();

    while let Some(mut field) = multipart
        .next_field()
        .await
        .map_err(|e| format!("请求格式错误: {e}"))?
    {
        let name = field.name().unwrap_or("").to_string();
        if let Some(file_name) = field.file_name() {
            if file.is_none() {
                let file_name = file_name.to_string();
                let mut buf = Vec::new();
                while let Some(chunk) = field
                    .chunk()
                    .await
                    .map_err(|e| format!("读取文件失败: {e}"))?
                {
                    buf.extend_from_slice(&chunk);
                }
                let ext = std::path::Path::new(&file_name)
                    .extension()
                    .map(|e| e.to_string_lossy().to_lowercase())
                    .unwrap_or_default();
                file = Some((buf, ext));
            }
            continue;
        }
        if let Ok(text) = field.text().await {
            fields.entry(name).or_default().push(text.trim().to_string());
        }
    }
    Ok((file, fields))
}

fn sanitize_ext(ext: &str) -> String {
    let lower = ext.to_lowercase();
    if OCR_EXTS.contains(&lower.as_str()) {
        lower
    } else {
        String::new()
    }
}

fn parse_options(fields: &HashMap<String, Vec<String>>) -> OcrOptions {
    let get = |key: &str| -> String {
        fields
            .get(key)
            .and_then(|vals| vals.first().cloned())
            .unwrap_or_default()
    };
    let mut formats: Vec<String> = Vec::new();
    if let Some(vals) = fields.get("formats") {
        for v in vals {
            for item in v.split(',') {
                let item = item.trim().to_lowercase();
                if !item.is_empty() {
                    formats.push(item);
                }
            }
        }
    }
    OcrOptions {
        mode: get("mode"),
        quality: get("quality"),
        charset: get("charset"),
        lang: get("lang"),
        engine: get("engine"),
        redbox: is_true(&get("redbox")),
        optimize: is_true(&get("optimize")),
        translate: is_true(&get("translate")),
        formats,
    }
}

/// POST /api/ocr — starts async recognition and returns {"success","task_id"}.
pub async fn handle_ocr(State(app): State<AppState>, mut multipart: Multipart) -> Response {
    let cfg = app.config.clone();
    let (file, fields) = match read_multipart(&mut multipart).await {
        Ok(v) => v,
        Err(e) => return bad(&e),
    };

    let tmp = cfg.tmp_dir.join(format!("ocr_{}", new_id(8)));
    let outdir = tmp.join("out");
    if std::fs::create_dir_all(&outdir).is_err() {
        return server_error();
    }

    let (src_path, ext, source) = if let Some((bytes, file_ext)) = file {
        let safe = sanitize_ext(&file_ext);
        if safe.is_empty() {
            return bad("不支持的文件类型，请上传图片或 PDF");
        }
        let path = tmp.join(format!("upload.{safe}"));
        if std::fs::write(&path, &bytes).is_err() {
            return server_error();
        }
        (path, safe, "file")
    } else if let Some(url) = fields.get("url").and_then(|v| v.first()) {
        if url.trim().is_empty() {
            return bad("请选择图片或 PDF 文件，或填写文档链接");
        }
        match fetch_doc(
            tmp.to_str().unwrap(),
            url.trim(),
            cfg.max_size,
            OCR_EXTS,
        )
        .await
        {
            Ok((path, file_ext)) => (PathBuf::from(path), file_ext, "url"),
            Err(e) => return bad(&e),
        }
    } else {
        return bad("请选择图片或 PDF 文件，或填写文档链接");
    };

    let mut opts = parse_options(&fields);
    if let Err(e) = ocr::normalize(&mut opts) {
        return bad(&e);
    }

    let job = app.ocr_jobs.create();
    let job_id = job.id.clone();
    if !app.ocr_jobs.acquire_one_slot() {
        app.ocr_jobs.delete(&job_id);
        return (
            StatusCode::SERVICE_UNAVAILABLE,
            Json(json!({ "success": false, "error": "服务器繁忙，请稍后重试" })),
        )
            .into_response();
    }

    let file_store = app.file_store.clone();
    let ocr_jobs = app.ocr_jobs.clone();
    let src_str = src_path.to_string_lossy().to_string();
    let out_str = outdir.to_string_lossy().to_string();
    let opts_json = serde_json::to_value(&opts).unwrap_or(serde_json::Value::Null);
    let opts_cloned = opts.clone();
    let source_cloned = source.to_string();
    let ext_cloned = ext.clone();

    tokio::spawn(async move {
        let (res, run_err) = match tokio::task::spawn_blocking(
            move || ocr::ocr_file(&src_str, &opts_cloned, &out_str),
        )
        .await
        {
            Ok(Ok(r)) => (Some(r), None),
            Ok(Err(e)) => (None, Some(e)),
            Err(e) => (None, Some(format!("OCR 任务异常: {e}"))),
        };

        let payload = match res {
            Some(r) => {
                let mut downloads = Vec::new();
                for (key, name) in DOWNLOAD_NAMES.iter() {
                    let Some(path) = r.files.get(*key) else { continue };
                    match file_store.register(path, name) {
                        Ok(url) => downloads.push(json!({
                            "name": name,
                            "type": key,
                            "url": url,
                        })),
                        Err(e) => tracing::warn!("OCR 下载注册失败 {key}: {e}"),
                    }
                }
                Some(json!({
                    "success": true,
                    "text": r.text,
                    "translated_text": r.translated_text,
                    "downloads": downloads,
                    "engine": r.engine,
                    "pages": r.pages,
                    "chars": r.chars,
                    "charset": r.charset,
                    "from_text_layer": r.from_text_layer,
                    "layout": r.layout,
                    "blocks": r.blocks,
                    "lines": r.lines,
                    "filtered_headers": r.filtered_headers,
                    "warnings": r.warnings,
                    "elapsed_ms": r.elapsed_ms,
                    "options": opts_json,
                    "source": source_cloned,
                    "ext": ext_cloned,
                    "empty": r.text.trim().is_empty(),
                }))
            }
            None => None,
        };

        let _ = std::fs::remove_dir_all(&tmp);
        ocr_jobs.release_one_slot();

        match payload {
            Some(p) => ocr_jobs.set_complete(&job_id, p),
            None => {
                tracing::error!("[OCR {job_id}] error: {}", run_err.unwrap_or_default());
                ocr_jobs.set_error(&job_id, "识别失败，请稍后重试");
            }
        }
    });

    Json(json!({ "success": true, "task_id": job.id }))
        .into_response()
}

/// GET /api/ocr/task/{id} — status and result of an async OCR job.
pub async fn handle_ocr_task(
    State(app): State<AppState>,
    Path(id): Path<String>,
) -> Response {
    let id = id.trim_matches('/');
    if id.is_empty() {
        return bad("缺少任务ID");
    }
    let Some(job) = app.ocr_jobs.get(id) else {
        return (
            StatusCode::NOT_FOUND,
            Json(json!({ "success": false, "error": "任务不存在或已过期" })),
        )
            .into_response();
    };
    Json(json!({
        "success": true,
        "status": job.status,
        "error": job.error,
        "result": job.result,
    }))
        .into_response()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_is_true_spellings() {
        for v in ["1", "true", "True", "YES", "on", " on "] {
            assert!(is_true(v));
        }
        for v in ["", "0", "false", "no", "off", "x"] {
            assert!(!is_true(v));
        }
    }

    #[test]
    fn test_sanitize_ext() {
        assert_eq!(sanitize_ext("PDF"), "pdf");
        assert_eq!(sanitize_ext("png"), "png");
        assert_eq!(sanitize_ext("exe"), "");
        assert_eq!(sanitize_ext("../etc"), "");
        assert_eq!(sanitize_ext(""), "");
    }

    #[test]
    fn test_parse_options_collects_formats() {
        let mut fields: HashMap<String, Vec<String>> = HashMap::new();
        fields.insert("mode".into(), vec!["advanced".into()]);
        fields.insert("formats".into(), vec!["docx,redbox".into(), "json".into()]);
        fields.insert("translate".into(), vec!["1".into()]);
        let o = parse_options(&fields);
        assert_eq!(o.mode, "advanced");
        assert_eq!(o.formats, vec!["docx", "redbox", "json"]);
        assert!(o.translate);
        assert!(!o.redbox);
    }

    #[test]
    fn test_parse_options_defaults_empty() {
        let fields: HashMap<String, Vec<String>> = HashMap::new();
        let o = parse_options(&fields);
        assert!(o.mode.is_empty());
        assert!(o.formats.is_empty());
    }
}
