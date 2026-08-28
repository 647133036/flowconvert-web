package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"flowconvert/internal/config"
	"flowconvert/internal/service"
)

// ConvertH includes all dependencies for conversion handlers.
type ConvertH struct {
	Cfg   *config.Config
	Store *FileStore
}

func (c *ConvertH) writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (c *ConvertH) writeErr(w http.ResponseWriter, status int, msg string) {
	c.writeJSON(w, status, map[string]interface{}{"success": false, "error": msg})
}

// safeErr returns a user-friendly generic message for 422 responses,
// logging the full error to stderr. Internal paths/command output
// should never reach the client.
func (c *ConvertH) safeErr(w http.ResponseWriter, err error) {
	fmt.Fprintf(os.Stderr, "[Convert] error: %v\n", err)
	c.writeJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{"success": false, "error": "处理失败，请稍后重试"})
}

// saveUpload saves an uploaded file into a tmp dir, validating size and type
// based on the expected format list.
func (c *ConvertH) saveUpload(r *http.Request, field string, allowExts []string) (string, string, error) {
	if err := r.ParseMultipartForm(c.Cfg.MaxSize + 1<<20); err != nil {
		return "", "", fmt.Errorf("上传文件过大或参数错误")
	}
	file, header, err := r.FormFile(field)
	if err != nil {
		return "", "", fmt.Errorf("请选择要上传的文件")
	}
	defer file.Close()
	if header.Size > c.Cfg.MaxSize {
		return "", "", fmt.Errorf("文件超过 50MB 限制")
	}

	fileData, err := io.ReadAll(io.LimitReader(file, c.Cfg.MaxSize+1))
	if err != nil || int64(len(fileData)) > c.Cfg.MaxSize {
		return "", "", fmt.Errorf("文件读取失败或超过 50MB 限制")
	}
	if len(fileData) < 4 {
		return "", "", fmt.Errorf("文件内容无效")
	}

	ctype := http.DetectContentType(fileData[:min(512, len(fileData))])
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(header.Filename)), ".")
	if !allowedType(ext, ctype, allowExts) {
		return "", "", fmt.Errorf("不支持的文件类型: .%s", ext)
	}

	tmpName := fmt.Sprintf("up_%s.%s", strconv.FormatInt(time.Now().UnixNano(), 10), ext)
	tmpPath := filepath.Join(c.Cfg.TmpDir, tmpName)
	if err := os.WriteFile(tmpPath, fileData, 0o644); err != nil {
		return "", "", fmt.Errorf("服务器错误，请重试")
	}
	return tmpPath, ext, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// allowedType accepts an upload only when BOTH the extension is in the
// allow list AND the sniffed content matches that extension's registered
// MIME type. Every currently allowed upload format (images, pdf) has a
// sniffable signature, so a payload with mismatched content is rejected.
func allowedType(ext, ctype string, allow []string) bool {
	inList := false
	for _, a := range allow {
		if ext == a {
			inList = true
			break
		}
	}
	if !inList {
		return false
	}
	m := mime.TypeByExtension("." + ext)
	if m == "" {
		// No registered MIME for this extension; extension membership is all we can verify.
		return true
	}
	base := strings.Split(m, ";")[0]
	return strings.HasPrefix(ctype, base)
}

// newTmp creates a per-request working directory.
func (c *ConvertH) newTmp() (string, error) {
	dir := filepath.Join(c.Cfg.TmpDir, "w_"+service.NewID(8))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func (c *ConvertH) cleanupTmp(dir string) {
	_ = os.RemoveAll(dir)
}

func (c *ConvertH) registerOutput(path, base string) (string, error) {
	return c.Store.Register(path, base)
}

// ── Image → Vector ──

var imageExts = []string{"jpg", "jpeg", "png", "bmp", "tiff", "tif", "webp", "gif"}

func (c *ConvertH) HandleUploadVectorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.writeErr(w, http.StatusMethodNotAllowed, "仅支持POST请求")
		return
	}
	src, ext, err := c.saveUpload(r, "file", imageExts)
	if err != nil {
		c.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	defer os.Remove(src)

	tmp, err := c.newTmp()
	if err != nil {
		c.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	defer c.cleanupTmp(tmp)
	_ = ext

	params := parseVecParams(r)
	output := r.FormValue("output")
	if output == "" {
		output = "svg"
	} else if v := validOutput(output, "vector"); v == "" {
		c.writeErr(w, http.StatusBadRequest, "不支持的输出格式")
		return
	} else {
		output = v
	}

	dest, err := service.Vectorize(tmp, src, output, params)
	if err != nil {
		c.safeErr(w, err)
		return
	}
	dl, err := c.registerOutput(dest, "converted."+output)
	if err != nil {
		c.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	c.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":      true,
		"download_url": dl,
		"format":       output,
	})
}

func parseVecParams(r *http.Request) service.VecParams {
	ci, _ := strconv.Atoi(r.FormValue("color_precision"))
	fs, _ := strconv.Atoi(r.FormValue("filter_speckle"))
	ct, _ := strconv.Atoi(r.FormValue("corner_threshold"))
	return service.VecParams{
		Mode:            r.FormValue("mode"),
		ColorPrecision:  ci,
		FilterSpeckle:   fs,
		CornerThreshold: ct,
	}
}

func (c *ConvertH) HandleURLVectorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		c.writeErr(w, http.StatusMethodNotAllowed, "仅支持GET/POST请求")
		return
	}
	url := r.URL.Query().Get("url")
	output := r.URL.Query().Get("output")
	if output == "" {
		output = "svg"
	} else if v := validOutput(output, "vector"); v == "" {
		c.writeErr(w, http.StatusBadRequest, "不支持的输出格式")
		return
	} else {
		output = v
	}
	params := service.VecParams{
		Mode:            r.URL.Query().Get("mode"),
		ColorPrecision:  parseIntDefault(r.URL.Query().Get("color_precision"), 6),
		FilterSpeckle:   parseIntDefault(r.URL.Query().Get("filter_speckle"), 4),
		CornerThreshold: parseIntDefault(r.URL.Query().Get("corner_threshold"), 60),
	}

	tmp, err := c.newTmp()
	if err != nil {
		c.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	defer c.cleanupTmp(tmp)

	src, err := service.FetchImage(tmp, url, c.Cfg.MaxURL)
	if err != nil {
		c.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	defer os.Remove(src)

	dest, err := service.Vectorize(tmp, src, output, params)
	if err != nil {
		c.safeErr(w, err)
		return
	}
	dl, err := c.registerOutput(dest, "converted."+output)
	if err != nil {
		c.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	c.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":      true,
		"download_url": dl,
		"format":       output,
	})
}

func parseIntDefault(v string, def int) int {
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return i
}

// ── PDF → Office ──

func (c *ConvertH) HandlePdfToOffice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.writeErr(w, http.StatusMethodNotAllowed, "仅支持POST请求")
		return
	}
	src, ext, err := c.saveUpload(r, "file", []string{"pdf"})
	if err != nil {
		c.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	defer os.Remove(src)
	_ = ext

	tmp, err := c.newTmp()
	if err != nil {
		c.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	defer c.cleanupTmp(tmp)

	output := r.FormValue("output")
	if output == "" {
		output = "docx"
	} else if v := validOutput(output, "pdf"); v == "" {
		c.writeErr(w, http.StatusBadRequest, "不支持的输出格式")
		return
	} else {
		output = v
	}
	dest, err := service.PdfToOffice(tmp, src, output)
	if err != nil {
		c.safeErr(w, err)
		return
	}
	dl, err := c.registerOutput(dest, "converted."+output)
	if err != nil {
		c.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	c.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":      true,
		"download_url": dl,
		"format":       output,
	})
}

// ── Sketch ──

func (c *ConvertH) HandleSketch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.writeErr(w, http.StatusMethodNotAllowed, "仅支持POST请求")
		return
	}
	src, ext, err := c.saveUpload(r, "file", imageExts)
	if err != nil {
		c.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	defer os.Remove(src)
	_ = ext

	tmp, err := c.newTmp()
	if err != nil {
		c.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	defer c.cleanupTmp(tmp)

	sigma, _ := strconv.ParseFloat(r.FormValue("sigma"), 64)
	if sigma <= 0 || sigma > 10 {
		sigma = 3.0
	}
	dest, err := service.MakeSketch(tmp, src, sigma)
	if err != nil {
		c.safeErr(w, err)
		return
	}
	dl, err := c.registerOutput(dest, "sketch.png")
	if err != nil {
		c.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	c.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":      true,
		"download_url": dl,
		"format":       "png",
	})
}

// ── ID Photo ──

func (c *ConvertH) HandleIdPhoto(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.writeErr(w, http.StatusMethodNotAllowed, "仅支持POST请求")
		return
	}
	src, ext, err := c.saveUpload(r, "file", imageExts)
	if err != nil {
		c.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	defer os.Remove(src)
	_ = ext

	size := r.FormValue("size")
	bg := r.FormValue("bg_color")

	tmp, err := c.newTmp()
	if err != nil {
		c.writeErr(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	defer c.cleanupTmp(tmp)

	dest, err := service.MakeIdPhoto(tmp, src, size, bg)
	if err != nil {
		c.safeErr(w, err)
		return
	}
	defer os.Remove(dest)

	// Serve the image directly
	data, err := os.ReadFile(dest)
	if err != nil {
		c.writeErr(w, http.StatusInternalServerError, "读取结果文件失败")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Disposition", `attachment; filename="证件照.png"`)
	_, _ = w.Write(data)
}

// ── Formats info ──

func (c *ConvertH) HandleFormats(w http.ResponseWriter, r *http.Request) {
	c.writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":        true,
		"image_input":    []string{"jpg", "jpeg", "png", "bmp", "tiff", "webp", "gif"},
		"vector_output":  []string{"svg", "ai", "dxf", "eps", "fig", "sk", "pdf"},
		"pdf_output":     []string{"docx", "xlsx"},
		"max_upload_mb":  50,
		"max_url_mb":     20,
	})
}

var allowedVectorOutputs = map[string]bool{"svg": true, "ai": true, "dxf": true, "eps": true, "fig": true, "sk": true, "pdf": true}
var allowedPDFOutputs = map[string]bool{"docx": true, "xlsx": true}

func validOutput(format, kind string) string {
	switch kind {
	case "vector":
		if allowedVectorOutputs[format] {
			return format
		}
	case "pdf":
		if allowedPDFOutputs[format] {
			return format
		}
	}
	return ""
}

const maxPromptLen = 2000

func validPrompt(p string) (string, error) {
	if len(p) > maxPromptLen {
		return "", fmt.Errorf("提示词长度不能超过%d个字符", maxPromptLen)
	}
	return p, nil
}