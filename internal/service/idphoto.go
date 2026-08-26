package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MakeIdPhoto generates an ID photo from a source image.
func MakeIdPhoto(tmpDir, src, size, bgColor string) (string, error) {
	dest := filepath.Join(tmpDir, "idphoto.png")
	out, err := RunCmd(
		PythonPath(), ScriptPath("idphoto.py"),
		src, dest, size, bgColor,
	)
	if err != nil {
		return "", fmt.Errorf("证件照生成失败: %s", strings.TrimSpace(out))
	}
	if _, err := os.Stat(dest); err != nil {
		return "", fmt.Errorf("证件照生成失败，请换一张清晰的正面照重试")
	}
	return dest, nil
}