package service

import (
	"os"
	"testing"
)

func TestTranslateText(t *testing.T) {
	if _, err := os.Stat(ScriptPath("translate.py")); err != nil {
		t.Skipf("translate scripts not available, skipping: %v", err)
	}
	result, err := TranslateText("hello", "en", "zh")
	if err != nil {
		t.Skipf("translate service unavailable: %v", err)
	}
	if result.Text == "" {
		t.Error("translated text is empty")
	}
	t.Logf("translation: %s -> %s (engine: %s)", "hello", result.Text, result.Engine)
}

func TestTranslateTextEmptyInput(t *testing.T) {
	if _, err := os.Stat(ScriptPath("translate.py")); err != nil {
		t.Skipf("translate scripts not available, skipping: %v", err)
	}
	_, err := TranslateText("", "auto", "zh")
	if err == nil {
		t.Error("expected error for empty input")
	}
}
