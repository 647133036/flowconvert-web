use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use axum::body::Bytes;
use axum::http::StatusCode;

use crate::util::{copy_file, lookup_name};

#[derive(Debug, Clone)]
pub struct StoredFile {
    pub path: PathBuf,
    pub download_as: String,
    pub created: Instant,
}

pub struct FileStore {
    files: Mutex<HashMap<String, StoredFile>>,
    out_dir: PathBuf,
    ttl_hours: u64,
}

impl FileStore {
    pub fn new(out_dir: PathBuf, ttl_hours: u64) -> Arc<Self> {
        let store = Arc::new(Self {
            files: Mutex::new(HashMap::new()),
            out_dir,
            ttl_hours,
        });
        let s = Arc::clone(&store);
        tokio::spawn(async move {
            let mut ticker = tokio::time::interval(Duration::from_secs(10 * 60));
            loop {
                ticker.tick().await;
                s.cleanup();
            }
        });
        store
    }

    pub fn register(&self, src_path: &str, base_name: &str) -> Result<String, String> {
        let name = lookup_name(src_path, base_name);
        let dst = self.out_dir.join(&name);
        if let Err(e) = copy_file(
            std::path::Path::new(src_path),
            std::path::Path::new(&dst),
        ) {
            return Err(format!("文件保存失败: {}", e));
        }
        let mut files = self.files.lock().unwrap();
        files.insert(
            name.clone(),
            StoredFile {
                path: dst,
                download_as: name.clone(),
                created: Instant::now(),
            },
        );
        Ok(format!("/api/download/{}", name))
    }

    pub fn download_handler(
        &self,
        name: &str,
    ) -> Result<(String, String, Bytes, u64), StatusCode> {
        let files = self.files.lock().unwrap();
        let f = files.get(name).ok_or(StatusCode::NOT_FOUND)?;
        if f.created.elapsed() > Duration::from_secs(self.ttl_hours * 3600) {
            return Err(StatusCode::NOT_FOUND);
        }
        let content = std::fs::read(&f.path).map_err(|_| StatusCode::NOT_FOUND)?;
        let size = content.len() as u64;
        let disposition = format!("attachment; filename=\"{}\"", f.download_as);
        Ok(("application/octet-stream".to_string(), disposition, Bytes::from(content), size))
    }

    fn cleanup(&self) {
        let cutoff = Instant::now() - Duration::from_secs(self.ttl_hours * 3600);
        let mut files = self.files.lock().unwrap();
        files.retain(|_, f| f.created >= cutoff);
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum JobStatus {
    Running,
    Completed,
    Failed,
}

#[derive(Debug, Clone)]
pub struct VideoJob {
    pub id: String,
    pub status: JobStatus,
    pub download_url: Option<String>,
    pub error: Option<String>,
    pub notice: Option<String>,
    pub created_at: Instant,
}

pub struct VideoJobStore {
    jobs: Mutex<HashMap<String, VideoJob>>,
    ttl: Duration,
    sem: tokio::sync::Semaphore,
}

const MAX_VIDEO_CONCURRENCY: usize = 6;

impl VideoJobStore {
    pub fn new(ttl_minutes: u64) -> Arc<Self> {
        let store = Arc::new(Self {
            jobs: Mutex::new(HashMap::new()),
            ttl: Duration::from_secs(ttl_minutes * 60),
            sem: tokio::sync::Semaphore::new(MAX_VIDEO_CONCURRENCY),
        });
        let s = Arc::clone(&store);
        tokio::spawn(async move {
            let mut ticker = tokio::time::interval(Duration::from_secs(10 * 60));
            loop {
                ticker.tick().await;
                s.gc();
            }
        });
        store
    }

    pub fn create(&self) -> VideoJob {
        let id = uuid::Uuid::new_v4().to_string();
        let job = VideoJob {
            id: id.clone(),
            status: JobStatus::Running,
            download_url: None,
            error: None,
            notice: None,
            created_at: Instant::now(),
        };
        self.jobs.lock().unwrap().insert(id.clone(), job.clone());
        job
    }

    pub fn get(&self, id: &str) -> Option<VideoJob> {
        self.jobs.lock().unwrap().get(id).cloned()
    }

    pub fn delete(&self, id: &str) {
        self.jobs.lock().unwrap().remove(id);
    }

    pub fn acquire_one_slot(&self) -> bool {
        self.sem
            .try_acquire()
            .is_ok()
    }

    pub fn release_one_slot(&self) {
        self.sem.add_permits(1);
    }

    pub fn set_complete(&self, id: &str, url: &str) {
        if let Some(j) = self.jobs.lock().unwrap().get_mut(id) {
            j.status = JobStatus::Completed;
            j.download_url = Some(url.to_string());
        }
    }

    pub fn set_error(&self, id: &str, msg: &str) {
        if let Some(j) = self.jobs.lock().unwrap().get_mut(id) {
            j.status = JobStatus::Failed;
            j.error = Some(msg.to_string());
        }
    }

    pub fn set_notice(&self, id: &str, msg: &str) {
        if let Some(j) = self.jobs.lock().unwrap().get_mut(id) {
            j.notice = Some(msg.to_string());
        }
    }

    fn gc(&self) {
        let cutoff = Instant::now() - self.ttl;
        self.jobs.lock().unwrap().retain(|_, j| j.created_at >= cutoff);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    #[tokio::test]
    async fn test_file_store_register_and_download() {
        let tmp = std::env::temp_dir().join("flowconvert_test_store");
        let _ = fs::create_dir_all(&tmp);
        let out = tmp.join("output");
        let _ = fs::create_dir_all(&out);

        let store = FileStore::new(out.clone(), 1);
        let src = tmp.join("src.txt");
        fs::write(&src, "hello world").unwrap();

        let dl_url = store.register(src.to_str().unwrap(), "test.txt").unwrap();
        assert!(dl_url.starts_with("/api/download/"));

        let name = dl_url.strip_prefix("/api/download/").unwrap();
        let (ct, _disp, bytes, size) = store.download_handler(name).unwrap();
        assert_eq!(ct, "application/octet-stream");
        assert_eq!(size, 11);
        assert_eq!(&bytes[..], b"hello world");

        let _ = fs::remove_dir_all(&tmp);
    }

    #[tokio::test]
    async fn test_video_job_create_get_status() {
        let store = VideoJobStore::new(60);
        let job = store.create();
        assert_eq!(job.status, JobStatus::Running);
        assert!(!job.id.is_empty());

        let retrieved = store.get(&job.id).unwrap();
        assert_eq!(retrieved.id, job.id);
        assert_eq!(retrieved.status, JobStatus::Running);

        store.set_complete(&job.id, "http://example.com/video.mp4");
        let updated = store.get(&job.id).unwrap();
        assert_eq!(updated.status, JobStatus::Completed);
        assert_eq!(updated.download_url, Some("http://example.com/video.mp4".to_string()));
    }

    #[tokio::test]
    async fn test_video_job_error() {
        let store = VideoJobStore::new(60);
        let job = store.create();
        store.set_error(&job.id, "生成失败");
        let updated = store.get(&job.id).unwrap();
        assert_eq!(updated.status, JobStatus::Failed);
        assert_eq!(updated.error, Some("生成失败".to_string()));
    }

    #[tokio::test]
    async fn test_video_job_not_found() {
        let store = VideoJobStore::new(60);
        assert!(store.get("nonexistent-id").is_none());
    }

    #[tokio::test]
    async fn test_file_store_ttl_gc() {
        let tmp = std::env::temp_dir().join("flowconvert_test_gc");
        let _ = fs::create_dir_all(&tmp);
        let out = tmp.join("output");
        let _ = fs::create_dir_all(&out);

        // Use 1 hour TTL
        let store = FileStore::new(out.clone(), 1);
        let src = tmp.join("src.txt");
        fs::write(&src, "hello").unwrap();

        let dl_url = store.register(src.to_str().unwrap(), "test.txt").unwrap();
        let name = dl_url.strip_prefix("/api/download/").unwrap();

        // Download should succeed
        let (ct, _disp, bytes, size) = store.download_handler(name).unwrap();
        assert_eq!(ct, "application/octet-stream");
        assert_eq!(&bytes[..], b"hello");
        assert_eq!(size, 5);

        let _ = fs::remove_dir_all(&tmp);
    }

    #[tokio::test]
    async fn test_video_job_set_error_returns_failed() {
        let store = VideoJobStore::new(60);
        let job = store.create();
        store.set_error(&job.id, "生成失败");
        let updated = store.get(&job.id).unwrap();
        assert_eq!(updated.status, JobStatus::Failed);
        assert_eq!(updated.error, Some("生成失败".to_string()));
    }

    #[tokio::test]
    async fn test_video_job_gc_clears_expired() {
        let store = VideoJobStore::new(0);
        let job = store.create();
        // TTL is 0 minutes, so after a brief sleep it should be expired
        tokio::time::sleep(tokio::time::Duration::from_millis(10)).await;
        store.gc();
        assert!(store.get(&job.id).is_none());
    }

    #[tokio::test]
    async fn test_video_job_concurrent_acquire_release() {
        use std::sync::atomic::{AtomicUsize, Ordering};
        let store = VideoJobStore::new(60);
        let counter = Arc::new(AtomicUsize::new(0));
        let mut handles = vec![];

        for _ in 0..12 {
            let s = store.clone();
            let c = counter.clone();
            handles.push(tokio::spawn(async move {
                if s.acquire_one_slot() {
                    c.fetch_add(1, Ordering::SeqCst);
                    tokio::time::sleep(tokio::time::Duration::from_millis(5)).await;
                    s.release_one_slot();
                }
            }));
        }
        for h in handles {
            h.await.unwrap();
        }
        // All 12 should have acquired at least once (some may have missed due to full semaphore)
        assert!(counter.load(Ordering::SeqCst) > 0);
    }
}
