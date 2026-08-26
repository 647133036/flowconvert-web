package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"flowconvert/internal/config"
	"flowconvert/internal/service"
)

// TranslateH handles translation endpoints.
type TranslateH struct {
	Cfg   *config.Config
	Store *FileStore
}

var supportedLangs = map[string]bool{
	"zh": true, "en": true, "ja": true, "ko": true, "fr": true, "de": true,
	"es": true, "pt": true, "ru": true, "ar": true, "th": true, "vi": true,
	"it": true, "nl": true, "pl": true, "tr": true,
}

func validLang(v string) bool {
	return v == "auto" || supportedLangs[v]
}

func (h *TranslateH) writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// HandleTranslate: POST /api/translate
func (h *TranslateH) HandleTranslate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text   string `json:"text"`
		Source string `json:"source"`
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "请求参数错误"})
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		h.writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "请输入要翻译的文字"})
		return
	}
	if len([]rune(req.Text)) > 5000 {
		h.writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "文字过长（最多5000字符）"})
		return
	}
	if !validLang(req.Source) {
		req.Source = "auto"
	}
	if !validLang(req.Target) || req.Target == "auto" {
		req.Target = "zh"
	}

	res, err := service.TranslateText(req.Text, req.Source, req.Target)
	if err != nil {
		h.writeJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":          true,
		"translated_text":  res.Text,
		"detected_language": res.Detected,
		"engine":           res.Engine,
	})
}

// HandleTranslateFile: POST /api/translate/file
func (h *TranslateH) HandleTranslateFile(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(h.Cfg.MaxSize + 1<<20); err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "文件过大或参数错误"})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "请选择要翻译的文件"})
		return
	}
	defer file.Close()
	if header.Size > h.Cfg.MaxSize {
		h.writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "文件超过 50MB 限制"})
		return
	}

	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(header.Filename)), ".")
	okExt := map[string]bool{"txt": true, "pdf": true, "docx": true, "html": true, "htm": true, "xlsx": true, "pptx": true}
	if !okExt[ext] {
		h.writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "不支持的文件格式: ." + ext})
		return
	}

	source := r.FormValue("source")
	target := r.FormValue("target")
	if !validLang(source) {
		source = "auto"
	}
	if !validLang(target) || target == "auto" {
		target = "zh"
	}

	tmpName := fmt.Sprintf("doc_%s.%s", service.NewID(6), ext)
	tmpPath := filepath.Join(h.Cfg.TmpDir, tmpName)
	f, err := os.Create(tmpPath)
	if err != nil {
		h.writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"success": false, "error": "服务器错误"})
		return
	}
	if _, err := io.Copy(f, file); err != nil {
		f.Close()
		_ = os.Remove(tmpPath)
		h.writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"success": false, "error": "文件保存失败"})
		return
	}
	f.Close()
	defer os.Remove(tmpPath)

	outDir := filepath.Join(h.Cfg.TmpDir, "trans_"+service.NewID(6))
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		h.writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"success": false, "error": "服务器错误"})
		return
	}
	defer os.RemoveAll(outDir)

	output, err := service.TranslateFile(outDir, tmpPath, source, target)
	if err != nil {
		h.writeJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	outExt := strings.TrimPrefix(filepath.Ext(output), ".")
	outName := strings.TrimSuffix(header.Filename, filepath.Ext(header.Filename)) + "_翻译." + outExt
	dl := h.Store.Register(output, outName)
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":       true,
		"download_url":  dl,
		"original_name": header.Filename,
		"output_name":   outName,
	})
}