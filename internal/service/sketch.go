package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MakeSketch converts an image to a pencil-sketch style PNG.
// sigma controls line softness (0.5 - 10).
func MakeSketch(tmpDir, src string, sigma float64) (string, error) {
	if sigma <= 0 {
		sigma = 3.0
	}
	if sigma > 10 {
		sigma = 10.0
	}
	dest := filepath.Join(tmpDir, "sketch.png")
	// Convert input into PNG first (handles BMP/WebP etc.)
	pngPath := filepath.Join(tmpDir, "sketch_input.png")
	if out, err := RunCmd(PythonPath(), ScriptPath("vectorize.py"), "topng", src, pngPath); err != nil {
		return "", fmt.Errorf("图片读取失败: %s", strings.TrimSpace(out))
	}
	sigmaStr := strconv.FormatFloat(sigma, 'f', -1, 64)
	out, err := RunCmd(PythonPath(), ScriptPath("sketch.py"), "go", pngPath, sigmaStr, dest)
	if err != nil {
		return "", fmt.Errorf("素描生成失败: %s", strings.TrimSpace(out))
	}
	if _, err := os.Stat(dest); err != nil {
		return "", fmt.Errorf("素描生成失败")
	}
	return dest, nil
}