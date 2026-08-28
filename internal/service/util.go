package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const ScriptsDir = "scripts"

const defaultCmdTimeout = 60 * time.Second

// NewID generates a random hex token.
func NewID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// CopyFile copies src to dst.
func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// RunCmd runs a command with a default timeout and returns combined output.
func RunCmd(name string, args ...string) (string, error) {
	return RunCmdTimeout(defaultCmdTimeout, name, args...)
}

// RunCmdTimeout runs a command with a specified timeout and returns combined output.
func RunCmdTimeout(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("命令超时（%s）", timeout.String())
	}
	return string(out), err
}

// RunCmdIn runs a command in a working directory.
func RunCmdIn(dir, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultCmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("命令超时（%s）", defaultCmdTimeout.String())
	}
	return string(out), err
}

// PythonPath resolves the python3 interpreter.
func PythonPath() string {
	if p := os.Getenv("FLOWCONVERT_PYTHON"); p != "" {
		return p
	}
	return "python3"
}

// ScriptPath returns the absolute path to a script under ScriptsDir.
func ScriptPath(name string) string {
	abs, err := filepath.Abs(filepath.Join(ScriptsDir, name))
	if err != nil {
		return filepath.Join(ScriptsDir, name)
	}
	return abs
}

// SafeExt sanitizes an extension string.
func SafeExt(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	ext = strings.TrimPrefix(ext, ".")
	if ext == "" {
		return ""
	}
	for _, c := range ext {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return ""
		}
	}
	return ext
}

// ResultToken builds a downloadable filename token for an output file.
func FileToken(filename string) (string, error) {
	token := NewID(16)
	// We only accept safe names
	name := filepath.Base(filename)
	if strings.Contains(name, "..") {
		return "", fmt.Errorf("invalid filename")
	}
	return fmt.Sprintf("%s__%s", token, name), nil
}