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

// ImageGenH handles AI image generation endpoints.
type ImageGenH struct {
	Cfg   *config.Config
	Store *FileStore
	AI    *service.AIClient
}

func (h *ImageGenH) writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *ImageGenH) writeErr(w http.ResponseWriter, status int, msg string) {
	h.writeJSON(w, status, map[string]interface{}{"success": false, "error": msg})
}

// HandleTextImage: POST /api/convert/image/text
func (h *ImageGenH) HandleTextImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeErr(w, http.StatusMethodNotAllowed, "仅支持POST请求")
		return
	}

	prompt := strings.TrimSpace(r.FormValue("prompt"))
	if prompt == "" {
		h.writeErr(w, http.StatusBadRequest, "请输入提示词")
		return
	}

	width, _ := strconv.Atoi(r.FormValue("width"))
	height, _ := strconv.Atoi(r.FormValue("height"))
	if width <= 0 || width > 4096 {
		width = 1024
	}
	if height <= 0 || height > 4096 {
		height = 1024
	}

	tmp, err := h.newTmp()
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	defer h.cleanupTmp(tmp)

	var dest string
	if h.AI != nil {
		dest, err = service.MakeImageAI(h.AI, tmp, prompt, width, height)
	}
	if dest == "" || err != nil {
		dest, err = service.MakeImage(tmp, prompt, width, height)
		if err != nil {
			h.writeErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}

	dl, err := h.registerOutput(dest, "generated.png")
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":      true,
		"download_url": dl,
		"format":       "png",
		"width":        width,
		"height":       height,
	})
}

// HandleEditImage: POST /api/convert/image/edit
func (h *ImageGenH) HandleEditImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeErr(w, http.StatusMethodNotAllowed, "仅支持POST请求")
		return
	}

	prompt := strings.TrimSpace(r.FormValue("prompt"))
	if prompt == "" {
		h.writeErr(w, http.StatusBadRequest, "请输入编辑描述")
		return
	}

	if err := r.ParseMultipartForm(20 << 20); err != nil {
		h.writeErr(w, http.StatusBadRequest, "上传文件大小超限（单文件最大20MB）")
		return
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		h.writeErr(w, http.StatusBadRequest, "请选择要编辑的图像")
		return
	}
	defer file.Close()
	if header.Size > 20<<20 || !isValidImageType(header.Filename) {
		h.writeErr(w, http.StatusBadRequest, "图片格式或大小无效")
		return
	}

	tmp, err := h.newTmp()
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	defer h.cleanupTmp(tmp)

	srcPath := filepath.Join(tmp, "src.png")
	out, err := os.Create(srcPath)
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, "保存文件失败")
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		os.Remove(srcPath)
		h.writeErr(w, http.StatusInternalServerError, "读取文件失败")
		return
	}
	out.Close()

	size := r.FormValue("size")
	var width, height int
	switch size {
	case "1k":
		width, height = 1024, 1024
	case "2k":
		width, height = 1792, 1024
	case "4k":
		width, height = 2048, 2048
	}

	var dest string
	if h.AI != nil {
		dest, err = service.MakeEditedImageAI(h.AI, tmp, srcPath, prompt, width, height)
	}
	if dest == "" || err != nil {
		dest, err = service.MakeEditedImage(tmp, srcPath, prompt, width, height)
		if err != nil {
			h.writeErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}

	dl, err := h.registerOutput(dest, "edited.png")
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":      true,
		"download_url": dl,
		"format":       "png",
	})
}

// HandleComposeImage: POST /api/convert/image/compose
func (h *ImageGenH) HandleComposeImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeErr(w, http.StatusMethodNotAllowed, "仅支持POST请求")
		return
	}

	prompt := strings.TrimSpace(r.FormValue("prompt"))
	if prompt == "" {
		h.writeErr(w, http.StatusBadRequest, "请输入提示词")
		return
	}

	tmp, err := h.newTmp()
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	defer h.cleanupTmp(tmp)

	if err := r.ParseMultipartForm(20 << 20); err != nil {
		h.writeErr(w, http.StatusBadRequest, "上传文件大小超限（单文件最大20MB）")
		return
	}

	var refPaths []string
	for i := 0; ; i++ {
		field := fmt.Sprintf("ref_%d", i)
		file, header, err := r.FormFile(field)
		if err != nil {
			break
		}
		if header.Size > 20<<20 || !isValidImageType(header.Filename) {
			file.Close()
			h.writeErr(w, http.StatusBadRequest, fmt.Sprintf("参考图 %d 格式或大小无效", i))
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
		if len(refPaths) >= 4 {
			break
		}
	}

	width, _ := strconv.Atoi(r.FormValue("width"))
	height, _ := strconv.Atoi(r.FormValue("height"))

	var dest string
	if h.AI != nil {
		dest, err = service.MakeComposeImageAI(h.AI, tmp, prompt, refPaths, width, height)
	}
	if dest == "" || err != nil {
		dest, err = service.MakeComposeImage(tmp, prompt, refPaths, width, height)
		if err != nil {
			h.writeErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}

	dl, err := h.registerOutput(dest, "composed.png")
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":      true,
		"download_url": dl,
		"format":       "png",
	})
}

// ── Helper methods ──

func (h *ImageGenH) newTmp() (string, error) {
	dir := filepath.Join(h.Cfg.TmpDir, "img_"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func (h *ImageGenH) cleanupTmp(dir string) {
	_ = os.RemoveAll(dir)
}

func (h *ImageGenH) registerOutput(path, base string) (string, error) {
	return h.Store.Register(path, base)
}
