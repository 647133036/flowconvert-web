use std::fs;
use std::path::PathBuf;

#[derive(Debug, Clone)]
pub struct Config {
    pub port: String,
    pub base_url: String,
    pub data_dir: String,
    pub tmp_dir: PathBuf,
    pub out_dir: PathBuf,
    pub max_size: u64,
    pub max_url: u64,
    pub ttl_hours: i64,
    pub agnes_api_key: String,
    pub agnes_base_url: String,
    pub sensenova_key: String,
    pub sensenova_base: String,
}

impl Config {
    pub fn load() -> Self {
        load_dotenv();
        let data_dir = env("FLOWCONVERT_DATA", "data");
        Self {
            port: env("FLOWCONVERT_PORT", "8080"),
            base_url: env("FLOWCONVERT_BASE_URL", "http://localhost:8080"),
            tmp_dir: PathBuf::from(&data_dir).join("tmp"),
            out_dir: PathBuf::from(&data_dir).join("output"),
            data_dir,
            max_size: 50 << 20,
            max_url: 20 << 20,
            ttl_hours: 2,
            agnes_api_key: env("AGNES_API_KEY", ""),
            agnes_base_url: env("AGNES_BASE_URL", "https://apihub.agnes-ai.cn/v1"),
            sensenova_key: env("SENSENOVA_API_KEY", ""),
            sensenova_base: env("SENSENOVA_BASE_URL", "https://token.sensenova.cn/v1"),
        }
    }

    pub fn ensure_dirs(&self) -> std::io::Result<()> {
        let paths: Vec<std::path::PathBuf> = vec![
            self.data_dir.clone().into(),
            self.tmp_dir.clone(),
            self.out_dir.clone(),
        ];
        for d in paths {
            fs::create_dir_all(d)?;
        }
        Ok(())
    }
}

fn env(key: &str, def: &str) -> String {
    match std::env::var(key) {
        Ok(v) if !v.is_empty() => v,
        _ => def.to_string(),
    }
}

/// Reads a .env file from the working directory and sets environment
/// variables for keys that are not already set.
fn load_dotenv() {
    let Ok(content) = fs::read_to_string(".env") else {
        return;
    };
    for line in content.lines() {
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let Some((key, val)) = line.split_once('=') else {
            continue;
        };
        let key = key.trim();
        let mut val = val.trim().to_string();
        if (val.starts_with('"') && val.ends_with('"') && val.len() >= 2)
            || (val.starts_with('\'') && val.ends_with('\'') && val.len() >= 2)
        {
            val = val[1..val.len() - 1].to_string();
        }
        if std::env::var(key).unwrap_or_default().is_empty() {
            std::env::set_var(key, val);
        }
    }
}
