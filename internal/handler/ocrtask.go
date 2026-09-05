package handler

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"flowconvert/internal/service"
)

// OcrJob tracks an asynchronous OCR recognition task.
type OcrJob struct {
	ID        string
	Status    string
	Error     string
	Result    map[string]interface{}
	CreatedAt time.Time
}

// OcrJobStore holds in-memory OCR jobs and garbage-collects stale entries.
type OcrJobStore struct {
	mu   sync.Mutex
	jobs map[string]*OcrJob
	ttl  time.Duration
	sem  chan struct{}
}

const maxOcrConcurrency = 2

func NewOcrJobStore(ttl time.Duration) *OcrJobStore {
	s := &OcrJobStore{
		jobs: make(map[string]*OcrJob),
		ttl:  ttl,
		sem:  make(chan struct{}, maxOcrConcurrency),
	}
	go s.gcLoop()
	return s
}

func (s *OcrJobStore) Create() *OcrJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := &OcrJob{
		ID:        service.NewID(16),
		Status:    "running",
		CreatedAt: time.Now(),
	}
	s.jobs[j.ID] = j
	return j
}

func (s *OcrJobStore) Get(id string) *OcrJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		cp := *j
		if j.Result != nil {
			cp.Result = j.Result
		}
		return &cp
	}
	return nil
}

func (s *OcrJobStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
}

func (s *OcrJobStore) AcquireSlot() bool {
	select {
	case s.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *OcrJobStore) ReleaseSlot() {
	<-s.sem
}

func (s *OcrJobStore) SetComplete(id string, result map[string]interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		j.Status = "completed"
		j.Result = result
	}
}

func (s *OcrJobStore) SetError(id, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		j.Status = "failed"
		j.Error = msg
	}
}

func (s *OcrJobStore) gcLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.gc()
	}
}

func (s *OcrJobStore) gc() {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-s.ttl)
	for id, j := range s.jobs {
		if j.CreatedAt.Before(cutoff) {
			delete(s.jobs, id)
		}
	}
}

// HandleOcrTask reports the status of an async OCR job.
func (h *OcrH) HandleOcrTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeErr(w, http.StatusMethodNotAllowed, "仅支持GET请求")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/ocr/task/")
	if id == "" {
		h.writeErr(w, http.StatusBadRequest, "缺少任务ID")
		return
	}
	if h.Jobs == nil {
		h.writeErr(w, http.StatusNotFound, "任务不存在或已过期")
		return
	}
	j := h.Jobs.Get(id)
	if j == nil {
		h.writeErr(w, http.StatusNotFound, "任务不存在或已过期")
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"status":  j.Status,
		"error":   j.Error,
		"result":  j.Result,
	})
}
