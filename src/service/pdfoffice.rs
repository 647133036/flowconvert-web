use crate::util::{run_cmd, python_path, script_path, safe_ext};
use std::path::PathBuf;
use std::time::Duration;

pub fn pdf_to_office(tmp_dir: &str, src: &str, output: &str) -> Result<String, String> {
    let output = safe_ext(output);
    let output = if output.is_empty() { "docx" } else { &output };
    if output != "docx" && output != "xlsx" {
        return Err(format!("不支持的输出格式: {}", output));
    }

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
}
