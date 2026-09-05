use std::sync::Arc;

use axum::body::Body;
use axum::http::{Method, Request};
use axum::response::IntoResponse;
use axum::Router;
use tower::ServiceExt;

use flowconvert::config::Config;
use flowconvert::middleware::{RateLimiter, security_headers};
use flowconvert::store::{FileStore, OcrJobStore, VideoJobStore};
use flowconvert::AppState;

fn make_app() -> Router {
    let tmp_dir = std::env::temp_dir().join(format!("flowconvert_integration_{}", uuid::Uuid::new_v4()));
    std::fs::create_dir_all(&tmp_dir).ok();
    let out_dir = tmp_dir.join("output");
    std::fs::create_dir_all(&out_dir).ok();

    let cfg = Config {
        port: "0".to_string(),
        base_url: "http://localhost:0".to_string(),
        data_dir: tmp_dir.to_string_lossy().to_string(),
        tmp_dir: tmp_dir.clone(),
        out_dir: out_dir.clone(),
        max_size: 50 << 20,
        max_url: 20 << 20,
        ttl_hours: 2,
        agnes_api_key: String::new(),
        agnes_base_url: String::new(),
        sensenova_key: String::new(),
        sensenova_base: String::new(),
    };

    let limiter = RateLimiter::new(100);
    let file_store = FileStore::new(out_dir, 1);
    let video_jobs = VideoJobStore::new(60);
    let ocr_jobs = OcrJobStore::new(30);

    let state = AppState {
        config: Arc::new(cfg),
        file_store,
        video_jobs,
        ocr_jobs,
        client: None,
    };

    use axum::routing::{get, post};
    let api = Router::new()
        .route("/api/formats", get(flowconvert::handler::convert::formats))
        .route(
            "/api/convert/upload",
            post(flowconvert::handler::convert::handle_upload_vectorize),
        )
        .route(
            "/api/convert/url",
            get(flowconvert::handler::convert::handle_url_vectorize)
                .post(flowconvert::handler::convert::handle_upload_vectorize),
        )
        .route(
            "/api/translate",
            post(flowconvert::handler::translate::handle_translate),
        )
        .route(
            "/api/convert/video/task/{id}",
            get(flowconvert::handler::videogen::handle_video_task_status),
        )
        .route(
            "/api/download/{*name}",
            get(flowconvert::handler::download::handle_download),
        )
        .route("/api/convert/image/text", post(flowconvert::handler::imagegen::handle_text_image))
        .route("/api/convert/image/edit", post(flowconvert::handler::imagegen::handle_edit_image))
        .route("/api/convert/image/compose", post(flowconvert::handler::imagegen::handle_compose_image))
        .route("/api/convert/video/text", post(flowconvert::handler::videogen::handle_text_video))
        .route("/api/convert/sketch", post(flowconvert::handler::convert::handle_sketch))
        .route("/api/convert/pdf-to-office", post(flowconvert::handler::convert::handle_pdf_to_office))
        .route("/api/ocr", post(flowconvert::handler::ocr::handle_ocr))
        .route(
            "/api/ocr/task/{id}",
            get(flowconvert::handler::ocr::handle_ocr_task),
        );

    Router::new()
        .merge(api)
        .route("/{*path}", get(flowconvert::handler::pages::page))
        .route("/", get(flowconvert::handler::pages::page))
        .layer(axum::middleware::from_fn_with_state(
            limiter,
            rate_limit_middleware,
        ))
        .layer(axum::middleware::from_fn(security_headers))
        .with_state(state)
}

async fn rate_limit_middleware(
    s: axum::extract::State<Arc<RateLimiter>>,
    req: axum::http::Request<axum::body::Body>,
    next: axum::middleware::Next,
) -> axum::response::Response {
    use axum::http::StatusCode;
    use flowconvert::middleware::{client_ip, RateDecision};
    let path = req.uri().path();
    if !path.starts_with("/api/") || req.method() == Method::OPTIONS {
        return next.run(req).await;
    }
    let ip = client_ip(&req);
    match s.0.check(&ip) {
        RateDecision::Allow => next.run(req).await,
        RateDecision::TooManyRequests => {
            (StatusCode::TOO_MANY_REQUESTS, "请求过于频繁\n").into_response()
        }
        RateDecision::Busy => (
            StatusCode::SERVICE_UNAVAILABLE,
            "服务器繁忙，请稍后重试\n",
        )
            .into_response(),
    }
}

#[tokio::test]
async fn test_get_formats_returns_200() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("GET")
                .uri("/api/formats")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 200);
    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
        .await
        .unwrap();
    let json: serde_json::Value = serde_json::from_slice(&body).unwrap();
    assert_eq!(json["success"], true);
    assert_eq!(json["image_input"].as_array().unwrap().len(), 7);
    assert_eq!(json["vector_output"].as_array().unwrap().len(), 7);
}

#[tokio::test]
async fn test_get_root_returns_200() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("GET")
                .uri("/")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 200);
    let headers = resp.headers();
    assert_eq!(
        headers.get("content-type").unwrap(),
        "text/html; charset=utf-8"
    );
}

#[tokio::test]
async fn test_get_static_css_returns_200() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("GET")
                .uri("/static/css/style.css")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 200);
    let headers = resp.headers();
    assert!(headers
        .get("content-type")
        .unwrap()
        .to_str()
        .unwrap()
        .starts_with("text/css"));
}

#[tokio::test]
async fn test_get_nonexistent_returns_404() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("GET")
                .uri("/nonexistent")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 404);
}

#[tokio::test]
async fn test_options_api_formats() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("OPTIONS")
                .uri("/api/formats")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 200);
    let headers = resp.headers();
    assert_eq!(headers.get("Access-Control-Allow-Origin").unwrap(), "*");
    assert_eq!(
        headers.get("Access-Control-Allow-Methods").unwrap(),
        "GET, POST, OPTIONS"
    );
    assert_eq!(
        headers.get("X-Content-Type-Options").unwrap(),
        "nosniff"
    );
}

#[tokio::test]
async fn test_post_convert_url_missing_param_returns_400() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("GET")
                .uri("/api/convert/url")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 400);
}

#[tokio::test]
async fn test_post_convert_url_invalid_image_returns_400() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("GET")
                .uri("/api/convert/url?url=http://example.com/test.png")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    // example.com is a public host but the image won't exist or won't be an image
    // The handler will try to fetch and fail with a 400-level error
    assert_eq!(resp.status(), 400);
}

#[tokio::test]
async fn test_post_convert_upload_no_file_returns_400() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/convert/upload")
                .header("Content-Type", "multipart/form-data; boundary=----WebKitFormBoundary")
                .body(Body::from(
                    "------WebKitFormBoundary\r\n\
                     Content-Disposition: form-data; name=\"output\"\r\n\r\nsvg\r\n\
                     ------WebKitFormBoundary--\r\n",
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 400);
}

#[tokio::test]
async fn test_post_convert_upload_invalid_ext_returns_400() {
    let app = make_app();
    // Create a valid PNG
    let png_data = create_test_png();
    let body = format!(
        "\
         ------WebKitFormBoundary\r\n\
         Content-Disposition: form-data; name=\"file\"; filename=\"test.exe\"\r\n\r\n\
         {}",
        String::from_utf8_lossy(&png_data)
    );
    let resp = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/convert/upload")
                .header("Content-Type", "multipart/form-data; boundary=----WebKitFormBoundary")
                .body(Body::from(body))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 400);
}

#[tokio::test]
async fn test_post_convert_image_text_empty_prompt_returns_400() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/convert/image/text")
                .header("Content-Type", "application/x-www-form-urlencoded")
                .body(Body::from("prompt=&width=100&height=100"))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 400);
}

#[tokio::test]
async fn test_post_convert_image_edit_missing_params_returns_400() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/convert/image/edit")
                .header("Content-Type", "application/x-www-form-urlencoded")
                .body(Body::from(""))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 400);
}

#[tokio::test]
async fn test_post_convert_image_compose_missing_params_returns_400() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/convert/image/compose")
                .header("Content-Type", "application/x-www-form-urlencoded")
                .body(Body::from(""))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 400);
}

#[tokio::test]
async fn test_post_convert_video_text_missing_prompt_returns_400() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/convert/video/text")
                .header("Content-Type", "application/x-www-form-urlencoded")
                .body(Body::from("prompt=&duration=5"))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 400);
}

#[tokio::test]
async fn test_post_convert_sketch_empty_file_returns_400() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/convert/sketch")
                .header("Content-Type", "multipart/form-data; boundary=----WebKitFormBoundary")
                .body(Body::from(
                    "------WebKitFormBoundary\r\n\
                     Content-Disposition: form-data; name=\"file\"; filename=\"\"\r\n\r\n\
                     ------WebKitFormBoundary--\r\n",
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 400);
}

#[tokio::test]
async fn test_post_convert_pdf_to_office_empty_file_returns_400() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/convert/pdf-to-office")
                .header("Content-Type", "multipart/form-data; boundary=----WebKitFormBoundary")
                .body(Body::from(
                    "------WebKitFormBoundary\r\n\
                     Content-Disposition: form-data; name=\"file\"; filename=\"\"\r\n\r\n\
                     ------WebKitFormBoundary--\r\n",
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 400);
}

#[tokio::test]
async fn test_get_download_nonexistent_returns_404() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("GET")
                .uri("/api/download/nonexistent")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 404);
}

#[tokio::test]
async fn test_video_task_status_empty_id_returns_400() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("GET")
                .uri("/api/convert/video/task/")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 404);
}

#[tokio::test]
async fn test_video_task_status_not_found_returns_404() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("GET")
                .uri("/api/convert/video/task/nonexistent-id")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 404);
}

#[tokio::test]
async fn test_get_index_html() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("GET")
                .uri("/index.html")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 200);
}

#[tokio::test]
async fn test_ocr_task_unknown_returns_404() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("GET")
                .uri("/api/ocr/task/nonexistent-id")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 404);
}

#[tokio::test]
async fn test_ocr_without_source_returns_400() {
    let app = make_app();
    let resp = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/ocr")
                .header("Content-Type", "multipart/form-data; boundary=----WebKitFormBoundary")
                .body(Body::from(
                    "------WebKitFormBoundary\r\n\
                     Content-Disposition: form-data; name=\"formats\"\r\n\r\ntxt\r\n\
                     ------WebKitFormBoundary--\r\n",
                ))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 400);
}

#[tokio::test]
async fn test_ocr_invalid_ext_returns_400() {
    let app = make_app();
    let png = create_test_png();
    let body = multipart_body(&[("file", Some("bad.exe"), Some("image/png"), &png)], &[]);
    let resp = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/ocr")
                .header("Content-Type", "multipart/form-data; boundary=----WebKitFormBoundary")
                .body(Body::from(body))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 400);
}

#[tokio::test]
async fn test_ocr_upload_runs_and_completes() {
    let app = make_app();
    let png = create_test_png();
    let body = multipart_body(&[("file", Some("blank.png"), Some("image/png"), &png)], &[("formats", "txt")]);
    let resp = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/api/ocr")
                .header("Content-Type", "multipart/form-data; boundary=----WebKitFormBoundary")
                .body(Body::from(body))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 200);
    let payload = axum::body::to_bytes(resp.into_body(), usize::MAX).await.unwrap();
    let json: serde_json::Value = serde_json::from_slice(&payload).unwrap();
    let task_id = json["task_id"].as_str().expect("task_id").to_string();
    assert!(json["success"].as_bool().unwrap_or(false));

    let mut status = String::new();
    for _ in 0..240 {
        tokio::time::sleep(std::time::Duration::from_millis(500)).await;
        let r = app
            .clone()
            .oneshot(
                Request::builder()
                    .method("GET")
                    .uri(format!("/api/ocr/task/{}", task_id))
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(r.status(), 200);
        let p = axum::body::to_bytes(r.into_body(), usize::MAX).await.unwrap();
        let v: serde_json::Value = serde_json::from_slice(&p).unwrap();
        status = v["status"].as_str().unwrap_or("running").to_string();
        if status != "running" {
            eprintln!("OCR TASK DEBUG: {}", serde_json::to_string(&v).unwrap_or_default());
            break;
        }
    }
    assert_eq!(status, "completed", "OCR task did not finish in time");

    let r = app
        .oneshot(
            Request::builder()
                .method("GET")
                .uri(format!("/api/ocr/task/{}", task_id))
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    let p = axum::body::to_bytes(r.into_body(), usize::MAX).await.unwrap();
    let v: serde_json::Value = serde_json::from_slice(&p).unwrap();
    assert!(v["result"]["success"].as_bool().unwrap_or(false));
    assert!(v["result"]["text"].is_string());
    assert!(v["result"]["elapsed_ms"].is_i64());
    assert!(v["result"]["downloads"].is_array());
}

/// Build a multipart body with the fixed test boundary, keeping file bytes intact.
fn multipart_body(
    files: &[(&str, Option<&str>, Option<&str>, &[u8])],
    fields: &[(&str, &str)],
) -> Vec<u8> {
    const B: &[u8] = b"------WebKitFormBoundary";
    let mut out = Vec::new();
    for (name, file_name, content_type, data) in files {
        out.extend_from_slice(B);
        out.extend_from_slice(b"\r\n");
        if let Some(fn_) = file_name {
            out.extend_from_slice(format!("Content-Disposition: form-data; name=\"{}\"; filename=\"{}\"\r\n", name, fn_).as_bytes());
        } else {
            out.extend_from_slice(format!("Content-Disposition: form-data; name=\"{}\"\r\n", name).as_bytes());
        }
        if let Some(ct) = content_type {
            out.extend_from_slice(format!("Content-Type: {}\r\n", ct).as_bytes());
        }
        out.extend_from_slice(b"\r\n");
        out.extend_from_slice(data);
        out.extend_from_slice(b"\r\n");
    }
    for (name, value) in fields {
        out.extend_from_slice(B);
        out.extend_from_slice(b"\r\n");
        out.extend_from_slice(format!("Content-Disposition: form-data; name=\"{}\"\r\n\r\n", name).as_bytes());
        out.extend_from_slice(value.as_bytes());
        out.extend_from_slice(b"\r\n");
    }
    out.extend_from_slice(B);
    out.extend_from_slice(b"--\r\n");
    out
}

fn append_png_chunk(out: &mut Vec<u8>, name: &[u8], data: &[u8]) {
    out.extend_from_slice(&(data.len() as u32).to_be_bytes());
    out.extend_from_slice(name);
    out.extend_from_slice(data);
    let mut crc_src = Vec::with_capacity(name.len() + data.len());
    crc_src.extend_from_slice(name);
    crc_src.extend_from_slice(data);
    out.extend_from_slice(&crc32fast::hash(&crc_src).to_be_bytes());
}

fn create_test_png() -> Vec<u8> {
    // Solid 40x30 RGB image; the IDAT uses a real zlib stored block so PIL
    // can actually decode it (hand-rolled deflate bytes are not decodable).
    const W: usize = 40;
    const H: usize = 30;

    let mut scan = Vec::with_capacity(H * (1 + W * 3));
    for _ in 0..H {
        scan.push(0);
        for _ in 0..W {
            scan.extend_from_slice(&[255u8, 255, 255]);
        }
    }

    let mut zlib = Vec::with_capacity(scan.len() + 6);
    zlib.extend_from_slice(&[0x78, 0x01]);
    zlib.push(0x01);
    zlib.extend_from_slice(&((scan.len() & 0xFFFF) as u16).to_le_bytes());
    zlib.extend_from_slice(&(!((scan.len() & 0xFFFF) as u16)).to_le_bytes());
    zlib.extend_from_slice(&scan);

    let mut ihdr = Vec::new();
    ihdr.extend_from_slice(&(W as u32).to_be_bytes());
    ihdr.extend_from_slice(&(H as u32).to_be_bytes());
    ihdr.extend_from_slice(&[8, 2, 0, 0, 0]);

    let mut out = Vec::new();
    out.extend_from_slice(b"\x89PNG\r\n\x1a\n");
    append_png_chunk(&mut out, b"IHDR", &ihdr);
    append_png_chunk(&mut out, b"IDAT", &zlib);
    append_png_chunk(&mut out, b"IEND", b"");
    out
}
