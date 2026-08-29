package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
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
	AI    *service.AIClient
	Jobs  *VideoJobStore
}

func (h *VideoGenH) writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *VideoGenH) writeErr(w http.ResponseWriter, status int, msg string) {
	h.writeJSON(w, status, map[string]interface{}{"success": false, "error": msg})
}

func (h *VideoGenH) safeJobErr(jobID string, err error) {
	fmt.Fprintf(os.Stderr, "[VideoJob %s] error: %v\n", jobID, err)
	h.Jobs.SetError(jobID, "视频生成失败，请稍后重试")
}

// HandleTextVideo: POST /api/convert/video/text
// Creates an async job and returns its task_id immediately; progress is
// polled via GET /api/convert/video/task/{id}.
func (h *VideoGenH) HandleTextVideo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeErr(w, http.StatusMethodNotAllowed, "仅支持POST请求")
		return
	}

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		h.writeErr(w, http.StatusBadRequest, "请求体过大")
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
	if duration > 120 {
		duration = 120
	}
	aspectRatio := validAspectRatio(strings.TrimSpace(r.FormValue("aspect_ratio")))

	tmp, err := h.newTmp()
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}

	job := h.Jobs.Create()
	if !h.Jobs.AcquireSlot() {
		h.Jobs.Delete(job.ID)
		h.writeErr(w, http.StatusServiceUnavailable, "服务器繁忙，请稍后重试")
		return
	}
	go func() {
		defer h.Jobs.ReleaseSlot()
		defer h.cleanupTmp(tmp)

		var dest string
		var genErr error
		if h.AI != nil {
			if duration > 12 {
				dest, genErr = service.MakeLongTextVideoAI(h.AI, tmp, prompt, duration, aspectRatio)
			} else {
				dest, genErr = service.MakeTextVideoAI(h.AI, tmp, prompt, duration, aspectRatio)
			}
		}
		if dest == "" || genErr != nil {
			fmt.Fprintf(os.Stderr, "[VideoTextJob %s] AI failed: %v, fallback to python\n", job.ID, genErr)
			if genErr != nil {
				h.Jobs.SetNotice(job.ID, "AI 生成失败，已降级为本地合成视频")
			} else {
				h.Jobs.SetNotice(job.ID, "AI 不可用，已降级为本地合成视频")
			}
			dest, genErr = service.MakeTextVideo(tmp, prompt, duration)
		}
		if genErr != nil {
			fmt.Fprintf(os.Stderr, "[VideoTextJob %s] FINAL ERROR: %v\n", job.ID, genErr)
			h.Jobs.SetError(job.ID, "视频生成失败，请稍后重试")
			return
		}
		dl, err := h.registerOutput(dest, "video.mp4")
		if err != nil {
			fmt.Fprintf(os.Stderr, "[VideoTextJob %s] registerOutput failed: %v\n", job.ID, err)
			h.Jobs.SetError(job.ID, "服务器错误")
			return
		}
		fmt.Fprintf(os.Stderr, "[VideoTextJob %s] DONE: %s\n", job.ID, dl)
		h.Jobs.SetComplete(job.ID, dl)
	}()

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"task_id": job.ID,
	})
}

// HandleKeyframeVideo: POST /api/convert/video/keyframe
func (h *VideoGenH) HandleKeyframeVideo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeErr(w, http.StatusMethodNotAllowed, "仅支持POST请求")
		return
	}

	prompt := strings.TrimSpace(r.FormValue("prompt"))
	if len(prompt) > maxPromptLen {
		h.writeErr(w, http.StatusBadRequest, fmt.Sprintf("提示词长度不能超过%d个字符", maxPromptLen))
		return
	}

	firstFrame, firstHeader, err := r.FormFile("first_frame")
	if err != nil {
		h.writeErr(w, http.StatusBadRequest, "请上传首帧图片")
		return
	}
	defer firstFrame.Close()

	lastFrame, lastHeader, err := r.FormFile("last_frame")
	if err != nil {
		h.writeErr(w, http.StatusBadRequest, "请上传尾帧图片")
		return
	}
	defer lastFrame.Close()

	if firstHeader.Size > 20<<20 || !isValidImageType(firstHeader.Filename) {
		h.writeErr(w, http.StatusBadRequest, "首帧图片格式或大小无效")
		return
	}
	if lastHeader.Size > 20<<20 || !isValidImageType(lastHeader.Filename) {
		h.writeErr(w, http.StatusBadRequest, "尾帧图片格式或大小无效")
		return
	}

	duration, _ := strconv.Atoi(r.FormValue("duration"))
	if duration <= 0 {
		duration = 5
	}
	if duration > 120 {
		duration = 120
	}
	aspectRatio := validAspectRatio(strings.TrimSpace(r.FormValue("aspect_ratio")))

	tmp, err := h.newTmp()
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}

	firstPath := filepath.Join(tmp, "first.png")
	lastPath := filepath.Join(tmp, "last.png")

	for _, pair := range []struct {
		path  string
		file  multipart.File
		label string
	}{
		{firstPath, firstFrame, "首帧"},
		{lastPath, lastFrame, "尾帧"},
	} {
		out, err := os.Create(pair.path)
		if err != nil {
			h.writeErr(w, http.StatusInternalServerError, fmt.Sprintf("保存%s图片失败", pair.label))
			return
		}
		if _, err := io.Copy(out, pair.file); err != nil {
			out.Close()
			h.writeErr(w, http.StatusInternalServerError, fmt.Sprintf("读取%s图片失败", pair.label))
			return
		}
		out.Close()
	}

	// Save frames to public output dir so Agnes API can fetch them
	firstURL, err := h.uploadImageToPublic(firstPath, "first.png")
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, "保存图片失败")
		return
	}
	lastURL, err := h.uploadImageToPublic(lastPath, "last.png")
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, "保存图片失败")
		return
	}

	job := h.Jobs.Create()
	if !h.Jobs.AcquireSlot() {
		h.Jobs.Delete(job.ID)
		h.writeErr(w, http.StatusServiceUnavailable, "服务器繁忙，请稍后重试")
		return
	}
	go func() {
		defer h.Jobs.ReleaseSlot()
		defer h.cleanupTmp(tmp)

		dest := filepath.Join(tmp, "keyframe_video.mp4")
		var destPath string
		var aiErr error
		fmt.Fprintf(os.Stderr, "[VideoKFJob %s] start: duration=%d ratio=%s prompt=%s\n", job.ID, duration, aspectRatio, prompt)
		if h.AI != nil {
			if duration > 12 {
				destPath, aiErr = service.MakeLongKeyframeVideoAI(h.AI, tmp, firstURL, lastURL, prompt, duration, aspectRatio)
			} else {
				destPath, aiErr = service.MakeKeyframeVideoAI(h.AI, tmp, firstURL, lastURL, prompt, duration, aspectRatio)
			}
		}
		if destPath == "" || aiErr != nil {
			fmt.Fprintf(os.Stderr, "[VideoKFJob %s] AI failed: %v, fallback\n", job.ID, aiErr)
			if aiErr != nil {
				h.Jobs.SetNotice(job.ID, "AI 生成失败，已降级为本地合成视频")
			} else {
				h.Jobs.SetNotice(job.ID, "AI 不可用，已降级为本地合成视频")
			}
			destPath, aiErr = service.MakeKeyframeVideo(tmp, firstPath, lastPath, prompt, duration)
		}
		if aiErr != nil {
			fmt.Fprintf(os.Stderr, "[VideoKFJob %s] FINAL ERROR: %v\n", job.ID, aiErr)
			h.Jobs.SetError(job.ID, "视频生成失败，请稍后重试")
			return
		}
		dest = destPath

		dl, err := h.registerOutput(dest, "video.mp4")
		if err != nil {
			fmt.Fprintf(os.Stderr, "[VideoKFJob %s] registerOutput failed: %v\n", job.ID, err)
			h.Jobs.SetError(job.ID, "服务器错误")
			return
		}
		fmt.Fprintf(os.Stderr, "[VideoKFJob %s] DONE: %s\n", job.ID, dl)
		h.Jobs.SetComplete(job.ID, dl)
	}()

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"task_id": job.ID,
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
	if len(prompt) > maxPromptLen {
		h.writeErr(w, http.StatusBadRequest, fmt.Sprintf("提示词长度不能超过%d个字符", maxPromptLen))
		return
	}

	duration, _ := strconv.Atoi(r.FormValue("duration"))
	if duration <= 0 {
		duration = 5
	}
	if duration > 120 {
		duration = 120
	}
	aspectRatio := validAspectRatio(strings.TrimSpace(r.FormValue("aspect_ratio")))

	if err := r.ParseMultipartForm(20 << 20); err != nil {
		h.writeErr(w, http.StatusBadRequest, "上传文件大小超限（单文件最大20MB）")
		return
	}

	tmp, err := h.newTmp()
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}

	var refPaths []string
	for i := 0; ; i++ {
		field := fmt.Sprintf("ref_%d", i)
		file, header, err := r.FormFile(field)
		if err != nil {
			break
		}
		if header.Size > 20<<20 {
			file.Close()
			h.writeErr(w, http.StatusBadRequest, fmt.Sprintf("参考图 %d 超过20MB", i))
			return
		}
		if !isValidImageType(header.Filename) {
			file.Close()
			h.writeErr(w, http.StatusBadRequest, fmt.Sprintf("参考图 %d 格式不支持", i))
			return
		}
		refName := fmt.Sprintf("ref_%d.png", i)
		refPath := filepath.Join(tmp, refName)
		refOut, err := os.Create(refPath)
		if err != nil {
			file.Close()
			break
		}
		if _, err := io.Copy(refOut, file); err != nil {
			refOut.Close()
			file.Close()
			os.Remove(refPath)
			break
		}
		refOut.Close()
		file.Close()
		refPaths = append(refPaths, refPath)
		if len(refPaths) >= 5 {
			break
		}
	}

	// Upload ref images to public dir for Agnes API
	var refURLs []string
	for i, refPath := range refPaths {
		refName := fmt.Sprintf("ref_%d.png", i)
		url, err := h.uploadImageToPublic(refPath, refName)
		if err != nil || url == "" {
			continue
		}
		refURLs = append(refURLs, url)
	}

	job := h.Jobs.Create()
	if !h.Jobs.AcquireSlot() {
		h.Jobs.Delete(job.ID)
		h.writeErr(w, http.StatusServiceUnavailable, "服务器繁忙，请稍后重试")
		return
	}
	go func() {
		defer h.Jobs.ReleaseSlot()
		defer h.cleanupTmp(tmp)

		var dest string
		var genErr error
		fmt.Fprintf(os.Stderr, "[VideoRefJob %s] start: duration=%d ratio=%s prompt=%s refCount=%d\n", job.ID, duration, aspectRatio, prompt, len(refPaths))
		if h.AI != nil {
			if duration > 12 {
				dest, genErr = service.MakeLongRefVideoAI(h.AI, tmp, prompt, refURLs, duration, aspectRatio)
			} else {
				dest, genErr = service.MakeRefVideoAI(h.AI, tmp, prompt, refURLs, duration, aspectRatio)
			}
		}
		if dest == "" || genErr != nil {
			fmt.Fprintf(os.Stderr, "[VideoRefJob %s] AI failed: %v, fallback\n", job.ID, genErr)
			if genErr != nil {
				h.Jobs.SetNotice(job.ID, "AI 生成失败，已降级为本地合成视频")
			} else {
				h.Jobs.SetNotice(job.ID, "AI 不可用，已降级为本地合成视频")
			}
			dest, genErr = service.MakeRefVideo(tmp, prompt, refPaths, duration)
		}
		if genErr != nil {
			fmt.Fprintf(os.Stderr, "[VideoRefJob %s] FINAL ERROR: %v\n", job.ID, genErr)
			h.Jobs.SetError(job.ID, "视频生成失败，请稍后重试")
			return
		}
		dl, err := h.registerOutput(dest, "video.mp4")
		if err != nil {
			fmt.Fprintf(os.Stderr, "[VideoRefJob %s] registerOutput failed: %v\n", job.ID, err)
			h.Jobs.SetError(job.ID, "服务器错误")
			return
		}
		fmt.Fprintf(os.Stderr, "[VideoRefJob %s] DONE: %s\n", job.ID, dl)
		h.Jobs.SetComplete(job.ID, dl)
	}()

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"task_id": job.ID,
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
	return h.Store.Register(path, base)
}

// uploadImageToPublic copies a local image into the persistent output dir
// under a unique name (no shared filenames, so concurrent jobs don't overwrite
// each other) and returns its public URL.
func (h *VideoGenH) uploadImageToPublic(localPath, baseName string) (string, error) {
	name, err := h.Store.Register(localPath, baseName)
	if err != nil {
		return "", err
	}
	return h.Cfg.BaseURL + name, nil
}

func isValidImageType(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp":
		return true
	}
	return false
}

var validAspectRatios = map[string]bool{
	"16:9": true, "9:16": true, "1:1": true,
	"4:3": true, "3:4": true, "2:3": true,
	"3:2": true, "21:9": true,
}

func validAspectRatio(r string) string {
	if validAspectRatios[r] {
		return r
	}
	return "16:9"
}
