use crate::util::{python_path, script_path, safe_ext};
use std::path::PathBuf;
use std::time::Duration;

/// Convert a PDF to an Office document (DOCX/XLSX).
///
/// Uses pdf-inspector for fast classification:
/// - TextBased PDFs: extract markdown via Rust, convert with Python scripts
/// - Scanned/ImageBased PDFs: fall back to existing Python OCR pipeline
pub fn pdf_to_office(tmp_dir: &str, src: &str, output: &str) -> Result<String, String> {
    let output = safe_ext(output);
    let output = if output.is_empty() { "docx" } else { &output };
    if output != "docx" && output != "xlsx" {
        return Err(format!("不支持的输出格式: {}", output));
    }

    let data = std::fs::read(src).map_err(|e| format!("读取PDF失败: {}", e))?;

    // Fast classification: ~10-50ms
    match pdf_inspector::classify_pdf_mem(&data) {
        Ok(classification) => {
            tracing::info!(
                "PDF 分类: {:?}, 页数: {}, 置信度: {:.2}, OCR页面: {:?}",
                classification.pdf_type,
                classification.page_count,
                classification.confidence,
                classification.pages_needing_ocr
            );

            match classification.pdf_type {
                pdf_inspector::PdfType::TextBased if classification.confidence >= 0.5 => {
                    // Fast path: extract markdown via Rust, no OCR needed
                    return pdf_to_office_from_mem(tmp_dir, &data, output);
                }
                pdf_inspector::PdfType::Mixed if classification.confidence >= 0.5 => {
                    // Mixed PDF: try fast path first, fallback to Python if extraction is weak
                    return pdf_to_office_from_mem(tmp_dir, &data, output);
                }
                _ => {
                    // Scanned/ImageBased or low confidence: use Python OCR path
                    tracing::info!("PDF 为扫描版，使用 OCR 路径");
                    return pdf_to_office_python(tmp_dir, src, output);
                }
            }
        }
        Err(e) => {
            tracing::warn!("PDF 分类失败，回退到 Python 路径: {}", e);
            return pdf_to_office_python(tmp_dir, src, output);
        }
    }
}

/// Fast path: extract markdown via pdf-inspector, convert with Python.
fn pdf_to_office_from_mem(
    tmp_dir: &str,
    data: &[u8],
    output: &str,
) -> Result<String, String> {
    let out_dir = PathBuf::from(tmp_dir).join("office_out");
    std::fs::create_dir_all(&out_dir).map_err(|e| format!("创建目录失败: {}", e))?;

    // Extract markdown
    let pages_result = pdf_inspector::extract_pages_markdown_mem(data, None)
        .map_err(|e| format!("PDF 文本提取失败: {}", e))?;

    let markdown: String = pages_result
        .pages
        .iter()
        .map(|pm| format!("## 第{}页\n\n{}", pm.page + 1, pm.markdown))
        .collect::<Vec<_>>()
        .join("\n\n---\n\n");

    // Write markdown to temp file
    let md_path = out_dir.join("content.md");
    std::fs::write(&md_path, &markdown).map_err(|e| format!("写入临时文件失败: {}", e))?;

    // Convert to target format using Python script
    let py_script = format!("md2{}.py", output);
    let py_path = script_path(&py_script);
    if !std::path::Path::new(&py_path).exists() {
        return Err(format!("缺少转换脚本: {}", py_script));
    }

    let stem = PathBuf::new()
        .join("document")
        .file_stem()
        .map(|s| s.to_string_lossy().to_string())
        .unwrap_or_default();
    let dest = out_dir.join(format!("{}.{}", stem, output));

    let result = run_cmd_timeout(Duration::from_secs(60), python_path(), &[
        &py_path,
        md_path.to_str().unwrap(),
        dest.to_str().unwrap(),
    ]);

    if let Some(ref err) = result.error {
        return Err(format!("转换失败: {}", err));
    }
    if !dest.exists() {
        return Err("未生成输出文件".to_string());
    }
    Ok(dest.to_string_lossy().to_string())
}

/// Fallback path: use existing Python PDF conversion scripts (with OCR support).
fn pdf_to_office_python(tmp_dir: &str, src: &str, output: &str) -> Result<String, String> {
    let py_script = format!("pdf2{}.py", output);
    let py_path = script_path(&py_script);
    if !std::path::Path::new(&py_path).exists() {
        return Err(format!("缺少转换脚本: {}", py_script));
    }

    let out_dir = PathBuf::from(tmp_dir).join("office_out");
    std::fs::create_dir_all(&out_dir).map_err(|e| format!("创建目录失败: {}", e))?;

    let stem = PathBuf::from(src)
        .file_stem()
        .map(|s| s.to_string_lossy().to_string())
        .unwrap_or_default();
    let dest = out_dir.join(format!("{}.{}", stem, output));

    let result = run_cmd_timeout(Duration::from_secs(300), python_path(), &[
        &py_path,
        src,
        dest.to_str().unwrap(),
    ]);

    if let Some(ref err) = result.error {
        return Err(format!("PDF 转换失败: {}", err));
    }
    if !dest.exists() {
        return Err("未生成输出文件".to_string());
    }
    Ok(dest.to_string_lossy().to_string())
}

/// Convert a PDF directly to Markdown (new endpoint, no Office conversion).
pub fn pdf_to_markdown(tmp_dir: &str, src: &str) -> Result<String, String> {
    let data = std::fs::read(src).map_err(|e| format!("读取PDF失败: {}", e))?;

    let _classification = pdf_inspector::classify_pdf_mem(&data)
        .map_err(|e| format!("PDF 分类失败: {}", e))?;

    let pages_result = pdf_inspector::extract_pages_markdown_mem(&data, None)
        .map_err(|e| format!("PDF 文本提取失败: {}", e))?;

    let markdown: String = pages_result
        .pages
        .iter()
        .map(|pm| format!("## 第{}页\n\n{}", pm.page + 1, pm.markdown))
        .collect::<Vec<_>>()
        .join("\n\n---\n\n");

    let out_dir = PathBuf::from(tmp_dir).join("markdown_out");
    std::fs::create_dir_all(&out_dir).map_err(|e| format!("创建目录失败: {}", e))?;

    let dest = out_dir.join("output.md");
    std::fs::write(&dest, &markdown).map_err(|e| format!("写入失败: {}", e))?;

    Ok(dest.to_string_lossy().to_string())
}

fn run_cmd_timeout(timeout: Duration, program: &str, args: &[&str]) -> crate::util::CmdResult {
    crate::util::run_cmd_timeout(timeout, program, args)
}

#[cfg(test)]
mod tests {
    #[test]
    fn test_safe_ext_rejects_invalid() {
        assert_eq!(crate::util::safe_ext("exe"), "");
        assert_eq!(crate::util::safe_ext("docx"), "docx");
        assert_eq!(crate::util::safe_ext("xlsx"), "xlsx");
    }

    #[test]
    fn test_pdf_classification_api_available() {
        // Verify pdf-inspector is linked and classify_pdf_mem is callable
        // with empty buffer (should return an error, not panic)
        let result = pdf_inspector::classify_pdf_mem(b"not a pdf");
        assert!(result.is_err());
    }
}
