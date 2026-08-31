use crate::util::{run_cmd, python_path, script_path};
use std::path::PathBuf;

pub fn make_id_photo(tmp_dir: &str, src: &str, size: &str, bg_color: &str) -> Result<String, String> {
    let dest = PathBuf::from(tmp_dir).join("idphoto.png");
    let result = run_cmd(python_path(), &[
        &script_path("idphoto.py"),
        src,
        dest.to_str().unwrap(),
        size,
        bg_color,
    ]);
    if let Some(ref err) = result.error {
        return Err(format!("证件照生成失败: {}", err));
    }

    // Check if the script output an error JSON on stdout
    let stdout = result.stdout.trim();
    if stdout.starts_with('{') {
        if let Ok(v) = serde_json::from_str::<serde_json::Value>(stdout) {
            if let Some(err_msg) = v.get("error").and_then(|e| e.as_str()) {
                return Err(err_msg.to_string());
            }
        }
    }

    if !dest.exists() {
        return Err("证件照生成失败，请换一张清晰的正面照重试".to_string());
    }
    Ok(dest.to_string_lossy().to_string())
}
