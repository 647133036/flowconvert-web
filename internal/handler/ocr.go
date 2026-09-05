package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"flowconvert/internal/config"
	"flowconvert/internal/service"
)

// OcrH handles OCR endpoints.
type OcrH struct {
	Cfg   *config.Config
	Store *FileStore
	Jobs  *OcrJobStore
}

var ocrExts = []string{"jpg", "jpeg", "png", "bmp", "tiff", "tif", "webp", "gif", "pdf"}

var ocrDownloadNames = map[string]string{
	"txt":    "识别结果.txt",
	"docx":   "识别结果.docx",
	"xlsx":   "识别结果.xlsx",
	"json":   "识别结果.json",
	"redbox": "红框标记图.png",
}

var ocrExportOrder = []string{"txt", "docx", "xlsx", "json", "redbox"}

func (h *OcrH) writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *OcrH) writeErr(w http.ResponseWriter, status int, msg string) {
	h.writeJSON(w, status, map[string]interface{}{"success": false, "error": msg})
}

func (h *OcrH) safeErr(w http.ResponseWriter, err error) {
	fmt.Fprintf(os.Stderr, "[OCR] error: %v\n", err)
	h.writeJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{"success": false, "error": "识别失败，请稍后重试"})
}

func (h *OcrH) newTmp() (string, error) {
	dir := filepath.Join(h.Cfg.TmpDir, "ocr_"+service.NewID(8))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func (h *OcrH) cleanupTmp(dir string) {
	_ = os.RemoveAll(dir)
}

// isMultipartForm reports whether the request body is multipart form data.
func isMultipartForm(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = ct[:i]
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "multipart/")
}

// parseOcrBool accepts the truthy spellings browsers and curl send for a checkbox.
func parseOcrBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// parseOcrOptions reads the option form fields; empty values keep their defaults
// so Normalize can validate them.
func parseOcrOptions(r *http.Request) service.OcrOptions {
	// ParseForm alone does not read multipart bodies, so the right parser has
	// to be picked based on the content type.
	if r.Form == nil {
		if isMultipartForm(r) {
			_ = r.ParseMultipartForm(32 << 20)
		} else {
			_ = r.ParseForm()
		}
	}
	form := r.Form
	formats := []string{"txt"}
	if vals := form["formats"]; len(vals) > 0 {
		formats = nil
		for _, v := range vals {
			for _, item := range strings.Split(v, ",") {
				item = strings.ToLower(strings.TrimSpace(item))
				if item != "" {
					formats = append(formats, item)
				}
			}
		}
		if len(formats) == 0 {
			formats = []string{"txt"}
		}
	}
	get := func(key string) string {
		if vs := form[key]; len(vs) > 0 {
			return strings.TrimSpace(vs[0])
		}
		return ""
	}
	return service.OcrOptions{
		Mode:      get("mode"),
		Quality:   get("quality"),
		Charset:   get("charset"),
		Lang:      get("lang"),
		Engine:    get("engine"),
		Redbox:    parseOcrBool(get("redbox")),
		Optimize:  parseOcrBool(get("optimize")),
		Translate: parseOcrBool(get("translate")),
		Exam:      parseOcrBool(get("exam")),
		Formats:   formats,
	}
}

func ocrURLMsg(err error) string {
	switch {
	case errors.Is(err, service.ErrBadURL):
		return "链接无效或不允许访问"
	case errors.Is(err, service.ErrTooLarge):
		return "链接文件超过大小限制"
	case errors.Is(err, service.ErrNotDoc), errors.Is(err, service.ErrNotImage):
		return "链接内容不是图片或 PDF"
	}
	return "文档链接读取失败"
}

// registerOcrOutputs turns script output files into stable download URLs.
func (h *OcrH) registerOcrOutputs(files map[string]string) []map[string]interface{} {
	out := []map[string]interface{}{}
	for _, key := range ocrExportOrder {
		path, ok := files[key]
		if !ok || path == "" {
			continue
		}
		url, err := h.Store.Register(path, ocrDownloadNames[key])
		if err != nil {
			fmt.Fprintf(os.Stderr, "[OCR] register %s failed: %v\n", key, err)
			continue
		}
		out = append(out, map[string]interface{}{
			"name": ocrDownloadNames[key],
			"type": key,
			"url":  url,
		})
	}
	return out
}

// HandleOcr: POST /api/ocr — accepts an uploaded image/PDF or document URL,
// starts recognition in the background, and returns {"success", "task_id"}.
// Progress is polled via GET /api/ocr/task/{id}.
func (h *OcrH) HandleOcr(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeErr(w, http.StatusMethodNotAllowed, "仅支持POST请求")
		return
	}

	tmp, err := h.newTmp()
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}

	outdir := filepath.Join(tmp, "out")
	if err := os.MkdirAll(outdir, 0o755); err != nil {
		h.cleanupTmp(tmp)
		h.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}

	var srcPath, ext string
	source := "file"
	if isMultipartForm(r) {
		srcPath, ext, err = saveUpload(r, h.Cfg, "file", ocrExts, tmp)
		if err != nil {
			// Browsers and curl often send the document URL through a
			// multipart form too, so fall back to it when no file was posted.
			urlStr := strings.TrimSpace(r.PostFormValue("url"))
			if urlStr == "" {
				h.cleanupTmp(tmp)
				h.writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			srcPath, ext, err = service.FetchFile(tmp, urlStr, h.Cfg.MaxSize, ocrExts)
			if err != nil {
				h.cleanupTmp(tmp)
				h.writeErr(w, http.StatusBadRequest, ocrURLMsg(err))
				return
			}
			source = "url"
		}
	} else {
		if err := r.ParseForm(); err != nil {
			h.cleanupTmp(tmp)
			h.writeErr(w, http.StatusBadRequest, "参数格式错误")
			return
		}
		urlStr := strings.TrimSpace(r.FormValue("url"))
		if urlStr == "" {
			h.cleanupTmp(tmp)
			h.writeErr(w, http.StatusBadRequest, "请选择图片或 PDF 文件，或填写文档链接")
			return
		}
		srcPath, ext, err = service.FetchFile(tmp, urlStr, h.Cfg.MaxSize, ocrExts)
		if err != nil {
			h.cleanupTmp(tmp)
			h.writeErr(w, http.StatusBadRequest, ocrURLMsg(err))
			return
		}
		source = "url"
	}

	opts := parseOcrOptions(r)
	if err := opts.Normalize(); err != nil {
		h.cleanupTmp(tmp)
		h.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	if h.Jobs == nil {
		h.cleanupTmp(tmp)
		h.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	job := h.Jobs.Create()
	if !h.Jobs.AcquireSlot() {
		h.Jobs.Delete(job.ID)
		h.cleanupTmp(tmp)
		h.writeErr(w, http.StatusServiceUnavailable, "服务器繁忙，请稍后重试")
		return
	}
	go func() {
		defer h.Jobs.ReleaseSlot()
		defer h.cleanupTmp(tmp)
		result, err := service.OcrFile(srcPath, opts, outdir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[OCR %s] error: %v\n", job.ID, err)
			h.Jobs.SetError(job.ID, "识别失败，请稍后重试")
			return
		}
		h.Jobs.SetComplete(job.ID, h.buildOcrPayload(result, opts, source, ext))
	}()

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"task_id": job.ID,
	})
}

func (h *OcrH) buildOcrPayload(result *service.OcrResult, opts service.OcrOptions, source, ext string) map[string]interface{} {
	text := result.Text
	return map[string]interface{}{
		"success":          true,
		"text":             text,
		"translated_text":  result.TranslatedText,
		"downloads":        h.registerOcrOutputs(result.Files),
		"engine":           result.Engine,
		"pages":            result.Pages,
		"chars":            result.Chars,
		"charset":          result.Charset,
		"from_text_layer":  result.FromTextLayer,
		"layout":           result.Layout,
		"blocks":           result.Blocks,
		"lines":            result.Lines,
		"filtered_headers": result.FilteredHeaders,
		"warnings":         result.Warnings,
		"elapsed_ms":       result.ElapsedMs,
		"options":          opts,
		"source":           source,
		"ext":              ext,
		"empty":            strings.TrimSpace(text) == "",
	}
}
