pub mod config;
pub mod handler;
pub mod middleware;
pub mod service;
pub mod store;
pub mod util;

use std::sync::Arc;

use axum::extract::DefaultBodyLimit;
use axum::middleware as axum_mw;
use axum::Router;

use config::Config;
use handler::{download, imagegen, pages, translate, videogen};
use middleware::{security_headers, RateDecision, RateLimiter, MAX_API_BODY};
use store::{FileStore, VideoJobStore};

pub async fn main_inner() {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new("info")),
        )
        .init();

    let cfg = Config::load();
    if let Err(e) = cfg.ensure_dirs() {
        eprintln!("初始化数据目录失败: {e}");
        std::process::exit(1);
    }

    let limiter = RateLimiter::new(100);
    limiter.spawn_cleanup();

    let file_store = FileStore::new(cfg.out_dir.clone(), cfg.ttl_hours as u64);
    let video_jobs = VideoJobStore::new(60);

    let client = if !cfg.agnes_api_key.is_empty() || !cfg.sensenova_key.is_empty() {
        Some(Arc::new(service::AIClient::new(
            &cfg.agnes_base_url,
            &cfg.agnes_api_key,
            &cfg.sensenova_base,
            &cfg.sensenova_key,
            Some(video_jobs.clone()),
        )))
    } else {
        None
    };

    let state = AppState {
        config: Arc::new(cfg),
        file_store,
        video_jobs,
        client,
    };

    let api = Router::new()
        .route("/api/formats", axum::routing::get(handler::convert::formats))
        .route(
            "/api/convert/upload",
            axum::routing::post(handler::convert::handle_upload_vectorize),
        )
        .route(
            "/api/convert/url",
            axum::routing::get(handler::convert::handle_url_vectorize)
                .post(handler::convert::handle_upload_vectorize),
        )
        .route(
            "/api/convert/pdf-to-office",
            axum::routing::post(handler::convert::handle_pdf_to_office),
        )
        .route(
            "/api/convert/pdf-to-markdown",
            axum::routing::post(handler::convert::handle_pdf_to_markdown),
        )
        .route(
            "/api/convert/sketch",
            axum::routing::post(handler::convert::handle_sketch),
        )
        .route(
            "/api/convert/idphoto",
            axum::routing::post(handler::convert::handle_id_photo),
        )
        .route(
            "/api/translate",
            axum::routing::post(translate::handle_translate),
        )
        .route(
            "/api/translate/file",
            axum::routing::post(translate::handle_translate_file),
        )
        .route(
            "/api/convert/image/text",
            axum::routing::post(imagegen::handle_text_image),
        )
        .route(
            "/api/convert/image/edit",
            axum::routing::post(imagegen::handle_edit_image),
        )
        .route(
            "/api/convert/image/compose",
            axum::routing::post(imagegen::handle_compose_image),
        )
        .route(
            "/api/convert/video/text",
            axum::routing::post(videogen::handle_text_video),
        )
        .route(
            "/api/convert/video/keyframe",
            axum::routing::post(videogen::handle_keyframe_video),
        )
        .route(
            "/api/convert/video/ref",
            axum::routing::post(videogen::handle_ref_video),
        )
        .route(
            "/api/convert/video/task/{id}",
            axum::routing::get(videogen::handle_video_task_status),
        )
        .route(
            "/api/download/{*name}",
            axum::routing::get(download::handle_download),
        )
        .layer(DefaultBodyLimit::max(MAX_API_BODY));

    let app = Router::new()
        .merge(api)
        .route("/{*path}", axum::routing::get(pages::page))
        .route("/", axum::routing::get(pages::page))
        .layer(axum_mw::from_fn_with_state(
            limiter.clone(),
            rate_limit_mw,
        ))
        .layer(axum_mw::from_fn(security_headers))
        .with_state(state.clone());

    let addr = format!("0.0.0.0:{}", state.config.port);
    tracing::info!("FlowConvert 启动于 http://{addr}");
    let listener = tokio::net::TcpListener::bind(&addr)
        .await
        .unwrap_or_else(|e| panic!("服务器启动失败: {e}"));
    axum::serve(
        listener,
        app.into_make_service_with_connect_info::<std::net::SocketAddr>(),
    )
    .await
    .unwrap();
}

#[derive(Clone)]
pub struct AppState {
    pub config: Arc<Config>,
    pub file_store: Arc<FileStore>,
    pub video_jobs: Arc<VideoJobStore>,
    pub client: Option<Arc<service::AIClient>>,
}

/// Per-IP rate limiting for /api/ requests (mirrors Go `RateLimit` wiring).
async fn rate_limit_mw(
    axum::extract::State(limiter): axum::extract::State<Arc<RateLimiter>>,
    req: axum::extract::Request,
    next: axum::middleware::Next,
) -> axum::response::Response {
    use axum::http::StatusCode;
    use axum::response::IntoResponse;

    let path = req.uri().path();
    if !path.starts_with("/api/") || req.method() == axum::http::Method::OPTIONS {
        return next.run(req).await;
    }
    let ip = middleware::client_ip(&req);
    match limiter.check(&ip) {
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
