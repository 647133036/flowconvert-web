package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"flowconvert/internal/config"
	"flowconvert/internal/service"
)

// VideoGenH handles AI video generation endpoints.
type VideoGenH struct {
	Cfg   *config.Config
	Store *FileStore
}

func (h *VideoGenH) writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *VideoGenH) writeErr(w http.ResponseWriter, status int, msg string) {
	h.writeJSON(w, status, map[string]interface{}{"success": false, "error": msg})
}

// HandleTextVideo: POST /api/convert/video/text
func (h *VideoGenH) HandleTextVideo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeErr(w, http.StatusMethodNotAllowed, "仅支持POST请求")
		return
	}

	prompt := strings.TrimSpace(r.FormValue("prompt"))
	if prompt == "" {
		h.writeErr(w, http.StatusBadRequest, "请输入提示词")
		return
	}

	duration, _ := strconv.Atoi(r.FormValue("duration"))
	if duration <= 0 {
		duration = 3
	}

	tmp, err := h.newTmp()
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	defer h.cleanupTmp(tmp)

	dest, err := service.MakeTextVideo(tmp, prompt, duration)
	if err != nil {
		h.writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	dl, err := h.registerOutput(dest, "video.mp4")
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":      true,
		"download_url": dl,
		"format":       "mp4",
	})
}

// HandleKeyframeVideo: POST /api/convert/video/keyframe
func (h *VideoGenH) HandleKeyframeVideo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeErr(w, http.StatusMethodNotAllowed, "仅支持POST请求")
		return
	}

	prompt := strings.TrimSpace(r.FormValue("prompt"))

	firstFrame, _, err := r.FormFile("first_frame")
	if err != nil {
		h.writeErr(w, http.StatusBadRequest, "请上传首帧图片")
		return
	}
	defer firstFrame.Close()

	lastFrame, _, err := r.FormFile("last_frame")
	if err != nil {
		h.writeErr(w, http.StatusBadRequest, "请上传尾帧图片")
		return
	}
	defer lastFrame.Close()

	duration, _ := strconv.Atoi(r.FormValue("duration"))
	if duration <= 0 {
		duration = 5
	}

	tmp, err := h.newTmp()
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	defer h.cleanupTmp(tmp)

	firstPath := filepath.Join(tmp, "first.png")
	lastPath := filepath.Join(tmp, "last.png")

	out, _ := os.Create(firstPath)
	io.Copy(out, firstFrame)
	out.Close()

	out, _ = os.Create(lastPath)
	io.Copy(out, lastFrame)
	out.Close()

	dest, err := service.MakeKeyframeVideo(tmp, firstPath, lastPath, prompt, duration)
	if err != nil {
		h.writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	dl, err := h.registerOutput(dest, "video.mp4")
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":      true,
		"download_url": dl,
		"format":       "mp4",
	})
}

// HandleRefVideo: POST /api/convert/video/ref
func (h *VideoGenH) HandleRefVideo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeErr(w, http.StatusMethodNotAllowed, "仅支持POST请求")
		return
	}

	prompt := strings.TrimSpace(r.FormValue("prompt"))
	if prompt == "" {
		h.writeErr(w, http.StatusBadRequest, "请输入提示词")
		return
	}

	duration, _ := strconv.Atoi(r.FormValue("duration"))
	if duration <= 0 {
		duration = 5
	}

	tmp, err := h.newTmp()
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	defer h.cleanupTmp(tmp)

	var refPaths []string
	for i := 0; ; i++ {
		field := fmt.Sprintf("ref_%d", i)
		file, _, err := r.FormFile(field)
		if err != nil {
			break
		}
		refName := fmt.Sprintf("ref_%d.png", i)
		refPath := filepath.Join(tmp, refName)
		refOut, err := os.Create(refPath)
		if err != nil {
			file.Close()
			break
		}
		io.Copy(refOut, file)
		refOut.Close()
		file.Close()
		refPaths = append(refPaths, refPath)
		if len(refPaths) >= 3 {
			break
		}
	}

	dest, err := service.MakeRefVideo(tmp, prompt, refPaths, duration)
	if err != nil {
		h.writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	dl, err := h.registerOutput(dest, "video.mp4")
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":      true,
		"download_url": dl,
		"format":       "mp4",
	})
}

// ── Helper methods ──

func (h *VideoGenH) newTmp() (string, error) {
	dir := filepath.Join(h.Cfg.TmpDir, "vid_"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func (h *VideoGenH) cleanupTmp(dir string) {
	_ = os.RemoveAll(dir)
}

func (h *VideoGenH) registerOutput(path, base string) (string, error) {
	return h.Store.Register(path, base), nil
}
