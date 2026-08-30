use axum::extract::Request;
use axum::http::{header, StatusCode};
use axum::response::{IntoResponse, Response};

use include_dir::{include_dir, Dir};

/// Embedded frontend assets (mirrors Go web.Assets embed).
static WEB: Dir = include_dir!("$CARGO_MANIFEST_DIR/web");

const PAGES: [(&str, &str); 8] = [
    ("/", "index.html"),
    ("/index.html", "index.html"),
    ("/idphoto", "idphoto.html"),
    ("/translate", "translate.html"),
    ("/video", "video.html"),
    ("/image", "image.html"),
    ("/about", "about.html"),
    ("/donate", "donate.html"),
];

/// Serves static assets and multi-page HTML (mirrors Go pageHandler).
pub async fn page(req: Request) -> Response {
    let path = req.uri().path();
    if let Some((_, file)) = PAGES.iter().find(|(route, _)| *route == path) {
        return serve_embedded(file, "text/html; charset=utf-8");
    }
    if path == "/static/" || path.starts_with("/static/") {
        let name = percent_decode(path.trim_start_matches('/'));
        // Reject traversal before touching the embedded tree.
        if name.contains("..") {
            return not_found();
        }
        return serve_embedded(&name, "");
    }
    not_found()
}

fn percent_decode(s: &str) -> String {
    percent_encoding::percent_decode_str(s).decode_utf8_lossy().into_owned()
}

fn not_found() -> Response {
    (
        StatusCode::NOT_FOUND,
        [("Content-Type", "text/plain; charset=utf-8")],
        "404 page not found",
    )
        .into_response()
}

fn serve_embedded(name: &str, default_mime: &str) -> Response {
    let Some(file) = WEB.get_file(name) else {
        return not_found();
    };
    let mime = if default_mime.is_empty() {
        content_type_by_name(name).unwrap_or("application/octet-stream")
    } else {
        default_mime
    };
    (
        StatusCode::OK,
        [(header::CONTENT_TYPE, mime)],
        file.contents(),
    )
        .into_response()
}

/// Mirrors Go contentTypeByName; anything else falls back to octet-stream.
fn content_type_by_name(name: &str) -> Option<&'static str> {
    if name.ends_with(".css") {
        Some("text/css; charset=utf-8")
    } else if name.ends_with(".js") {
        Some("application/javascript; charset=utf-8")
    } else if name.ends_with(".webp") {
        Some("image/webp")
    } else if name.ends_with(".png") {
        Some("image/png")
    } else if name.ends_with(".svg") {
        Some("image/svg+xml")
    } else {
        None
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn embedded_pages_exist() {
        for (_, file) in PAGES.iter() {
            assert!(WEB.get_file(file).is_some(), "missing embedded page {file}");
        }
    }

    #[test]
    fn embedded_static_exists() {
        assert!(WEB.get_file("static/css/style.css").is_some());
    }

    #[test]
    fn content_type_mapping() {
        assert_eq!(
            content_type_by_name("static/css/style.css"),
            Some("text/css; charset=utf-8")
        );
        assert_eq!(content_type_by_name("static/js/app.js"), Some("application/javascript; charset=utf-8"));
        assert_eq!(content_type_by_name("static/img/logo.svg"), Some("image/svg+xml"));
        assert_eq!(content_type_by_name("unknown.bin"), None);
    }
}
