package handler

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"flowconvert/internal/service"
)

// VideoJob tracks an asynchronous video generation task.
type VideoJob struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"` // running, completed, failed
	DownloadURL string    `json:"download_url,omitempty"`
	Error       string    `json:"error,omitempty"`
	Notice      string    `json:"notice,omitempty"`
	CreatedAt   time.Time `json:"-"`
	DoneAt      time.Time `json:"-"` // 完成/失败时刻；运行中为零值
}

// VideoJobStore holds in-memory video generation jobs and garbage
// collects stale entries so the map does not grow unbounded.
type VideoJobStore struct {
	mu   sync.Mutex
	jobs map[string]*VideoJob
	ttl  time.Duration
	sem  chan struct{} // concurrency limiter
}

const maxVideoConcurrency = 6

func NewVideoJobStore(ttl time.Duration) *VideoJobStore {
	s := &VideoJobStore{
		jobs: make(map[string]*VideoJob),
		ttl:  ttl,
		sem:  make(chan struct{}, maxVideoConcurrency),
	}
	go s.gcLoop()
	return s
}

func (s *VideoJobStore) Create() *VideoJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := &VideoJob{
		ID:        service.NewID(16),
		Status:    "running",
		CreatedAt: time.Now(),
	}
	s.jobs[j.ID] = j
	return j
}

func (s *VideoJobStore) Get(id string) *VideoJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobs[id]
}

// Delete removes a job entry (used when rejecting before goroutine starts).
func (s *VideoJobStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
}

// AcquireSlot blocks until a concurrency slot is available or returns
// false if the server is at capacity.
func (s *VideoJobStore) AcquireSlot() bool {
	select {
	case s.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *VideoJobStore) ReleaseSlot() {
	<-s.sem
}

func (s *VideoJobStore) SetComplete(id, url string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		j.Status = "completed"
		j.DownloadURL = url
		j.DoneAt = time.Now()
	}
}

func (s *VideoJobStore) SetError(id, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		j.Status = "failed"
		j.Error = msg
		j.DoneAt = time.Now()
	}
}

// SetNotice attaches a non-fatal warning/message to a job that may otherwise
// complete, so the frontend can surface degraded results to the user.
func (s *VideoJobStore) SetNotice(id, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		j.Notice = msg
	}
}

func (s *VideoJobStore) gcLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.gc()
	}
}

// maxVideoJobRunTime 是运行中任务的兜底寿命：AI 视频生成可能超过 30
// 分钟，运行期间不能按创建时间回收；仅当任务异常卡死（goroutine 退出
// 但未置状态）时，超过该时长才被清理。
const maxVideoJobRunTime = 2 * time.Hour

func (s *VideoJobStore) gc() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, j := range s.jobs {
		if j.DoneAt.IsZero() {
			// 仍在运行：按创建时间给兜底寿命，避免长任务生成中途被回收
			if j.CreatedAt.Before(now.Add(-maxVideoJobRunTime)) {
				delete(s.jobs, id)
			}
			continue
		}
		// 已结束：从完成时刻起保留一个 TTL，与文件存储寿命对齐
		if j.DoneAt.Before(now.Add(-s.ttl)) {
			delete(s.jobs, id)
		}
	}
}

// HandleVideoTaskStatus reports the status of an async video job.
func (h *VideoGenH) HandleVideoTaskStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeErr(w, http.StatusMethodNotAllowed, "仅支持GET请求")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/convert/video/task/")
	if id == "" {
		h.writeErr(w, http.StatusBadRequest, "缺少任务ID")
		return
	}
	j := h.Jobs.Get(id)
	if j == nil {
		h.writeErr(w, http.StatusNotFound, "任务不存在或已过期")
		return
	}
	h.writeJSON(w, http.StatusOK, j)
}
