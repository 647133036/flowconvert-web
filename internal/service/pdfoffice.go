package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PdfToOffice converts a PDF to docx/xlsx via Python scripts.
func PdfToOffice(tmpDir, src, output string) (string, error) {
	output = SafeExt(output)
	if output == "" {
		output = "docx"
	}
	if output != "docx" && output != "xlsx" {
		return "", fmt.Errorf("不支持的输出格式: %s", output)
	}

	pyPath := findScript(fmt.Sprintf("pdf2%s.py", output))
	if pyPath == "" {
		return "", fmt.Errorf("缺少转换脚本: pdf2%s.py", output)
	}

	outDir := filepath.Join(tmpDir, "office_out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	stem := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	dest := filepath.Join(outDir, stem+"."+output)

	out, err := RunCmdTimeout(5*time.Minute, PythonPath(), pyPath, src, dest)
	if err != nil {
		return "", fmt.Errorf("PDF 转换失败: %s", strings.TrimSpace(out))
	}
	if _, err := os.Stat(dest); err != nil {
		return "", fmt.Errorf("未生成输出文件")
	}
	return dest, nil
}

func findScript(name string) string {
	candidates := []string{
		filepath.Join("scripts", name),
		filepath.Join("..", "scripts", name),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}
