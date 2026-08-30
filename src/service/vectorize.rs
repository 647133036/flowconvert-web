use crate::util::{run_cmd, python_path, script_path, safe_ext};
use std::path::PathBuf;

#[derive(Debug, Clone)]
pub struct VecParams {
    pub mode: String,
    pub color_precision: i32,
    pub filter_speckle: i32,
    pub corner_threshold: i32,
}

impl Default for VecParams {
    fn default() -> Self {
        Self {
            mode: "spline".to_string(),
            color_precision: 6,
            filter_speckle: 4,
            corner_threshold: 60,
        }
    }
}

impl VecParams {
    pub fn normalize(&mut self) {
        match self.mode.as_str() {
            "polygon" | "pixel" | "spline" => {}
            _ => self.mode = "spline".to_string(),
        }
        if self.color_precision < 2 || self.color_precision > 8 {
            self.color_precision = 6;
        }
        if self.filter_speckle < 0 || self.filter_speckle > 20 {
            self.filter_speckle = 4;
        }
        if self.corner_threshold < 1 || self.corner_threshold > 180 {
            self.corner_threshold = 60;
        }
    }
}

#[derive(Debug, Clone)]
pub struct ToolAvailability {
    pub vtracer: bool,
    pub inkscape: bool,
    pub potrace: bool,
    pub libre: bool,
    pub soffice: String,
    pub inkscape_bin: String,
    pub potrace_bin: String,
}

pub fn detect_tools() -> ToolAvailability {
    let vtracer = which::which("vtracer").is_ok();
    let inkscape_bin = which::which("inkscape").map(|p| p.to_string_lossy().to_string()).unwrap_or_default();
    let inkscape = !inkscape_bin.is_empty();
    let potrace_bin = which::which("potrace").map(|p| p.to_string_lossy().to_string()).unwrap_or_default();
    let potrace = !potrace_bin.is_empty();
    let (libre, soffice) = ["soffice", "libreoffice"]
        .into_iter()
        .find_map(|name| {
            which::which(name).ok().map(|p| (true, p.to_string_lossy().to_string()))
        })
        .unwrap_or((false, String::new()));
    ToolAvailability {
        vtracer,
        inkscape,
        potrace,
        libre,
        soffice,
        inkscape_bin,
        potrace_bin,
    }
}

pub fn vectorize_image(src: &str, out_svg: &str, params: &VecParams) -> Result<(), String> {
    let mut p = params.clone();
    p.normalize();
    let params_json = serde_json::json!({
        "colormode": "color",
        "mode": p.mode,
        "filter_speckle": p.filter_speckle,
        "color_precision": p.color_precision,
        "corner_threshold": p.corner_threshold,
        "path_precision": 8,
    });
    let result = run_cmd(python_path(), &[
        &script_path("vectorize.py"),
        "svg",
        src,
        out_svg,
        &params_json.to_string(),
    ]);
    if let Some(ref err) = result.error {
        return Err(format!("vtracer 失败: {}", err));
    }
    if !std::path::Path::new(out_svg).exists() {
        return Err(format!("vtracer 未能生成SVG: {}", result.stdout.trim()));
    }
    Ok(())
}

pub fn vectorize(
    tmp_dir: &str,
    src: &str,
    output: &str,
    params: VecParams,
) -> Result<String, String> {
    // topng first
    let png_path = PathBuf::from(tmp_dir).join("input.png");
    let result = run_cmd(python_path(), &[
        &script_path("vectorize.py"),
        "topng",
        src,
        png_path.to_str().unwrap(),
    ]);
    if let Some(ref err) = result.error {
        return Err(format!("图片读取失败: {}", err));
    }

    let output = safe_ext(output);
    let output = if output.is_empty() { "svg" } else { &output };

    let svg_path = PathBuf::from(tmp_dir).join("out.svg");
    vectorize_image(png_path.to_str().unwrap(), svg_path.to_str().unwrap(), &params)?;

    match output {
        "svg" => {
            let dest = PathBuf::from(tmp_dir).join("result.svg");
            std::fs::copy(&svg_path, &dest).map_err(|e| format!("复制失败: {}", e))?;
            Ok(dest.to_string_lossy().to_string())
        }
        "pdf" | "ai" | "eps" => {
            let tools = detect_tools();
            if tools.inkscape_bin.is_empty() {
                return Err(format!("inkscape 未安装，无法输出 {} 格式，请改用 SVG", output.to_uppercase()));
            }
            let dest = PathBuf::from(tmp_dir).join(format!("result.{}", output));
            let export_type = if output == "ai" { "pdf" } else { output };
            let result = run_cmd(&tools.inkscape_bin, &[
                "--export-type", export_type,
                "--export-filename", dest.to_str().unwrap(),
                svg_path.to_str().unwrap(),
            ]);
            if let Some(ref err) = result.error {
                return Err(format!("inkscape 转换失败: {}", err));
            }
            if !dest.exists() {
                return Err("inkscape 输出文件缺失".to_string());
            }
            if output == "ai" {
                // For AI, copy the PDF as AI
                let ai_dest = PathBuf::from(tmp_dir).join("result.ai");
                std::fs::copy(&dest, &ai_dest).map_err(|e| format!("复制 AI 文件失败: {}", e))?;
                Ok(ai_dest.to_string_lossy().to_string())
            } else {
                Ok(dest.to_string_lossy().to_string())
            }
        }
        "dxf" => {
            let tools = detect_tools();
            if tools.potrace_bin.is_empty() {
                return Err("potrace 未安装，无法输出 DXF 格式".to_string());
            }
            let pbm_path = PathBuf::from(tmp_dir).join("input.pbm");
            let result = run_cmd(python_path(), &[
                &script_path("vectorize.py"),
                "topbm",
                png_path.to_str().unwrap(),
                pbm_path.to_str().unwrap(),
            ]);
            if let Some(ref err) = result.error {
                return Err(format!("PBM 转换失败: {}", err));
            }
            let dest = PathBuf::from(tmp_dir).join("result.dxf");
            let result = run_cmd(&tools.potrace_bin, &[
                "-b", "dxf",
                "-o", dest.to_str().unwrap(),
                pbm_path.to_str().unwrap(),
            ]);
            if let Some(ref err) = result.error {
                return Err(format!("potrace 转换失败: {}", err));
            }
            if !dest.exists() {
                return Err("potrace 输出文件缺失".to_string());
            }
            Ok(dest.to_string_lossy().to_string())
        }
        "sk" => {
            let dest = PathBuf::from(tmp_dir).join("result.sketch");
            let result = run_cmd(python_path(), &[
                &script_path("vectorize.py"),
                "sketch",
                svg_path.to_str().unwrap(),
                dest.to_str().unwrap(),
            ]);
            if let Some(ref err) = result.error {
                return Err(format!("Sketch 包装失败: {}", err));
            }
            Ok(dest.to_string_lossy().to_string())
        }
        "fig" => {
            let dest = PathBuf::from(tmp_dir).join("result.fig");
            let result = run_cmd(python_path(), &[
                &script_path("vectorize.py"),
                "fig",
                svg_path.to_str().unwrap(),
                dest.to_str().unwrap(),
            ]);
            if let Some(ref err) = result.error {
                return Err(format!("Figma 包装失败: {}", err));
            }
            Ok(dest.to_string_lossy().to_string())
        }
        _ => Err(format!("不支持的输出格式: {}", output)),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_vec_params_normalize() {
        let mut p = VecParams {
            mode: "invalid".to_string(),
            color_precision: 1,
            filter_speckle: -1,
            corner_threshold: 0,
        };
        p.normalize();
        assert_eq!(p.mode, "spline");
        assert_eq!(p.color_precision, 6);
        assert_eq!(p.filter_speckle, 4);
        assert_eq!(p.corner_threshold, 60);
    }

    #[test]
    fn test_detect_tools() {
        let tools = detect_tools();
        // Just ensure it runs without panicking
        let _ = tools;
    }
}
