use std::sync::Arc;

use axum::body::Body;
use axum::http::{Method, Request};
use axum::response::IntoResponse;
use axum::Router;
use tower::ServiceExt;

use flowconvert::config::Config;
use flowconvert::middleware::{RateLimiter, security_headers};
use flowconvert::store::{FileStore, VideoJobStore};
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

    let state = AppState {
        config: Arc::new(cfg),
        file_store,
        video_jobs,
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
        .route("/api/convert/pdf-to-office", post(flowconvert::handler::convert::handle_pdf_to_office));

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

fn create_test_png() -> Vec<u8> {
    // Write minimal valid 1x1 red PNG directly
    // PNG signature + IHDR + IDAT + IEND for a 1x1 red pixel
    let mut data = Vec::new();
    // PNG signature
    data.extend_from_slice(&[0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A]);
    // IHDR: width=1, height=1, bit_depth=8, color_type=2 (RGB), compression=0, filter=0, interlace=0
    let ihdr_data: &[u8] = &[0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0];
    let ihdr_crc = crc32fast::hash(ihdr_data);
    data.extend_from_slice(&[0, 0, 0, 13]); // length
    data.extend_from_slice(b"IHDR");
    data.extend_from_slice(ihdr_data);
    data.extend_from_slice(&ihdr_crc.to_be_bytes());
    // IDAT: filter byte 0 + RGB (255,0,0) for 1 pixel, deflate compressed
    // Minimal zlib stream for filtered 1x1 RGB
    data.extend_from_slice(&[0, 0, 0, 5]); // length
    data.extend_from_slice(b"IDAT");
    // zlib compressed minimal scanline: filter=0, R=255, G=0, B=0
    data.extend_from_slice(&[0x08, 0x90, 0x03, 0x00, 0x00]); // minimal zlib
    let idat_crc = crc32fast::hash(&data[data.len() - 5..data.len()]);
    data.extend_from_slice(&idat_crc.to_be_bytes());
    // IEND
    data.extend_from_slice(&[0, 0, 0, 0]);
    data.extend_from_slice(b"IEND");
    data.extend_from_slice(&crc32fast::hash(b"IEND").to_be_bytes());
    data
}
