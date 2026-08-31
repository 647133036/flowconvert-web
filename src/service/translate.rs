use serde::Deserialize;

#[derive(Debug, Clone)]
pub struct TranslateResult {
    pub text: String,
    pub detected: String,
    pub engine: String,
}

use crate::util::{run_cmd, python_path, script_path};

pub fn translate_text(text: &str, source: &str, target: &str) -> Result<TranslateResult, String> {
    let payload_file = format!(
        "{}/fc_payload_{}.json",
        std::env::temp_dir().to_string_lossy(),
        crate::util::new_id(8)
    );
    let payload = serde_json::json!({"text": text});
    std::fs::write(&payload_file, serde_json::to_string(&payload).unwrap())
        .map_err(|e| format!("写入临时文件失败: {}", e))?;
    let payload_cleanup = payload_file.clone();
    drop(payload); // release borrow

    let result = run_cmd(python_path(), &[
        &script_path("translate.py"),
        "text",
        source,
        target,
        &payload_file,
    ]);
    std::fs::remove_file(&payload_cleanup).ok();

    if let Some(ref err) = result.error {
        return Err(format!("翻译失败: {}", err));
    }

    #[derive(Deserialize)]
    struct Output {
        text: String,
        detected: String,
        engine: String,
    }
    let output: Output = serde_json::from_str(&result.stdout.trim())
        .map_err(|_| "翻译服务响应异常".to_string())?;

    Ok(TranslateResult {
        text: output.text,
        detected: output.detected,
        engine: output.engine,
    })
}

/// Fast path: use pdf-inspector to classify and extract text, then translate via Python text mode.
/// Scanned/ImageBased PDFs fall back to the full Python OCR pipeline.
fn translate_pdf_fast(
    tmp_dir: &str,
    src: &str,
    source: &str,
    target: &str,
    dest: &str,
) -> Result<String, String> {
    let data = std::fs::read(src).map_err(|e| format!("读取PDF失败: {}", e))?;

    // Classify PDF (~10-50ms)
    let classification = pdf_inspector::classify_pdf_mem(&data)
        .map_err(|e| format!("PDF 分类失败: {}", e))?;

    tracing::info!(
        "PDF 翻译分类: {:?}, 置信度: {:.2}, 页数: {}",
        classification.pdf_type,
        classification.confidence,
        classification.page_count,
    );

    match classification.pdf_type {
        pdf_inspector::PdfType::TextBased if classification.confidence >= 0.5 => {
            // Fast path: extract text via Rust, translate via Python text mode
            let pages_result = pdf_inspector::extract_pages_markdown_mem(&data, None)
                .map_err(|e| format!("PDF 文本提取失败: {}", e))?;

            let markdown: String = pages_result
                .pages
                .iter()
                .map(|pm| format!("## 第{}页\n\n{}", pm.page + 1, pm.markdown))
                .collect::<Vec<_>>()
                .join("\n\n---\n\n");

            // Write markdown to temp file for Python translation
            let md_path = format!("{}/content.md", tmp_dir);
            std::fs::write(&md_path, &markdown).map_err(|e| format!("写入临时文件失败: {}", e))?;

            // Call Python text translation mode
            let result = run_cmd(python_path(), &[
                &script_path("translate.py"),
                "text",
                source,
                target,
                &md_path,
            ]);

            if let Some(ref err) = result.error {
                return Err(format!("PDF 文本翻译失败: {}", err));
            }

            // Write translated text to output file
            std::fs::write(dest, &result.stdout).map_err(|e| format!("写入翻译结果失败: {}", e))?;

            Ok(dest.to_string())
        }
        _ => {
            // Scanned/ImageBased/Mixed or low confidence: fall back to Python OCR pipeline
            tracing::info!("PDF 为扫描版，使用 OCR 路径");
            translate_pdf_python(src, source, target, dest)
        }
    }
}

/// Fallback path: use Python translate.py file mode (OCR + translate).
fn translate_pdf_python(
    src: &str,
    source: &str,
    target: &str,
    dest: &str,
) -> Result<String, String> {
    let result = run_cmd(python_path(), &[
        &script_path("translate.py"),
        "file",
        src,
        dest,
        source,
        target,
    ]);

    if let Some(ref err) = result.error {
        if std::path::Path::new(dest).exists() {
            return Ok(dest.to_string());
        }
        return Err(format!("PDF 翻译失败: {}", err));
    }

    #[derive(Deserialize)]
    struct Output {
        output: String,
    }
    let output: Output = serde_json::from_str(&result.stdout.trim())
        .unwrap_or(Output { output: String::new() });

    let final_dest = if !output.output.is_empty() {
        output.output
    } else {
        dest.to_string()
    };

    if !std::path::Path::new(&final_dest).exists() {
        return Err("PDF 翻译未生成输出文件".to_string());
    }
    Ok(final_dest)
}

pub fn translate_file(
    tmp_dir: &str,
    src: &str,
    source: &str,
    target: &str,
) -> Result<String, String> {
    let ext = std::path::Path::new(src)
        .extension()
        .map(|e| e.to_string_lossy().to_lowercase())
        .unwrap_or_default();
    let ext = ext.strip_prefix('.').unwrap_or(&ext).to_string();
    let dest = format!("{}/translated.{}", tmp_dir, ext);

    // Fast path for TextBased PDFs
    if ext == "pdf" {
        return translate_pdf_fast(tmp_dir, src, source, target, &dest);
    }

    // Non-PDF: use Python translate.py file mode directly
    let result = run_cmd(python_path(), &[
        &script_path("translate.py"),
        "file",
        src,
        &dest,
        source,
        target,
    ]);

    if let Some(ref err) = result.error {
        // fall back: try dest directly
        if std::path::Path::new(&dest).exists() {
            return Ok(dest);
        }
        return Err(format!("文档翻译失败: {}", err));
    }

    #[derive(Deserialize)]
    struct Output {
        output: String,
    }
    let output: Output = serde_json::from_str(&result.stdout.trim())
        .unwrap_or(Output { output: String::new() });

    let final_dest = if !output.output.is_empty() {
        output.output
    } else {
        dest
    };

    if !std::path::Path::new(&final_dest).exists() {
        return Err("文档翻译未生成输出文件".to_string());
    }
    Ok(final_dest)
}

#[cfg(test)]
mod tests {
    #[test]
    fn test_translate_requires_non_empty_text() {
        // The translate service itself requires non-empty text;
        // we test that our wrapper passes through correctly.
        // Integration test with actual script would need Python env.
        assert!(super::translate_text("", "en", "zh").is_err());
    }

    #[test]
    fn test_pdf_classification_api_available() {
        // Verify pdf-inspector is linked and classify_pdf_mem is callable
        let result = pdf_inspector::classify_pdf_mem(b"not a pdf");
        assert!(result.is_err());
    }
}
