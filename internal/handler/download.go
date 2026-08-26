package handler

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"flowconvert/internal/config"
	"flowconvert/internal/service"
)

// FileStore registers output files for download and garbage collects old ones.
type FileStore struct {
	mu       sync.RWMutex
	files    map[string]storedFile
	outDir   string
	ttlHours int
}

type storedFile struct {
	path       string
	downloadAs string
	created    time.Time
	registerAt time.Time
}

func NewFileStore(cfg *config.Config) *FileStore {
	fs := &FileStore{
		files:    make(map[string]storedFile),
		outDir:   cfg.OutDir,
		ttlHours: cfg.TTLHours,
	}
	go fs.gcLoop()
	return fs
}

// lookupName builds a unique download name for a file.
func lookupName(path, baseName string) string {
	name := filepath.Base(path)
	if baseName != "" {
		// sanitize baseName
		baseName = filepath.Base(baseName)
		if strings.ContainsAny(baseName, "/\\") {
			baseName = ""
		}
		if baseName != "" {
			name = baseName
		}
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	return fmt.Sprintf("%d_%s%s", time.Now().UnixNano(), stem, ext)
}

// Register assigns a download name and returns the URL path.
// It copies the source file into the persistent output directory.
func (fs *FileStore) Register(path, baseName string) string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	name := lookupName(path, baseName)
	dst := filepath.Join(fs.outDir, name)
	if err := service.CopyFile(path, dst); err != nil {
		// Fall back to registering the original path
		fs.files[name] = storedFile{
			path:       path,
			downloadAs: name,
			created:    time.Now(),
			registerAt: time.Now(),
		}
		return "/api/download/" + name
	}
	fs.files[name] = storedFile{
		path:       dst,
		downloadAs: name,
		created:    time.Now(),
		registerAt: time.Now(),
	}
	return "/api/download/" + name
}

func (fs *FileStore) gcLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		fs.cleanup()
	}
}

func (fs *FileStore) cleanup() {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	cutoff := time.Now().Add(-time.Duration(fs.ttlHours) * time.Hour)
	for name, f := range fs.files {
		if f.created.Before(cutoff) {
			_ = os.Remove(f.path)
			delete(fs.files, name)
		}
	}
}

// DownloadHandler serves /api/download/{name}.
func (fs *FileStore) DownloadHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/download/")
	fs.mu.RLock()
	f, ok := fs.files[name]
	fs.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	fd, err := os.Open(f.path)
	if err != nil {
		http.Error(w, "文件不存在或已过期", http.StatusNotFound)
		return
	}
	defer fd.Close()
	stat, _ := fd.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+f.downloadAs+"\"")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", stat.Size()))
	_, _ = io.Copy(w, fd)
}