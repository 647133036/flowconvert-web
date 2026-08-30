mod config;
mod handler;
mod middleware;

use std::sync::Arc;

use axum::extract::DefaultBodyLimit;
use axum::middleware as axum_mw;
use axum::routing::{get, post};
use axum::Router;

use config::Config;
use middleware::{security_headers, RateDecision, RateLimiter, MAX_API_BODY};

#[tokio::main]
async fn main() {
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

    let api = Router::new()
        .route("/api/formats", get(handler::convert::formats))
        // Endpoints pending migration: routes stay registered so the
        // frontend contract is preserved; each returns 501 for now.
        .route("/api/convert/upload", post(handler::not_implemented))
        .route("/api/convert/url", post(handler::not_implemented))
        .route("/api/convert/pdf-to-office", post(handler::not_implemented))
        .route("/api/convert/sketch", post(handler::not_implemented))
        .route("/api/convert/idphoto", post(handler::not_implemented))
        .route("/api/translate", post(handler::not_implemented))
        .route("/api/translate/file", post(handler::not_implemented))
        .route("/api/convert/image/text", post(handler::not_implemented))
        .route("/api/convert/image/edit", post(handler::not_implemented))
        .route("/api/convert/image/compose", post(handler::not_implemented))
        .route("/api/convert/video/text", post(handler::not_implemented))
        .route("/api/convert/video/keyframe", post(handler::not_implemented))
        .route("/api/convert/video/ref", post(handler::not_implemented))
        .route(
            "/api/convert/video/task/{id}",
            get(handler::not_implemented),
        )
        .route("/api/download/{*path}", get(handler::not_implemented))
        .layer(DefaultBodyLimit::max(MAX_API_BODY));

    let app = Router::new()
        .merge(api)
        .route("/{*path}", get(handler::pages::page))
        .route("/", get(handler::pages::page))
        .layer(axum_mw::from_fn_with_state(
            limiter.clone(),
            rate_limit_mw,
        ))
        .layer(axum_mw::from_fn(security_headers));

    let addr = format!("0.0.0.0:{}", cfg.port);
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
