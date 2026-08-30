pub mod convert;
pub mod download;
pub mod imagegen;
pub mod pages;
pub mod translate;
pub mod videogen;

use axum::http::StatusCode;
use axum::response::IntoResponse;
use axum::Json;
use serde_json::json;

/// Placeholder response for endpoints that are pending migration.
pub async fn not_implemented() -> impl IntoResponse {
    (
        StatusCode::NOT_IMPLEMENTED,
        Json(json!({
            "success": false,
            "error": "该接口正在 Rust 版迁移中，请暂时使用 Go 版服务"
        })),
    )
        .into_response()
}
