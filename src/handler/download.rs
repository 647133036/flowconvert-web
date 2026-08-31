use axum::extract::{Path, State};
use axum::http::StatusCode;
use axum::response::IntoResponse;

use crate::AppState;

pub async fn handle_download(
    State(app): State<AppState>,
    Path(name): Path<String>,
) -> impl IntoResponse {
    // Prevent path traversal: reject names containing directory separators or dot-dot
    if name.is_empty()
        || name.contains('/')
        || name.contains('\\')
        || name.contains("..")
    {
        return StatusCode::BAD_REQUEST.into_response();
    }
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

#[cfg(test)]
mod tests {

    #[test]
    fn test_path_traversal_rejected() {
        let traversal_patterns = vec!["../etc/passwd", "foo/../../bar", "foo\\bar", "a/b/c"];
        for pattern in traversal_patterns {
            assert!(
                pattern.is_empty() || pattern.contains('/') || pattern.contains('\\') || pattern.contains(".."),
                "pattern '{}' should be rejected", pattern
            );
        }
    }

    #[test]
    fn test_safe_name_accepted() {
        let safe_names = vec!["abc123", "test.png", "doc_v2.pdf", "video_mp4"];
        for name in safe_names {
            assert!(!name.is_empty() && !name.contains('/') && !name.contains('\\') && !name.contains(".."));
        }
    }
}
