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
}
