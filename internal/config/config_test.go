package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestLoadYAMLThenEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := "primary_provider: groq\ngroq:\n  api_key: from-yaml\ncache:\n  ttl: 5m\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROQ_API_KEY", "from-env")
	t.Setenv("GEMINI_API_KEY", "g")
	t.Setenv("SIMILARITY_THRESHOLD", "0.9")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Groq.APIKey != "from-env" || cfg.Cache.TTL != 5*time.Minute || cfg.Cache.SimilarityThreshold != 0.9 {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
	if cfg.Groq.Model != "llama-3.1-8b-instant" {
		t.Fatalf("default model lost: %q", cfg.Groq.Model)
	}
	if got := cfg.ProviderOrder(); !slices.Equal(got, []string{"groq", "gemini", "ollama"}) {
		t.Fatalf("order = %v", got)
	}
}

func TestLoadRejectsUnconfiguredPrimary(t *testing.T) {
	t.Setenv("PRIMARY_PROVIDER", "gemini")
	t.Setenv("GEMINI_API_KEY", "") // empty = unset
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected error for primary provider without api key")
	}
}
