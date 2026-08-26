package config

import (
	"os"
	"path/filepath"
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
}

func Load() *Config {
	dataDir := env("FLOWCONVERT_DATA", "data")
	return &Config{
		Port:           env("FLOWCONVERT_PORT", "8080"),
		DataDir:        dataDir,
		TmpDir:         filepath.Join(dataDir, "tmp"),
		OutDir:         filepath.Join(dataDir, "output"),
		MaxSize:        50 << 20,
		MaxURL:         20 << 20,
		TTLHours:       1,
		AgnesAPIKey:    env("AGNES_API_KEY", ""),
		AgnesBaseURL:   env("AGNES_BASE_URL", "https://apihub.agnes-ai.cn/v1"),
		SenseNovaKey:   env("SENSENOVA_API_KEY", ""),
		SenseNovaBase:  env("SENSENOVA_BASE_URL", "https://token.sensenova.cn/v1"),
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
