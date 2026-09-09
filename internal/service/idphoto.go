package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// idphotoTimeout gives the rembg model load + matting room to finish.
// Measured matting time varies 2s (warm) to 60s+ (cold model under memory pressure),
// so the shared 60s default can kill a valid request mid-flight.
const idphotoTimeout = 180 * time.Second

// UserFacingError carries a message that is safe to return to the client.
// The Python pipeline emits these for expected user mistakes (e.g. no face
// detected) as {"error": "..."} with exit code 0.
type UserFacingError struct{ Msg string }

func (e *UserFacingError) Error() string { return e.Msg }

// MakeIdPhoto generates an ID photo from a source image. hiRes=true outputs at
// 600DPI (4x pixels) for online upload / high-quality printing.
func MakeIdPhoto(tmpDir, src, size, bgColor string, hiRes bool) (string, error) {
	hiresArg := "0"
	if hiRes {
		hiresArg = "1"
	}
	dest := filepath.Join(tmpDir, "idphoto.jpg")
	out, err := RunCmdTimeout(idphotoTimeout, PythonPath(), ScriptPath("idphoto.py"), src, dest, size, bgColor, hiresArg)
	if err != nil {
		if msg := pythonErrorMsg(out); msg != "" {
			return "", &UserFacingError{Msg: msg}
		}
		return "", fmt.Errorf("证件照生成失败: %s", strings.TrimSpace(out))
	}
	if _, statErr := os.Stat(dest); statErr != nil {
		// Exit code 0 but no output file: the pipeline reported a user-facing reason.
		if msg := pythonErrorMsg(out); msg != "" {
			return "", &UserFacingError{Msg: msg}
		}
		return "", &UserFacingError{Msg: "未检测到清晰人脸，请换一张正面照重试"}
	}
	return dest, nil
}

// pythonErrorMsg extracts the "error" field from the script's JSON output.
func pythonErrorMsg(out string) string {
	var res struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		return ""
	}
	return strings.TrimSpace(res.Error)
}
