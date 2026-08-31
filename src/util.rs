use std::fmt::Write as FmtWrite;
use std::io::Read;
use std::path::Path;
use std::process::{Command, Stdio};
use std::thread;
use std::time::{Duration, Instant};

pub fn new_id(n: usize) -> String {
    let uuid = uuid::Uuid::new_v4();
    let bytes = uuid.as_bytes();
    let mut buf = String::with_capacity(n * 2);
    for i in 0..n {
        write!(&mut buf, "{:02x}", bytes[i % 16]).unwrap();
    }
    buf
}

pub fn copy_file(src: &Path, dst: &Path) -> std::io::Result<()> {
    let mut in_file = std::fs::File::open(src)?;
    let mut out_file = std::fs::File::create(dst)?;
    if let Err(e) = std::io::copy(&mut in_file, &mut out_file) {
        let _ = std::fs::remove_file(dst);
        return Err(e);
    }
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
    cmd.stdout(Stdio::piped());
    cmd.stderr(Stdio::piped());

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

    let stdout_handle = child.stdout.take();
    let stderr_handle = child.stderr.take();

    let start = Instant::now();
    let mut stdout_buf = Vec::new();
    let mut stderr_buf = Vec::new();
    let mut exit_code = None;

    // Spawn reader threads to avoid deadlocks from full pipe buffers
    let stdout_thread = stdout_handle.map(|mut h| {
        thread::spawn(move || {
            let mut buf = vec![0u8; 4096];
            let mut total = Vec::new();
            loop {
                match h.read(&mut buf) {
                    Ok(0) => break,
                    Ok(n) => total.extend_from_slice(&buf[..n]),
                    Err(_) => break,
                }
            }
            total
        })
    });

    let stderr_thread = stderr_handle.map(|mut h| {
        thread::spawn(move || {
            let mut buf = vec![0u8; 4096];
            let mut total = Vec::new();
            loop {
                match h.read(&mut buf) {
                    Ok(0) => break,
                    Ok(n) => total.extend_from_slice(&buf[..n]),
                    Err(_) => break,
                }
            }
            total
        })
    });

    loop {
        if start.elapsed() > timeout {
            let _ = child.kill();
            if let Some(t) = stdout_thread {
                if let Ok(buf) = t.join() { stdout_buf = buf; }
            }
            if let Some(t) = stderr_thread {
                if let Ok(buf) = t.join() { stderr_buf = buf; }
            }
            let stdout = String::from_utf8_lossy(&stdout_buf).to_string();
            let stderr = String::from_utf8_lossy(&stderr_buf).to_string();
            return CmdResult {
                stdout,
                exit_code: None,
                error: Some(format!("命令超时（{}s），stderr: {}", timeout.as_secs(), stderr.trim_end())),
            };
        }

        match child.try_wait() {
            Ok(Some(status)) => {
                exit_code = status.code();
                break;
            }
            Ok(None) => thread::sleep(Duration::from_millis(50)),
            Err(_) => break,
        }
    }

    // Join reader threads to get remaining output
    if let Some(t) = stdout_thread {
        if let Ok(buf) = t.join() { stdout_buf = buf; }
    }
    if let Some(t) = stderr_thread {
        if let Ok(buf) = t.join() { stderr_buf = buf; }
    }

    let _ = child.wait();

    let stdout = String::from_utf8_lossy(&stdout_buf).to_string();
    let stderr = String::from_utf8_lossy(&stderr_buf).to_string();
    let error = if let Some(code) = exit_code {
        if code != 0 {
            Some(format!("exit code {}，stderr: {}", code, stderr.trim_end()))
        } else {
            None
        }
    } else {
        None
    };

    CmdResult { stdout, exit_code, error }
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
        // Vector graphics (AI, DXF, SK, FIG)
        "ai", "dxf", "sk", "fig",
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
    fn test_new_id_no_duplicates() {
        let mut ids = std::collections::HashSet::new();
        // UUID v4 is crypto-random; test within safe range to verify uniqueness
        for _ in 0..32 {
            let id = new_id(8);
            assert!(ids.insert(id), "duplicate id found");
        }
    }

    #[test]
    fn test_sanitize_name_part() {
        assert_eq!(sanitize_name_part("hello\x01world"), "helloworld");
        assert_eq!(sanitize_name_part(&"a".repeat(100)), "a".repeat(80));
        // Control characters removed
        let input = "te\x00st\x01li\x02ne";
        assert_eq!(sanitize_name_part(input), "testline");
        // Quote and backslash removed
        assert_eq!(sanitize_name_part(r#"he"l\lo"#), "hello");
    }

    #[test]
    fn test_lookup_name_variants() {
        // Empty base
        let name = lookup_name("/tmp/test.svg", "");
        assert!(name.ends_with(".svg"));
        // Base with slash (should be ignored)
        let name = lookup_name("/tmp/test.svg", "bad/base");
        assert!(!name.contains("bad"));
        // No extension in path
        let name = lookup_name("/tmp/testfile", "");
        assert!(name.contains("testfile"));
        // Base with no extension
        let name = lookup_name("/tmp/test.svg", "custom");
        assert!(name.contains("custom"));
    }

    #[test]
    fn test_safe_ext() {
        // Whitelisted
        assert_eq!(safe_ext("PNG"), "png");
        assert_eq!(safe_ext(".svg"), "svg");
        assert_eq!(safe_ext("pdf"), "pdf");
        assert_eq!(safe_ext("docx"), "docx");
        assert_eq!(safe_ext("jpg"), "jpg");
        assert_eq!(safe_ext("mp4"), "mp4");
        assert_eq!(safe_ext("zip"), "zip");
        assert_eq!(safe_ext("svg"), "svg");
        assert_eq!(safe_ext("ai"), "ai");
        assert_eq!(safe_ext("dxf"), "dxf");
        assert_eq!(safe_ext("sk"), "sk");
        assert_eq!(safe_ext("fig"), "fig");
        // Not whitelisted
        assert_eq!(safe_ext("exe"), "");
        assert_eq!(safe_ext("sh"), "");
        assert_eq!(safe_ext("py"), "");
        assert_eq!(safe_ext("js"), "");
        assert_eq!(safe_ext(""), "");
        // Invalid chars in extension
        assert_eq!(safe_ext("do;c"), "");
        assert_eq!(safe_ext("ima/ge"), "");
    }

    #[test]
    fn test_run_cmd_timeout() {
        // Short timeout command should return timeout error
        let result = run_cmd_timeout(Duration::from_millis(100), "sleep", &["5"]);
        assert!(result.error.is_some());
        // Timeout should still capture any stdout read before kill
        // (sleep produces no output, so stdout should be empty)
        assert!(result.stdout.is_empty());
        assert!(result.error.as_ref().unwrap().contains("超时"));
    }
}
