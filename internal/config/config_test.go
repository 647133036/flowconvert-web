package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg := Load()
	if cfg.Port == "" {
		t.Error("Port should have a default")
	}
	if cfg.DataDir == "" {
		t.Error("DataDir should have a default")
	}
	if cfg.TmpDir == "" {
		t.Error("TmpDir should have a default")
	}
	if cfg.OutDir == "" {
		t.Error("OutDir should have a default")
	}
	if cfg.MaxSize <= 0 {
		t.Error("MaxSize should be positive")
	}
	if cfg.MaxURL <= 0 {
		t.Error("MaxURL should be positive")
	}
}

func TestEnsureDirs(t *testing.T) {
	cfg := &Config{
		DataDir:  filepath.Join(t.TempDir(), "data"),
	}
	cfg.TmpDir = filepath.Join(cfg.DataDir, "tmp")
	cfg.OutDir = filepath.Join(cfg.DataDir, "output")

	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs failed: %v", err)
	}

	for _, d := range []string{cfg.DataDir, cfg.TmpDir, cfg.OutDir} {
		info, err := os.Stat(d)
		if err != nil {
			t.Errorf("directory %s not created: %v", d, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", d)
		}
	}
}

func TestEnsureDirsIdempotent(t *testing.T) {
	cfg := &Config{
		DataDir: filepath.Join(t.TempDir(), "data"),
	}
	cfg.TmpDir = filepath.Join(cfg.DataDir, "tmp")
	cfg.OutDir = filepath.Join(cfg.DataDir, "output")

	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("first EnsureDirs failed: %v", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("second EnsureDirs failed: %v", err)
	}
}

func TestEnvHelper(t *testing.T) {
	os.Setenv("TEST_FC_PORT", "9999")
	defer os.Unsetenv("TEST_FC_PORT")

	got := env("TEST_FC_PORT", "8080")
	if got != "9999" {
		t.Errorf("env() = %s, want 9999", got)
	}

	os.Unsetenv("TEST_FC_PORT")
	got = env("TEST_FC_PORT", "8080")
	if got != "8080" {
		t.Errorf("env() with default = %s, want 8080", got)
	}
}

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	if err := os.WriteFile(envFile, []byte("TEST_DOTENV_KEY=hello\n# comment\n\nTEST_DOTENV_NUM=42\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	os.Chdir(dir)

	defer os.Unsetenv("TEST_DOTENV_KEY")
	defer os.Unsetenv("TEST_DOTENV_NUM")

	loadDotEnv()

	if v := os.Getenv("TEST_DOTENV_KEY"); v != "hello" {
		t.Errorf("TEST_DOTENV_KEY = %q, want 'hello'", v)
	}
	if v := os.Getenv("TEST_DOTENV_NUM"); v != "42" {
		t.Errorf("TEST_DOTENV_NUM = %q, want '42'", v)
	}
}

func TestLoadDotEnvNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	os.WriteFile(envFile, []byte("TEST_NO_OVERWRITE=fromfile\n"), 0o644)

	os.Setenv("TEST_NO_OVERWRITE", "fromenv")
	defer os.Unsetenv("TEST_NO_OVERWRITE")

	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	os.Chdir(dir)

	loadDotEnv()

	if v := os.Getenv("TEST_NO_OVERWRITE"); v != "fromenv" {
		t.Errorf("existing env should not be overwritten: got %q, want 'fromenv'", v)
	}
}
