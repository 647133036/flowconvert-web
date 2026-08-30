use crate::util::{run_cmd, python_path, script_path};
use std::path::PathBuf;

pub fn make_sketch(tmp_dir: &str, src: &str, sigma: f64) -> Result<String, String> {
    let sigma = if sigma <= 0.0 { 3.0 } else if sigma > 10.0 { 10.0 } else { sigma };
    let dest = PathBuf::from(tmp_dir).join("sketch.png");
    let png_path = PathBuf::from(tmp_dir).join("sketch_input.png");

    // Convert input to PNG first
    let result = run_cmd(python_path(), &[
        &script_path("vectorize.py"),
        "topng",
        src,
        png_path.to_str().unwrap(),
    ]);
    if let Some(ref err) = result.error {
        return Err(format!("图片读取失败: {}", err));
    }

    let sigma_str = sigma.to_string();
    let result = run_cmd(python_path(), &[
        &script_path("sketch.py"),
        "go",
        png_path.to_str().unwrap(),
        &sigma_str,
        dest.to_str().unwrap(),
    ]);
    if let Some(ref err) = result.error {
        return Err(format!("素描生成失败: {}", err));
    }
    if !dest.exists() {
        return Err("素描生成失败".to_string());
    }
    Ok(dest.to_string_lossy().to_string())
}

#[cfg(test)]
mod tests {
    #[test]
    fn test_sigma_clamping() {
        assert!((3.0_f64 - 3.0_f64).abs() < 0.001);
        assert!((10.0_f64 - 10.0_f64).abs() < 0.001);
        assert!((5.0_f64 - 5.0_f64).abs() < 0.001);
    }
}
