package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// VideoParams holds parameters for video generation.
type VideoParams struct {
	Prompt   string
	Duration int
	Style    string
	Width    int
	Height   int
}

// MakeTextVideo creates a video from text prompt via Python backend.
func MakeTextVideo(tmpDir, prompt string, duration int) (string, error) {
	if duration <= 0 {
		duration = 3
	}
	if duration > 60 {
		duration = 60
	}
	dest := filepath.Join(tmpDir, "video.mp4")
	payloadPath := filepath.Join(tmpDir, "video_payload.json")
	payload := fmt.Sprintf(`{"prompt": %q, "duration": %d}`, prompt, duration)
	if err := os.WriteFile(payloadPath, []byte(payload), 0o600); err != nil {
		return "", fmt.Errorf("保存参数失败: %v", err)
	}
	defer os.Remove(payloadPath)

	out, err := RunCmd(PythonPath(), ScriptPath("video.py"), "text", payloadPath, dest)
	if err != nil {
		return "", fmt.Errorf("视频生成失败: %s", strings.TrimSpace(out))
	}
	if _, err := os.Stat(dest); err != nil {
		return "", fmt.Errorf("视频生成失败，请稍后重试")
	}
	return dest, nil
}

// MakeKeyframeVideo creates a video between two keyframes.
func MakeKeyframeVideo(tmpDir, firstFrame, lastFrame, prompt string, duration int) (string, error) {
	if duration <= 0 {
		duration = 5
	}
	dest := filepath.Join(tmpDir, "keyframe_video.mp4")
	payloadPath := filepath.Join(tmpDir, "kf_payload.json")
	payload := fmt.Sprintf(`{"first": %q, "last": %q, "prompt": %q, "duration": %d}`, firstFrame, lastFrame, prompt, duration)
	if err := os.WriteFile(payloadPath, []byte(payload), 0o600); err != nil {
		return "", fmt.Errorf("保存参数失败: %v", err)
	}
	defer os.Remove(payloadPath)

	out, err := RunCmd(PythonPath(), ScriptPath("video.py"), "keyframe", payloadPath, dest)
	if err != nil {
		return "", fmt.Errorf("视频生成失败: %s", strings.TrimSpace(out))
	}
	if _, err := os.Stat(dest); err != nil {
		return "", fmt.Errorf("视频生成失败，请稍后重试")
	}
	return dest, nil
}

// MakeRefVideo creates a video from reference images.
func MakeRefVideo(tmpDir, prompt string, refPaths []string, duration int) (string, error) {
	if duration <= 0 {
		duration = 5
	}
	dest := filepath.Join(tmpDir, "ref_video.mp4")
	payloadPath := filepath.Join(tmpDir, "ref_payload.json")
	
	refsJSON := "["
	for i, p := range refPaths {
		if i > 0 {
			refsJSON += ", "
		}
		refsJSON += fmt.Sprintf("%q", p)
	}
	refsJSON += "]"
	
	payload := fmt.Sprintf(`{"prompt": %q, "refs": %s, "duration": %d}`, prompt, refsJSON, duration)
	if err := os.WriteFile(payloadPath, []byte(payload), 0o600); err != nil {
		return "", fmt.Errorf("保存参数失败: %v", err)
	}
	defer os.Remove(payloadPath)

	out, err := RunCmd(PythonPath(), ScriptPath("video.py"), "ref", payloadPath, dest)
	if err != nil {
		return "", fmt.Errorf("视频生成失败: %s", strings.TrimSpace(out))
	}
	if _, err := os.Stat(dest); err != nil {
		return "", fmt.Errorf("视频生成失败，请稍后重试")
	}
	return dest, nil
}
