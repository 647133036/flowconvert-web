package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TranslateResult is the outcome of a text translation.
type TranslateResult struct {
	Text     string `json:"text"`
	Detected string `json:"detected"`
	Engine   string `json:"engine"`
}

// TranslateText translates plain text via the Python backend.
func TranslateText(text, source, target string) (*TranslateResult, error) {
	payloadFile := filepath.Join(os.TempDir(), "fc_payload_"+NewID(8)+".json")
	payload := map[string]string{"text": text}
	b, _ := json.Marshal(payload)
	if err := os.WriteFile(payloadFile, b, 0o600); err != nil {
		return nil, err
	}
	defer os.Remove(payloadFile)

	out, err := RunCmd(PythonPath(), ScriptPath("translate.py"), "text", source, target, payloadFile)
	if err != nil {
		return nil, fmt.Errorf("翻译失败: %s", strings.TrimSpace(out))
	}
	var res struct {
		Text     string `json:"text"`
		Detected string `json:"detected"`
		Engine   string `json:"engine"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		return nil, fmt.Errorf("翻译服务响应异常")
	}
	return &TranslateResult{Text: res.Text, Detected: res.Detected, Engine: res.Engine}, nil
}

// TranslateFile translates a document and returns the output file path.
func TranslateFile(tmpDir, src, source, target string) (string, error) {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(src)), ".")
	dest := filepath.Join(tmpDir, "translated."+ext)
	out, err := RunCmd(PythonPath(), ScriptPath("translate.py"), "file", src, dest, source, target)
	if err != nil {
		return "", fmt.Errorf("文档翻译失败: %s", strings.TrimSpace(out))
	}
	var res struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		// fall back: try dest directly
		if _, statErr := os.Stat(dest); statErr == nil {
			return dest, nil
		}
		return "", fmt.Errorf("文档翻译响应异常")
	}
	if res.Output == "" {
		return "", fmt.Errorf("文档翻译未生成输出文件")
	}
	// The python returns absolute path which may be relative to cwd; resolve
	if !filepath.IsAbs(res.Output) {
		if abs, err := filepath.Abs(res.Output); err == nil {
			res.Output = abs
		}
	}
	if _, err := os.Stat(res.Output); err != nil {
		return "", fmt.Errorf("文档翻译未生成输出文件")
	}
	return res.Output, nil
}