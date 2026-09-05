use std::collections::BTreeMap;
use std::time::Duration;

use crate::util::{run_cmd_timeout, python_path, script_path};

/// Option set understood by scripts/ocr.py. Empty strings keep defaults.
#[derive(Clone, Debug, Default, serde::Serialize, serde::Deserialize)]
#[serde(default)]
pub struct OcrOptions {
    pub mode: String,
    pub quality: String,
    pub charset: String,
    pub lang: String,
    pub engine: String,
    pub redbox: bool,
    pub optimize: bool,
    pub translate: bool,
    pub formats: Vec<String>,
}

const OCR_LANGS: &[&str] =
    &["auto", "zh", "en", "ja", "ko", "de", "fr", "es", "ru", "multi"];
const OCR_FORMATS: &[&str] = &["txt", "docx", "xlsx", "json", "redbox"];

/// Fill defaults and reject unsupported values. Mirrors Go OcrOptions.Normalize.
pub fn normalize(o: &mut OcrOptions) -> Result<(), String> {
    if o.mode.is_empty() {
        o.mode = "general".into();
    }
    if o.mode != "general" && o.mode != "advanced" {
        return Err(format!("不支持的识别模式: {}", o.mode));
    }
    if o.quality.is_empty() {
        o.quality = "normal".into();
    }
    if o.quality != "normal" && o.quality != "high" {
        return Err(format!("不支持的识别质量: {}", o.quality));
    }
    if o.charset.is_empty() {
        o.charset = "auto".into();
    }
    if o.charset != "auto" && o.charset != "zh_hans" && o.charset != "zh_hant" {
        return Err(format!("不支持的输出字形: {}", o.charset));
    }
    if o.lang.is_empty() {
        o.lang = "auto".into();
    }
    if !OCR_LANGS.contains(&o.lang.as_str()) {
        return Err(format!("不支持的识别语言: {}", o.lang));
    }
    if o.engine.is_empty() {
        o.engine = "auto".into();
    }
    if o.engine != "auto" && o.engine != "tesseract" && o.engine != "pp_ocr" {
        return Err(format!("不支持的识别引擎: {}", o.engine));
    }
    o.formats
        .retain(|f| OCR_FORMATS.contains(&f.as_str()));
    if o.formats.is_empty() {
        o.formats = vec!["txt".into()];
    }
    Ok(())
}

/// Script deadline estimated from quality and translation. Mirrors Go Timeout.
pub fn timeout(o: &OcrOptions) -> Duration {
    let mut secs = 300u64;
    if o.quality == "high" {
        secs = 540;
    }
    if o.translate {
        secs += 180;
    }
    Duration::from_secs(secs)
}

/// Outcome of one OCR pass plus its exported files.
#[derive(Clone, Debug)]
pub struct OcrResult {
    pub text: String,
    pub translated_text: String,
    pub engine: String,
    pub pages: i64,
    pub chars: i64,
    pub charset: String,
    pub from_text_layer: bool,
    pub layout: serde_json::Value,
    pub blocks: serde_json::Value,
    pub lines: serde_json::Value,
    pub filtered_headers: Vec<String>,
    pub files: BTreeMap<String, String>,
    pub warnings: Vec<String>,
    pub elapsed_ms: i64,
    pub options: serde_json::Value,
}

/// Run the OCR script on an image or PDF and return text plus export paths.
pub fn ocr_file(
    path: &str,
    opts: &OcrOptions,
    outdir: &str,
) -> Result<OcrResult, String> {
    let payload =
        serde_json::to_string(opts).map_err(|e| format!("参数组装失败: {e}"))?;
    let script = script_path("ocr.py");
    if !std::path::Path::new(&script).exists() {
        return Err("缺少 OCR 识别脚本".into());
    }

    let args = [
        script.as_str(),
        path,
        "--options",
        payload.as_str(),
        "--outdir",
        outdir,
    ];
    let res = run_cmd_timeout(timeout(opts), python_path(), &args);
    if let Some(e) = &res.error {
        return Err(format!("OCR 识别失败: {}", tail_of(e, 300)));
    }

    let json = extract_json(&res.stdout);
    let raw: serde_json::Value = serde_json::from_str(json.trim())
        .map_err(|_| "OCR 服务响应异常".to_string())?;
    if !raw.get("success").and_then(|v| v.as_bool()).unwrap_or(false) {
        let msg = raw
            .get("error")
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();
        return if msg.is_empty() {
            Err("OCR 识别失败".into())
        } else {
            Err(format!("OCR 识别失败: {msg}"))
        };
    }

    let arr = |key: &str| -> serde_json::Value {
        raw.get(key)
            .filter(|v| v.is_array())
            .cloned()
            .unwrap_or_else(|| serde_json::json!([]))
    };
    let str_arr = |key: &str| -> Vec<String> {
        raw.get(key)
            .and_then(|v| v.as_array())
            .map(|items| {
                items
                    .iter()
                    .filter_map(|v| v.as_str().map(|s| s.to_string()))
                    .collect()
            })
            .unwrap_or_default()
    };
    let str_map = |key: &str| -> BTreeMap<String, String> {
        raw.get(key)
            .and_then(|v| v.as_object())
            .map(|obj| {
                obj.iter()
                    .filter_map(|(k, v)| v.as_str().map(|s| (k.clone(), s.to_string())))
                    .collect()
            })
            .unwrap_or_default()
    };

    let text = raw
        .get("text")
        .and_then(|v| v.as_str())
        .unwrap_or("")
        .to_string();
    let mut chars = raw.get("chars").and_then(|v| v.as_i64()).unwrap_or(0);
    if chars == 0 {
        chars = text.chars().count() as i64;
    }
    let engine = raw
        .get("engine")
        .and_then(|v| v.as_str())
        .filter(|s| !s.is_empty())
        .unwrap_or("unknown")
        .to_string();

    Ok(OcrResult {
        text,
        translated_text: str_of(&raw, "translated_text"),
        engine,
        pages: raw.get("pages").and_then(|v| v.as_i64()).unwrap_or(0),
        chars,
        charset: str_of(&raw, "charset"),
        from_text_layer: raw
            .get("from_text_layer")
            .and_then(|v| v.as_bool())
            .unwrap_or(false),
        layout: raw
            .get("layout")
            .filter(|v| v.is_object())
            .cloned()
            .unwrap_or_else(|| {
                serde_json::json!({"columns": 1, "tables": 0, "blocks": 0, "lines": 0})
            }),
        blocks: arr("blocks"),
        lines: arr("lines"),
        filtered_headers: str_arr("filtered_headers"),
        files: str_map("files"),
        warnings: str_arr("warnings"),
        elapsed_ms: raw.get("elapsed_ms").and_then(|v| v.as_i64()).unwrap_or(0),
        options: raw
            .get("options")
            .filter(|v| v.is_object())
            .cloned()
            .unwrap_or(serde_json::Value::Null),
    })
}

fn str_of(v: &serde_json::Value, key: &str) -> String {
    v.get(key)
        .and_then(|x| x.as_str())
        .unwrap_or("")
        .to_string()
}

/// Return the last standalone JSON object line, ignoring log noise.
fn extract_json(out: &str) -> String {
    for line in out.lines().rev() {
        let t = line.trim();
        if t.starts_with('{') {
            return t.to_string();
        }
    }
    out.trim().to_string()
}

fn tail_of(s: &str, n: usize) -> String {
    if s.chars().count() <= n {
        return s.to_string();
    }
    let skip = s.chars().count() - n;
    s.chars().skip(skip).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_normalize_defaults() {
        let mut o = OcrOptions::default();
        assert!(normalize(&mut o).is_ok());
        assert_eq!(o.mode, "general");
        assert_eq!(o.quality, "normal");
        assert_eq!(o.charset, "auto");
        assert_eq!(o.lang, "auto");
        assert_eq!(o.engine, "auto");
        assert_eq!(o.formats, vec!["txt"]);
    }

    #[test]
    fn test_normalize_rejects_bad_values() {
        let mut o = OcrOptions {
            mode: "weird".into(),
            ..Default::default()
        };
        assert!(normalize(&mut o).is_err());
        let mut o = OcrOptions {
            quality: "ultra".into(),
            ..Default::default()
        };
        assert!(normalize(&mut o).is_err());
        let mut o = OcrOptions {
            lang: "xx".into(),
            ..Default::default()
        };
        assert!(normalize(&mut o).is_err());
        let mut o = OcrOptions {
            charset: "klingon".into(),
            ..Default::default()
        };
        assert!(normalize(&mut o).is_err());
        let mut o = OcrOptions {
            engine: "abbyy".into(),
            ..Default::default()
        };
        assert!(normalize(&mut o).is_err());
    }

    #[test]
    fn test_normalize_filters_formats() {
        let mut o = OcrOptions {
            formats: vec!["docx".into(), "exe".into(), " ".into(), "redbox".into()],
            ..Default::default()
        };
        assert!(normalize(&mut o).is_ok());
        assert_eq!(o.formats, vec!["docx", "redbox"]);
        let mut o = OcrOptions {
            formats: vec!["exe".into()],
            ..Default::default()
        };
        assert!(normalize(&mut o).is_ok());
        assert_eq!(o.formats, vec!["txt"]);
    }

    #[test]
    fn test_timeout_profile() {
        let base = OcrOptions {
            quality: "normal".into(),
            ..Default::default()
        };
        assert_eq!(timeout(&base), Duration::from_secs(300));
        let high = OcrOptions {
            quality: "high".into(),
            ..Default::default()
        };
        assert_eq!(timeout(&high), Duration::from_secs(540));
        let tr = OcrOptions {
            quality: "high".into(),
            translate: true,
            ..Default::default()
        };
        assert_eq!(timeout(&tr), Duration::from_secs(720));
    }

    #[test]
    fn test_extract_json_skips_logs() {
        let out = "warning: something\n[OCR] starting\n{\"success\": true, \"text\": \"hi\"}\n";
        assert_eq!(
            extract_json(out),
            "{\"success\": true, \"text\": \"hi\"}"
        );
        assert_eq!(extract_json("no json here"), "no json here");
    }

    #[test]
    fn test_tail_of_short() {
        assert_eq!(tail_of("abc", 10), "abc");
    }
}
