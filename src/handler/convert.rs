use axum::http::StatusCode;
use axum::response::IntoResponse;
use serde_json::json;

pub const IMAGE_INPUT: [&str; 7] = ["jpg", "jpeg", "png", "bmp", "tiff", "webp", "gif"];
pub const VECTOR_OUTPUT: [&str; 7] = ["svg", "ai", "dxf", "eps", "fig", "sk", "pdf"];
pub const PDF_OUTPUT: [&str; 2] = ["docx", "xlsx"];

pub const MAX_PROMPT_LEN: usize = 2000;

/// GET /api/formats — capability info for the frontend.
pub async fn formats() -> impl IntoResponse {
    (
        StatusCode::OK,
        axum::Json(json!({
            "success": true,
            "image_input": IMAGE_INPUT,
            "vector_output": VECTOR_OUTPUT,
            "pdf_output": PDF_OUTPUT,
            "max_upload_mb": 50,
            "max_url_mb": 20,
        })),
    )
}

/// Validates an output format against the whitelist for `kind`.
/// Returns the sanitized format or `None` when invalid (mirrors Go `validOutput`).
pub fn valid_output(format: &str, kind: &str) -> Option<String> {
    match kind {
        "vector" if VECTOR_OUTPUT.contains(&format) => Some(format.to_string()),
        "pdf" if PDF_OUTPUT.contains(&format) => Some(format.to_string()),
        _ => None,
    }
}

/// Validates prompt length (mirrors Go `validPrompt`).
pub fn valid_prompt(p: &str) -> Result<&str, String> {
    if p.len() > MAX_PROMPT_LEN {
        return Err(format!("提示词长度不能超过{}个字符", MAX_PROMPT_LEN));
    }
    Ok(p)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn valid_output_whitelist() {
        assert_eq!(valid_output("svg", "vector"), Some("svg".to_string()));
        assert_eq!(valid_output("dxf", "vector"), Some("dxf".to_string()));
        assert_eq!(valid_output("exe", "vector"), None);
        assert_eq!(valid_output("docx", "pdf"), Some("docx".to_string()));
        assert_eq!(valid_output("svg", "pdf"), None);
        assert_eq!(valid_output("svg", "other"), None);
    }

    #[test]
    fn valid_prompt_length() {
        assert!(valid_prompt("hello").is_ok());
        assert!(valid_prompt("").is_ok());
        let long = "a".repeat(MAX_PROMPT_LEN + 1);
        assert!(valid_prompt(&long).is_err());
        let exact = "a".repeat(MAX_PROMPT_LEN);
        assert!(valid_prompt(&exact).is_ok());
    }
}
