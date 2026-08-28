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
}

// VideoJobStore holds in-memory video generation jobs and garbage
// collects stale entries so the map does not grow unbounded.
type VideoJobStore struct {
	mu   sync.Mutex
	jobs map[string]*VideoJob
	ttl  time.Duration
}

func NewVideoJobStore(ttl time.Duration) *VideoJobStore {
	s := &VideoJobStore{
		jobs: make(map[string]*VideoJob),
		ttl:  ttl,
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

func (s *VideoJobStore) SetComplete(id, url string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		j.Status = "completed"
		j.DownloadURL = url
	}
}

func (s *VideoJobStore) SetError(id, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		j.Status = "failed"
		j.Error = msg
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

func (s *VideoJobStore) gc() {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-s.ttl)
	for id, j := range s.jobs {
		if j.CreatedAt.Before(cutoff) {
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
