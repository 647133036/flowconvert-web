package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// OcrOptions mirrors the option set understood by scripts/ocr.py.
type OcrOptions struct {
	Mode      string   `json:"mode"`
	Quality   string   `json:"quality"`
	Charset   string   `json:"charset"`
	Lang      string   `json:"lang"`
	Redbox    bool     `json:"redbox"`
	Optimize  bool     `json:"optimize"`
	Translate bool     `json:"translate"`
	Exam      bool     `json:"exam"`
	Formats   []string `json:"formats"`
	Engine    string   `json:"engine"`
}

// ocrLangs are the language presets exposed by the OCR page.
var ocrLangs = map[string]bool{
	"auto": true, "zh": true, "en": true, "ja": true, "ko": true,
	"de": true, "fr": true, "es": true, "ru": true, "multi": true,
}

// OcrLayout summarizes the layout detected across the recognized pages.
type OcrLayout struct {
	Columns int `json:"columns"`
	Tables  int `json:"tables"`
	Blocks  int `json:"blocks"`
	Lines   int `json:"lines"`
}

// OcrResult holds the outcome of an OCR pass and its exported files.
type OcrResult struct {
	Text            string            `json:"text"`
	TranslatedText  string            `json:"translated_text"`
	Engine          string            `json:"engine"`
	Pages           int               `json:"pages"`
	Chars           int               `json:"chars"`
	Charset         string            `json:"charset"`
	FromTextLayer   bool              `json:"from_text_layer"`
	Layout          OcrLayout         `json:"layout"`
	Blocks          []map[string]any  `json:"blocks"`
	Lines           []map[string]any  `json:"lines"`
	FilteredHeaders []string          `json:"filtered_headers"`
	Files           map[string]string `json:"files"`
	Warnings        []string          `json:"warnings"`
	ElapsedMs       int64             `json:"elapsed_ms"`
	Options         map[string]any    `json:"options"`
}

// Normalize fills defaults and rejects unsupported option values.
func (o *OcrOptions) Normalize() error {
	if o.Mode == "" {
		o.Mode = "general"
	}
	if o.Mode != "general" && o.Mode != "advanced" {
		return fmt.Errorf("不支持的识别模式: %s", o.Mode)
	}
	if o.Quality == "" {
		o.Quality = "normal"
	}
	if o.Quality != "normal" && o.Quality != "high" {
		return fmt.Errorf("不支持的识别质量: %s", o.Quality)
	}
	if o.Charset == "" {
		o.Charset = "auto"
	}
	if o.Charset != "auto" && o.Charset != "zh_hans" && o.Charset != "zh_hant" {
		return fmt.Errorf("不支持的输出字形: %s", o.Charset)
	}
	if o.Lang == "" {
		o.Lang = "auto"
	}
	if !ocrLangs[o.Lang] {
		return fmt.Errorf("不支持的识别语言: %s", o.Lang)
	}
	if o.Engine == "" {
		o.Engine = "auto"
	}
	if o.Engine != "auto" && o.Engine != "tesseract" && o.Engine != "pp_ocr" {
		return fmt.Errorf("不支持的识别引擎: %s", o.Engine)
	}
	if len(o.Formats) == 0 {
		o.Formats = []string{"txt"}
	}
	cleaned := o.Formats[:0]
	for _, f := range o.Formats {
		switch f {
		case "txt", "docx", "xlsx", "json", "redbox":
			cleaned = append(cleaned, f)
		}
	}
	o.Formats = cleaned
	if len(o.Formats) == 0 {
		o.Formats = []string{"txt"}
	}
	return nil
}

// Timeout estimates the script deadline from quality and translation.
func (o *OcrOptions) Timeout() time.Duration {
	d := 300 * time.Second
	if o.Quality == "high" {
		d = 540 * time.Second
	}
	if o.Translate {
		d += 180 * time.Second
	}
	return d
}

// OcrFile runs the OCR script on an image or PDF and returns text plus exports.
func OcrFile(path string, opts OcrOptions, outdir string) (*OcrResult, error) {
	if err := opts.Normalize(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(opts)
	if err != nil {
		return nil, fmt.Errorf("参数组装失败")
	}
	args := []string{ScriptPath("ocr.py"), path, "--options", string(payload), "--outdir", outdir}
	out, err := RunCmdTimeout(opts.Timeout(), PythonPath(), args...)
	if err != nil {
		return nil, fmt.Errorf("OCR 识别失败: %s", strings.TrimSpace(tailOf(out, 300)))
	}

	var raw struct {
		Success         bool              `json:"success"`
		Error           string            `json:"error"`
		Text            string            `json:"text"`
		TranslatedText  string            `json:"translated_text"`
		Engine          string            `json:"engine"`
		Pages           int               `json:"pages"`
		Chars           int               `json:"chars"`
		Charset         string            `json:"charset"`
		FromTextLayer   bool              `json:"from_text_layer"`
		Layout          OcrLayout         `json:"layout"`
		Blocks          []map[string]any  `json:"blocks"`
		Lines           []map[string]any  `json:"lines"`
		FilteredHeaders []string          `json:"filtered_headers"`
		Files           map[string]string `json:"files"`
		Warnings        []string          `json:"warnings"`
		ElapsedMs       int64             `json:"elapsed_ms"`
		Options         map[string]any    `json:"options"`
	}
	if err := json.Unmarshal([]byte(extractJSON(out)), &raw); err != nil {
		return nil, fmt.Errorf("OCR 服务响应异常")
	}
	if !raw.Success {
		if raw.Error != "" {
			return nil, fmt.Errorf("OCR 识别失败: %s", raw.Error)
		}
		return nil, fmt.Errorf("OCR 识别失败")
	}
	if raw.Blocks == nil {
		raw.Blocks = []map[string]any{}
	}
	if raw.Lines == nil {
		raw.Lines = []map[string]any{}
	}
	if raw.Files == nil {
		raw.Files = map[string]string{}
	}
	if raw.Warnings == nil {
		raw.Warnings = []string{}
	}
	if raw.FilteredHeaders == nil {
		raw.FilteredHeaders = []string{}
	}
	if raw.Chars == 0 {
		raw.Chars = len([]rune(raw.Text))
	}
	if raw.Engine == "" {
		raw.Engine = "unknown"
	}
	return &OcrResult{
		Text:            raw.Text,
		TranslatedText:  raw.TranslatedText,
		Engine:          raw.Engine,
		Pages:           raw.Pages,
		Chars:           raw.Chars,
		Charset:         raw.Charset,
		FromTextLayer:   raw.FromTextLayer,
		Layout:          raw.Layout,
		Blocks:          raw.Blocks,
		Lines:           raw.Lines,
		FilteredHeaders: raw.FilteredHeaders,
		Files:           raw.Files,
		Warnings:        raw.Warnings,
		ElapsedMs:       raw.ElapsedMs,
		Options:         raw.Options,
	}, nil
}

// extractJSON returns the last standalone JSON object line found in the output,
// ignoring log lines that the script prints alongside the result.
func extractJSON(out string) string {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "{") {
			return trimmed
		}
	}
	return strings.TrimSpace(out)
}

func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
