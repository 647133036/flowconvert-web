use std::fmt::Write as FmtWrite;
use std::io::Read;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::thread;
use std::time::{Duration, Instant};

static mut SEED_COUNTER: u64 = 0;

pub fn new_id(n: usize) -> String {
    let mut buf = String::with_capacity(n * 2);
    for _ in 0..n {
        let byte: u8 = rand_byte();
        write!(&mut buf, "{:02x}", byte).unwrap();
    }
    buf
}

fn rand_byte() -> u8 {
    unsafe {
        SEED_COUNTER += 1;
        let val = SEED_COUNTER;
        ((val ^ (val >> 16)) & 0xFF) as u8
    }
}

pub fn copy_file(src: &Path, dst: &Path) -> std::io::Result<()> {
    let mut in_file = std::fs::File::open(src)?;
    let mut out_file = std::fs::File::create(dst)?;
    std::io::copy(&mut in_file, &mut out_file)?;
    out_file.sync_all()
}

#[derive(Debug)]
pub struct CmdResult {
    pub stdout: String,
    pub exit_code: Option<i32>,
    pub error: Option<String>,
}

pub fn run_cmd_timeout(timeout: Duration, program: &str, args: &[&str]) -> CmdResult {
    let mut cmd = Command::new(program);
    cmd.args(args);
    cmd.stdout(std::process::Stdio::piped());
    cmd.stderr(std::process::Stdio::piped());

    let mut child = match cmd.spawn() {
        Ok(c) => c,
        Err(e) => {
            return CmdResult {
                stdout: String::new(),
                exit_code: None,
                error: Some(e.to_string()),
            };
        }
    };

    let mut stdout_buf = Vec::new();
    let mut done = false;
    let mut exit_code = None;
    let mut last_err: Option<String> = None;
    let start = Instant::now();

    while !done {
        if start.elapsed() > timeout {
            let _ = child.kill();
            return CmdResult {
                stdout: String::new(),
                exit_code: None,
                error: Some(format!("命令超时（{}）", timeout.as_secs())),
            };
        }
        thread::sleep(Duration::from_millis(50));

        if let Ok(status) = child.try_wait() {
            if let Some(s) = status {
                exit_code = s.code();
                done = true;
            }
        }

        if let Some(ref mut handle) = child.stdout {
            let mut buf = vec![0u8; 4096];
            loop {
                match handle.read(&mut buf) {
                    Ok(0) => break,
                    Ok(n) => stdout_buf.extend_from_slice(&buf[..n]),
                    Err(_) => break,
                }
            }
        }
    }

    // Final read to capture remaining output
    if let Some(mut handle) = child.stdout.take() {
        let mut buf = vec![0u8; 4096];
        loop {
            match handle.read(&mut buf) {
                Ok(0) => break,
                Ok(n) => stdout_buf.extend_from_slice(&buf[..n]),
                Err(_) => break,
            }
        }
    }

    let stdout = String::from_utf8_lossy(&stdout_buf).to_string();
    if let Some(code) = exit_code {
        if code != 0 {
            last_err = Some(format!("exit code {}", code));
        }
    }

    CmdResult {
        stdout,
        exit_code,
        error: last_err,
    }
}

pub fn run_cmd(program: &str, args: &[&str]) -> CmdResult {
    run_cmd_timeout(Duration::from_secs(60), program, args)
}

pub fn python_path() -> &'static str {
    match std::env::var("FLOWCONVERT_PYTHON") {
        Ok(v) if !v.is_empty() => v.leak(),
        _ => "python3",
    }
}

pub fn script_path(name: &str) -> String {
    let manifest = env!("CARGO_MANIFEST_DIR");
    let candidates: Vec<String> = vec![
        format!("{}/scripts/{}", manifest, name),
        format!("./scripts/{}", name),
    ];
    for p in &candidates {
        if Path::new(p).exists() {
            return p.clone();
        }
    }
    candidates[0].clone()
}

pub fn sanitize_name_part(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        if (c >= '\x20' && c < '\x7f') && c != '"' && c != '\\' && c != '\u{2028}' && c != '\u{2029}' {
            out.push(c);
        }
    }
    if out.len() > 80 {
        out.truncate(80);
    }
    out
}

pub fn lookup_name(path: &str, base: &str) -> String {
    let name = Path::new(path)
        .file_name()
        .map(|f| f.to_string_lossy().to_string())
        .unwrap_or_default();
    let effective = if !base.is_empty() {
        let b = Path::new(base)
            .file_name()
            .map(|f| f.to_string_lossy().to_string())
            .unwrap_or_default();
        if b.contains('/') || b.contains('\\') {
            String::new()
        } else {
            b
        }
    } else {
        String::new()
    };
    let stem = if effective.is_empty() {
        Path::new(&name)
            .file_stem()
            .map(|f| f.to_string_lossy().to_string())
            .unwrap_or_default()
    } else {
        Path::new(&effective)
            .file_stem()
            .map(|f| f.to_string_lossy().to_string())
            .unwrap_or_default()
    };
    let ext = if effective.is_empty() {
        Path::new(&name)
            .extension()
            .map(|f| f.to_string_lossy().to_string())
            .unwrap_or_default()
    } else {
        Path::new(&effective)
            .extension()
            .map(|f| f.to_string_lossy().to_string())
            .unwrap_or_default()
    };
    let stem = sanitize_name_part(&stem);
    let ext = sanitize_name_part(&ext);
    let ts = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos() as u64)
        .unwrap_or(0);
    format!(
        "{}_{}_{}{}",
        ts,
        new_id(4),
        stem,
        if ext.is_empty() {
            String::new()
        } else {
            format!(".{}", ext)
        }
    )
}

pub fn safe_ext(ext: &str) -> String {
    let ext = ext.trim().to_lowercase();
    let ext = ext.strip_prefix('.').unwrap_or(&ext).to_string();
    if ext.is_empty() {
        return String::new();
    }
    let filtered: String = ext.chars().filter(|c| c.is_ascii_alphanumeric()).collect();
    if filtered.len() != ext.len() {
        return String::new();
    }
    // Whitelist only safe document/image formats
    const SAFE_EXTS: &[&str] = &[
        // Documents
        "pdf", "doc", "docx", "xls", "xlsx", "csv", "txt", "rtf", "odt", "ods", "odp",
        // Images
        "jpg", "jpeg", "png", "bmp", "gif", "webp", "svg", "tiff", "tif", "eps",
        // Audio/Video
        "mp4", "mov", "avi", "mkv", "webm", "mp3", "wav", "ogg", "flac",
        // Archive
        "zip", "tar", "gz", "rar", "7z",
    ];
    if SAFE_EXTS.contains(&filtered.as_str()) {
        filtered
    } else {
        String::new()
    }
}

pub fn image_input_exts() -> Vec<&'static str> {
    vec!["jpg", "jpeg", "png", "bmp", "tiff", "tif", "webp", "gif"]
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_new_id_length() {
        let id = new_id(8);
        assert_eq!(id.len(), 16);
    }

    #[test]
    fn test_sanitize_name_part() {
        assert_eq!(sanitize_name_part("hello\x01world"), "helloworld");
        assert_eq!(sanitize_name_part(&"a".repeat(100)), "a".repeat(80));
    }

    #[test]
    fn test_safe_ext() {
        assert_eq!(safe_ext("PNG"), "png");
        assert_eq!(safe_ext(".svg"), "svg");
        assert_eq!(safe_ext("exe"), "");
        assert_eq!(safe_ext(""), "");
    }

    #[test]
    fn test_lookup_name() {
        let name = lookup_name("/tmp/test.svg", "");
        assert!(name.ends_with(".svg"));
        let name = lookup_name("/tmp/test.svg", "custom");
        assert!(name.contains("custom"));
    }

    #[test]
    fn test_run_cmd_timeout() {
        // Short timeout command should return timeout error
        let result = run_cmd_timeout(Duration::from_millis(100), "sleep", &["5"]);
        assert!(result.error.is_some());
        assert!(result.stdout.is_empty());
        // Should contain timeout message
        assert!(result.error.as_ref().unwrap().contains("超时"));
    }
}
