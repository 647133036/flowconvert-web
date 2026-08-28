package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Port          string
	DataDir       string
	TmpDir        string
	OutDir        string
	MaxSize       int64
	MaxURL        int64
	TTLHours      int
	AgnesAPIKey   string
	AgnesBaseURL  string
	SenseNovaKey  string
	SenseNovaBase string
	BaseURL       string
}

func Load() *Config {
	loadDotEnv()
	dataDir := env("FLOWCONVERT_DATA", "data")
	return &Config{
		Port:          env("FLOWCONVERT_PORT", "8080"),
		BaseURL:       env("FLOWCONVERT_BASE_URL", "http://localhost:8080"),
		DataDir:       dataDir,
		TmpDir:        filepath.Join(dataDir, "tmp"),
		OutDir:        filepath.Join(dataDir, "output"),
		MaxSize:       50 << 20,
		MaxURL:        20 << 20,
		TTLHours:      1,
		AgnesAPIKey:   env("AGNES_API_KEY", ""),
		AgnesBaseURL:  env("AGNES_BASE_URL", "https://apihub.agnes-ai.cn/v1"),
		SenseNovaKey:  env("SENSENOVA_API_KEY", ""),
		SenseNovaBase: env("SENSENOVA_BASE_URL", "https://token.sensenova.cn/v1"),
	}
}

// loadDotEnv reads a .env file from the working directory and sets
// environment variables for keys that are not already set.
func loadDotEnv() {
	f, err := os.Open(".env")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, `"'`)
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}

func (c *Config) EnsureDirs() error {
	for _, d := range []string{c.DataDir, c.TmpDir, c.OutDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
