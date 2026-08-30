use std::path::PathBuf;
use std::time::Duration;

use crate::service::aiclient::AIClient;
use crate::util::{python_path, script_path};

/// Marshal video generation payload to JSON bytes.
pub fn marshal_video_payload(fields: &serde_json::Map<String, serde_json::Value>) -> Result<Vec<u8>, String> {
    let obj = serde_json::Value::Object(fields.clone());
    serde_json::to_vec(&obj).map_err(|e| format!("参数序列化失败: {}", e))
}

/// clamp_seconds clamps duration to the 2.5 Flash supported range (4-12).
pub fn clamp_seconds(d: i32) -> String {
    if d < 4 {
        return "4".to_string();
    }
    if d > 12 {
        return "12".to_string();
    }
    d.to_string()
}

/// split_duration divides a total duration into segments of 4-12 seconds each.
pub fn split_duration(total: i32) -> Vec<i32> {
    if total <= 4 {
        return vec![4];
    }
    if total <= 12 {
        return vec![total];
    }
    let n = (total + 11) / 12;
    let base = total / n;
    let rem = total % n;
    let mut segs = vec![base; n as usize];
    for i in 0..rem {
        segs[i as usize] += 1;
    }
    segs
}

/// split_prompt_clauses splits a user prompt into narrative clauses by Chinese
/// and ASCII punctuation.
pub fn split_prompt_clauses(prompt: &str) -> Vec<String> {
    let mut splits = Vec::new();
    let mut current = String::new();
    for c in prompt.chars() {
        match c {
            '，' | '。' | '！' | '？' | '、' | '；' | ',' | '.' | '!' | '?' | ';' => {
                if !current.is_empty() {
                    splits.push(current.trim().to_string());
                    current.clear();
                }
            }
            _ => current.push(c),
        }
    }
    if !current.is_empty() {
        splits.push(current.trim().to_string());
    }
    splits
}

/// segment_stage_prompt builds a distinct, user-derived prompt for a given segment
/// index.
pub fn segment_stage_prompt(prompt: &str, i: usize, n: usize) -> String {
    let clauses = split_prompt_clauses(prompt);
    let focus = if !clauses.is_empty() {
        clauses[i % clauses.len()].clone()
    } else {
        prompt.to_string()
    };
    let stage = match i {
        0 => "故事开端".to_string(),
        x if x == n - 1 => "故事结尾".to_string(),
        _ => format!("第{}阶段", i + 1),
    };
    format!("{}。本段聚焦：{}。叙事：{}", prompt, focus, stage)
}

/// probe_resolution returns width,height from a video file using ffprobe.
pub fn probe_resolution(path: &str) -> Result<(i32, i32), String> {
    let result = crate::util::run_cmd_timeout(Duration::from_secs(30), "ffprobe", &[
        "-v", "error",
        "-select_streams", "v:0",
        "-show_entries", "stream=width,height",
        "-of", "json",
        path,
    ]);
    if result.error.is_some() {
        return Err(result.error.unwrap());
    }
    let output = &result.stdout;
    #[derive(serde::Deserialize)]
    struct ProbeResult {
        streams: Vec<StreamInfo>,
    }
    #[derive(serde::Deserialize)]
    struct StreamInfo {
        width: i32,
        height: i32,
    }
    let probe: ProbeResult = serde_json::from_str(output).map_err(|e| format!("ffprobe解析失败: {}", e))?;
    let stream = probe.streams.first().ok_or("无法解析视频分辨率")?;
    Ok((stream.width, stream.height))
}

/// concat_videos merges multiple MP4 segments using ffmpeg concat demuxer.
pub fn concat_videos(tmp_dir: &str, seg_paths: &[String], dest: &str) -> Result<String, String> {
    let list_path = PathBuf::from(tmp_dir).join("concat_list.txt");
    let mut content = String::new();
    for p in seg_paths {
        let abs = std::fs::canonicalize(p).unwrap_or_else(|_| PathBuf::from(p).clone());
        content.push_str(&format!("file '{}'\n", abs.display()));
    }
    std::fs::write(&list_path, &content).map_err(|e| format!("写入拼接列表失败: {}", e))?;

    // Try stream copy first (fastest)
    let result = crate::util::run_cmd_timeout(Duration::from_secs(60), "ffmpeg", &[
        "-y", "-f", "concat", "-safe", "0",
        "-i", list_path.to_str().unwrap(),
        "-c", "copy", "-movflags", "+faststart",
        dest,
    ]);
    if result.error.is_none() {
        std::fs::remove_file(&list_path).ok();
        if std::path::Path::new(dest).exists() {
            return Ok(dest.to_string());
        }
    }

    // Fallback: re-encode with resolution from first segment
    let (w, h) = probe_resolution(seg_paths.first().ok_or("无视频片段")?)?;
    let result2 = crate::util::run_cmd_timeout(Duration::from_secs(180), "ffmpeg", &[
        "-y", "-f", "concat", "-safe", "0",
        "-i", list_path.to_str().unwrap(),
        "-s", &format!("{}x{}", w, h),
        "-c:v", "libx264", "-preset", "fast", "-crf", "23",
        "-c:a", "aac", "-movflags", "+faststart",
        dest,
    ]);
    std::fs::remove_file(&list_path).ok();
    if let Some(ref err) = result2.error {
        return Err(format!("视频拼接失败: {}", err));
    }
    if !std::path::Path::new(dest).exists() {
        return Err("拼接输出文件不存在".to_string());
    }
    Ok(dest.to_string())
}

/// is_transient_video_err reports whether an error is worth retrying.
pub fn is_transient_video_err(err: &str) -> bool {
    err.contains("DiffGenerator returned no result")
        || err.contains("no result")
        || err.contains("429")
        || err.contains("rate_limit")
        || err.contains("rate limit")
        || err.contains("503")
        || err.contains("video_queue_full")
}

const SEGMENT_ATTEMPTS: usize = 3;

/// generate_video_segment submits, polls and downloads a single video segment,
/// retrying on transient failures.
pub async fn generate_video_segment(
    client: &AIClient,
    seg_path: &str,
    params: &crate::service::aiclient::VideoTaskParams,
    label: &str,
) -> Result<(), String> {
    let mut last_err: Option<String> = None;
    for attempt in 0..SEGMENT_ATTEMPTS {
        if attempt > 0 {
            eprintln!("[Agnes] 段{}第{}次重试", label, attempt + 1);
            tokio::time::sleep(Duration::from_secs(5)).await;
        }
        let video_id = match client.create_video_task(params).await {
            Ok(id) => id,
            Err(e) => {
                last_err = Some(e.clone());
                if is_transient_video_err(&e) {
                    continue;
                }
                return Err(e);
            }
        };
        let video_url = match client.poll_video_task(&video_id, Duration::from_secs(1800)).await {
            Ok(url) => url,
            Err(e) => {
                last_err = Some(e.clone());
                if is_transient_video_err(&e) {
                    continue;
                }
                return Err(e);
            }
        };
        if client.download_video(&video_url, seg_path).await.is_ok() {
            return Ok(());
        }
        last_err = Some("下载视频失败".to_string());
    }
    Err(last_err.unwrap_or("未知错误".to_string()))
}

pub fn make_text_video(tmp_dir: &str, prompt: &str, duration: i32) -> Result<String, String> {
    let duration = if duration <= 0 { 3 } else if duration > 60 { 60 } else { duration };
    let dest = PathBuf::from(tmp_dir).join("video.mp4");
    let payload_path = PathBuf::from(tmp_dir).join("video_payload.json");

    let mut fields = serde_json::Map::new();
    fields.insert("prompt".to_string(), serde_json::Value::String(prompt.to_string()));
    fields.insert("duration".to_string(), serde_json::Value::Number(serde_json::Number::from(duration)));

    let payload = marshal_video_payload(&fields)?;
    std::fs::write(&payload_path, &payload).map_err(|e| format!("保存参数失败: {}", e))?;
    let payload_cleanup = payload_path.clone();

    let result = run_cmd_timeout(Duration::from_secs(600), python_path(), &[
        &script_path("video.py"),
        "text",
        payload_path.to_str().unwrap(),
        dest.to_str().unwrap(),
    ]);
    std::fs::remove_file(&payload_cleanup).ok();

    if let Some(ref err) = result.error {
        return Err(format!("视频生成失败: {}", err));
    }
    if !dest.exists() {
        return Err("视频生成失败，请稍后重试".to_string());
    }
    Ok(dest.to_string_lossy().to_string())
}

pub fn make_keyframe_video(
    tmp_dir: &str,
    first_frame: &str,
    last_frame: &str,
    prompt: &str,
    duration: i32,
) -> Result<String, String> {
    let duration = if duration <= 0 { 5 } else if duration > 60 { 60 } else { duration };
    let dest = PathBuf::from(tmp_dir).join("keyframe_video.mp4");
    let payload_path = PathBuf::from(tmp_dir).join("kf_payload.json");

    let mut fields = serde_json::Map::new();
    fields.insert("first".to_string(), serde_json::Value::String(first_frame.to_string()));
    fields.insert("last".to_string(), serde_json::Value::String(last_frame.to_string()));
    fields.insert("prompt".to_string(), serde_json::Value::String(prompt.to_string()));
    fields.insert("duration".to_string(), serde_json::Value::Number(serde_json::Number::from(duration)));

    let payload = marshal_video_payload(&fields)?;
    std::fs::write(&payload_path, &payload).map_err(|e| format!("保存参数失败: {}", e))?;
    let payload_cleanup = payload_path.clone();

    let result = run_cmd_timeout(Duration::from_secs(600), python_path(), &[
        &script_path("video.py"),
        "keyframe",
        payload_path.to_str().unwrap(),
        dest.to_str().unwrap(),
    ]);
    std::fs::remove_file(&payload_cleanup).ok();

    if let Some(ref err) = result.error {
        return Err(format!("视频生成失败: {}", err));
    }
    if !dest.exists() {
        return Err("视频生成失败，请稍后重试".to_string());
    }
    Ok(dest.to_string_lossy().to_string())
}

pub fn make_ref_video(
    tmp_dir: &str,
    prompt: &str,
    ref_paths: &[String],
    duration: i32,
) -> Result<String, String> {
    let duration = if duration <= 0 { 5 } else if duration > 60 { 60 } else { duration };
    let dest = PathBuf::from(tmp_dir).join("ref_video.mp4");
    let payload_path = PathBuf::from(tmp_dir).join("ref_payload.json");

    let mut fields = serde_json::Map::new();
    fields.insert("prompt".to_string(), serde_json::Value::String(prompt.to_string()));
    fields.insert(
        "refs".to_string(),
        serde_json::Value::Array(
            ref_paths.iter().map(|s| serde_json::Value::String(s.clone())).collect(),
        ),
    );
    fields.insert("duration".to_string(), serde_json::Value::Number(serde_json::Number::from(duration)));

    let payload = marshal_video_payload(&fields)?;
    std::fs::write(&payload_path, &payload).map_err(|e| format!("保存参数失败: {}", e))?;
    let payload_cleanup = payload_path.clone();

    let result = run_cmd_timeout(Duration::from_secs(600), python_path(), &[
        &script_path("video.py"),
        "ref",
        payload_path.to_str().unwrap(),
        dest.to_str().unwrap(),
    ]);
    std::fs::remove_file(&payload_cleanup).ok();

    if let Some(ref err) = result.error {
        return Err(format!("视频生成失败: {}", err));
    }
    if !dest.exists() {
        return Err("视频生成失败，请稍后重试".to_string());
    }
    Ok(dest.to_string_lossy().to_string())
}

fn run_cmd_timeout(timeout: Duration, program: &str, args: &[&str]) -> crate::util::CmdResult {
    crate::util::run_cmd_timeout(timeout, program, args)
}

pub async fn make_text_video_ai(
    client: &AIClient,
    tmp_dir: &str,
    prompt: &str,
    duration: i32,
    aspect_ratio: &str,
) -> Result<String, String> {
    let params = crate::service::aiclient::VideoTaskParams {
        prompt: prompt.to_string(),
        mode: "text".to_string(),
        seconds: clamp_seconds(duration),
        aspect_ratio: aspect_ratio.to_string(),
        first_frame: String::new(),
        last_frame: String::new(),
        images: Vec::new(),
    };
    let video_id = client.create_video_task(&params).await?;
    let video_url = client.poll_video_task(&video_id, Duration::from_secs(1800)).await?;
    let dest = PathBuf::from(tmp_dir).join("video.mp4");
    client.download_video(&video_url, dest.to_str().unwrap()).await?;
    Ok(dest.to_string_lossy().to_string())
}

pub async fn make_keyframe_video_ai(
    client: &AIClient,
    tmp_dir: &str,
    first_frame_url: &str,
    last_frame_url: &str,
    prompt: &str,
    duration: i32,
    aspect_ratio: &str,
) -> Result<String, String> {
    let params = crate::service::aiclient::VideoTaskParams {
        prompt: prompt.to_string(),
        mode: "keyframe".to_string(),
        seconds: clamp_seconds(duration),
        aspect_ratio: aspect_ratio.to_string(),
        first_frame: first_frame_url.to_string(),
        last_frame: last_frame_url.to_string(),
        images: Vec::new(),
    };
    let video_id = client.create_video_task(&params).await?;
    let video_url = client.poll_video_task(&video_id, Duration::from_secs(1800)).await?;
    let dest = PathBuf::from(tmp_dir).join("keyframe_video.mp4");
    client.download_video(&video_url, dest.to_str().unwrap()).await?;
    Ok(dest.to_string_lossy().to_string())
}

pub async fn make_ref_video_ai(
    client: &AIClient,
    tmp_dir: &str,
    prompt: &str,
    image_urls: &[String],
    duration: i32,
    aspect_ratio: &str,
) -> Result<String, String> {
    let params = crate::service::aiclient::VideoTaskParams {
        prompt: prompt.to_string(),
        mode: "reference".to_string(),
        seconds: clamp_seconds(duration),
        aspect_ratio: aspect_ratio.to_string(),
        first_frame: String::new(),
        last_frame: String::new(),
        images: image_urls.to_vec(),
    };
    let video_id = client.create_video_task(&params).await?;
    let video_url = client.poll_video_task(&video_id, Duration::from_secs(1800)).await?;
    let dest = PathBuf::from(tmp_dir).join("ref_video.mp4");
    client.download_video(&video_url, dest.to_str().unwrap()).await?;
    Ok(dest.to_string_lossy().to_string())
}

/// ensure_public_url converts a local file path to a public URL via image API,
/// or passes through if already a public HTTP(S) URL.
pub async fn ensure_public_url(
    client: &AIClient,
    input: &str,
    gen_prompt: &str,
) -> Result<String, String> {
    if input.is_empty() {
        return Ok(String::new());
    }
    // If it's already an HTTP(S) URL, pass through
    if input.starts_with("http://") || input.starts_with("https://") {
        return Ok(input.to_string());
    }
    // For local files, try to read as image and convert to data URI
    if let Ok(data_uri) = AIClient::file_to_data_uri(input) {
        return Ok(data_uri);
    }
    // Fall back to generating via image API
    match client.gen_image_agnes("agnes-image-2.1-flash", gen_prompt, "1K", "16:9", &[]).await {
        Ok((img_url, _)) => Ok(img_url),
        Err(e) => Err(format!("图片处理失败: {}", e)),
    }
}

/// MakeLongTextVideoAI generates a long video by splitting into segments,
/// generating each, then concatenating with ffmpeg.
pub async fn make_long_text_video_ai(
    client: &AIClient,
    tmp_dir: &str,
    prompt: &str,
    total_duration: i32,
    aspect_ratio: &str,
) -> Result<String, String> {
    if !client.has_agnes() {
        return Err("Agnes API未配置".to_string());
    }
    let segs = split_duration(total_duration);
    if segs.is_empty() {
        return Err("时长参数无效".to_string());
    }
    let n = segs.len();
    let mut seg_paths: Vec<String> = Vec::with_capacity(n);
    let mut errs: Vec<Option<String>> = vec![None; n];
    for (i, seg_dur) in segs.iter().enumerate() {
        if i > 0 {
            tokio::time::sleep(Duration::from_secs(5)).await;
        }
        let seg_path = format!("{}/seg_{:03}.mp4", tmp_dir, i);
        let seg_prompt = segment_stage_prompt(prompt, i, n);
        let params = crate::service::aiclient::VideoTaskParams {
            prompt: seg_prompt,
            mode: "text".to_string(),
            seconds: (*seg_dur).to_string(),
            aspect_ratio: aspect_ratio.to_string(),
            first_frame: String::new(),
            last_frame: String::new(),
            images: Vec::new(),
        };
        if let Err(e) = generate_video_segment(client, &seg_path, &params, &format!("text-{}", i + 1)).await {
            errs[i] = Some(format!("第{}段生成失败: {}", i + 1, e));
            eprintln!("[Video] 分段 {} 失败: {}", i + 1, e);
            continue;
        }
        seg_paths.push(seg_path);
    }
    if seg_paths.is_empty() {
        return Err(errs.iter().filter_map(|e| e.clone()).next().unwrap_or("所有分段生成失败".to_string()));
    }
    if seg_paths.len() == 1 {
        return Ok(seg_paths[0].clone());
    }
    let dest = format!("{}/long_video.mp4", tmp_dir);
    concat_videos(tmp_dir, &seg_paths, &dest)
}

/// MakeLongKeyframeVideoAI generates a long keyframe video by splitting.
pub async fn make_long_keyframe_video_ai(
    client: &AIClient,
    tmp_dir: &str,
    first_frame_url: &str,
    last_frame_url: &str,
    prompt: &str,
    total_duration: i32,
    aspect_ratio: &str,
) -> Result<String, String> {
    if !client.has_agnes() {
        return Err("Agnes API未配置".to_string());
    }
    let segs = split_duration(total_duration);
    if segs.is_empty() {
        return Err("时长参数无效".to_string());
    }
    let n = segs.len();
    let mut seg_paths: Vec<String> = Vec::with_capacity(n);
    let mut errs: Vec<Option<String>> = vec![None; n];

    for (i, seg_dur) in segs.iter().enumerate() {
        if i > 0 {
            tokio::time::sleep(Duration::from_secs(5)).await;
        }
        let seg_path = format!("{}/kf_seg_{:03}.mp4", tmp_dir, i);
        let seg_prompt = segment_stage_prompt(prompt, i, n);
        let mut params = crate::service::aiclient::VideoTaskParams {
            prompt: seg_prompt,
            mode: "keyframe".to_string(),
            seconds: (*seg_dur).to_string(),
            aspect_ratio: aspect_ratio.to_string(),
            first_frame: String::new(),
            last_frame: String::new(),
            images: Vec::new(),
        };
        match i {
            0 if !first_frame_url.is_empty() => {
                params.first_frame = first_frame_url.to_string();
            }
            x if x == n - 1 && !last_frame_url.is_empty() => {
                params.last_frame = last_frame_url.to_string();
            }
            _ => {
                params.mode = "text".to_string();
            }
        }
        if let Err(e) = generate_video_segment(client, &seg_path, &params, &format!("kf-{}", i + 1)).await {
            errs[i] = Some(format!("第{}段生成失败: {}", i + 1, e));
            eprintln!("[Video] 分段 {} 失败: {}", i + 1, e);
            continue;
        }
        seg_paths.push(seg_path);
    }
    if seg_paths.is_empty() {
        return Err(errs.iter().filter_map(|e| e.clone()).next().unwrap_or("所有分段生成失败".to_string()));
    }
    if seg_paths.len() == 1 {
        return Ok(seg_paths[0].clone());
    }
    let dest = format!("{}/long_keyframe_video.mp4", tmp_dir);
    concat_videos(tmp_dir, &seg_paths, &dest)
}

/// MakeLongRefVideoAI generates a long reference-guided video by splitting.
pub async fn make_long_ref_video_ai(
    client: &AIClient,
    tmp_dir: &str,
    prompt: &str,
    image_urls: &[String],
    total_duration: i32,
    aspect_ratio: &str,
) -> Result<String, String> {
    if !client.has_agnes() {
        return Err("Agnes API未配置".to_string());
    }
    if image_urls.is_empty() {
        return Err("无有效参考图片".to_string());
    }
    let mut public_urls = image_urls.to_vec();
    if public_urls.len() > 5 {
        public_urls.truncate(5);
    }
    let segs = split_duration(total_duration);
    if segs.is_empty() {
        return Err("时长参数无效".to_string());
    }
    let n = segs.len();
    let mut seg_paths: Vec<String> = Vec::with_capacity(n);
    let mut errs: Vec<Option<String>> = vec![None; n];
    let ref_prefix = "Use <Picture 1> as reference. ";

    for (i, seg_dur) in segs.iter().enumerate() {
        if i > 0 {
            tokio::time::sleep(Duration::from_secs(5)).await;
        }
        let seg_path = format!("{}/ref_seg_{:03}.mp4", tmp_dir, i);
        let seg_prompt = format!("{}{}", ref_prefix, segment_stage_prompt(prompt, i, n));
        let params = crate::service::aiclient::VideoTaskParams {
            prompt: seg_prompt,
            mode: "reference".to_string(),
            seconds: (*seg_dur).to_string(),
            aspect_ratio: aspect_ratio.to_string(),
            first_frame: String::new(),
            last_frame: String::new(),
            images: public_urls.clone(),
        };
        if let Err(e) = generate_video_segment(client, &seg_path, &params, &format!("ref-{}", i + 1)).await {
            errs[i] = Some(format!("第{}段生成失败: {}", i + 1, e));
            eprintln!("[Video] 分段 {} 失败: {}", i + 1, e);
            continue;
        }
        seg_paths.push(seg_path);
    }
    if seg_paths.is_empty() {
        return Err(errs.iter().filter_map(|e| e.clone()).next().unwrap_or("所有分段生成失败".to_string()));
    }
    if seg_paths.len() == 1 {
        return Ok(seg_paths[0].clone());
    }
    let dest = format!("{}/long_ref_video.mp4", tmp_dir);
    concat_videos(tmp_dir, &seg_paths, &dest)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_marshal_payload() {
        let mut fields = serde_json::Map::new();
        fields.insert("prompt".to_string(), serde_json::json!("test"));
        fields.insert("duration".to_string(), serde_json::json!(5));
        let result = super::marshal_video_payload(&fields).unwrap();
        let parsed: serde_json::Value = serde_json::from_slice(&result).unwrap();
        assert_eq!(parsed["prompt"], "test");
        assert_eq!(parsed["duration"], 5);
    }

    #[test]
    fn test_clamp_seconds() {
        assert_eq!(clamp_seconds(2), "4");
        assert_eq!(clamp_seconds(4), "4");
        assert_eq!(clamp_seconds(8), "8");
        assert_eq!(clamp_seconds(12), "12");
        assert_eq!(clamp_seconds(15), "12");
    }

    #[test]
    fn test_split_duration() {
        assert_eq!(split_duration(4), vec![4]);
        assert_eq!(split_duration(12), vec![12]);
        assert_eq!(split_duration(24), vec![12, 12]);
        assert_eq!(split_duration(25), vec![9, 8, 8]);
        assert_eq!(split_duration(30), vec![10, 10, 10]);
        assert_eq!(split_duration(100).iter().sum::<i32>(), 100);
    }

    #[test]
    fn test_segment_stage_prompt() {
        let prompt = "枫叶红在路的两边，两个人，回忆往事";
        assert!(segment_stage_prompt(prompt, 0, 3).contains("故事开端"));
        assert!(segment_stage_prompt(prompt, 2, 3).contains("故事结尾"));
        assert!(segment_stage_prompt(prompt, 1, 3).contains("第2阶段"));
    }

    #[test]
    fn test_split_prompt_clauses() {
        let clauses = split_prompt_clauses("枫叶红在路的两边，两个人，回忆往事");
        assert_eq!(clauses.len(), 3);
        assert_eq!(clauses[0], "枫叶红在路的两边");
        assert_eq!(clauses[1], "两个人");
        assert_eq!(clauses[2], "回忆往事");
    }

    #[test]
    fn test_is_transient_video_err() {
        assert!(is_transient_video_err("429 rate limit"));
        assert!(is_transient_video_err("no result"));
        assert!(is_transient_video_err("video_queue_full"));
        assert!(!is_transient_video_err("invalid prompt"));
    }
}
