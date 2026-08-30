use axum::extract::{Path, State};
use axum::http::StatusCode;
use axum::response::IntoResponse;
use std::sync::Arc;

use crate::AppState;

pub async fn handle_download(
    State(app): State<AppState>,
    Path(name): Path<String>,
) -> impl IntoResponse {
    match app.file_store.download_handler(&name) {
        Ok((content_type, disposition, bytes, _size)) => {
            let mut res = axum::response::Response::new(axum::body::Body::from(bytes));
            res.headers_mut().insert(
                axum::http::header::CONTENT_TYPE,
                content_type.parse().unwrap(),
            );
            res.headers_mut().insert(
                axum::http::header::CONTENT_DISPOSITION,
                disposition.parse().unwrap(),
            );
            res
        }
        Err(status) => status.into_response(),
    }
}
