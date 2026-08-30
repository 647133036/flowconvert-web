pub mod convert;
pub mod pages;

use axum::http::StatusCode;
use axum::response::IntoResponse;
use serde_json::json;

/// Placeholder response for endpoints that are pending migration.
pub async fn not_implemented() -> impl IntoResponse {
    (
        StatusCode::NOT_IMPLEMENTED,
        axum::Json(json!({
            "success": false,
            "error": "该接口正在 Rust 版迁移中，请暂时使用 Go 版服务"
        })),
    )
}
