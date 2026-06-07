// Package config loads gateway settings from an optional YAML file, then applies
// environment-variable overrides on top.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the full gateway configuration.
type Config struct {
	Port            int    `yaml:"port"`
	PrimaryProvider string `yaml:"primary_provider"` // groq | gemini | ollama
	// APIKeys are the client keys allowed to call the gateway. Empty means open access.
	APIKeys []string `yaml:"api_keys"`

	Groq   ProviderConfig `yaml:"groq"`
	Gemini ProviderConfig `yaml:"gemini"`
	Ollama ProviderConfig `yaml:"ollama"`

	Cache     CacheConfig     `yaml:"cache"`
	RateLimit RateLimitConfig `yaml:"rate_limit"`
}

// ProviderConfig holds one upstream's settings. Model is the default used when a
// request doesn't name one (and always on fallback, since model names are provider-specific).
type ProviderConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

type CacheConfig struct {
	TTL                 time.Duration `yaml:"ttl"`
	Semantic            bool          `yaml:"semantic"`
	SimilarityThreshold float64       `yaml:"similarity_threshold"`
	EmbeddingModel      string        `yaml:"embedding_model"`
}

// RateLimitConfig is a per-API-key token bucket: each request costs one token.
type RateLimitConfig struct {
	TokensPerSec float64 `yaml:"tokens_per_sec"`
	Burst        int     `yaml:"burst"`
}

// Default returns the configuration used for anything the YAML file and env leave unset.
func Default() *Config {
	return &Config{
		Port:            8080,
		PrimaryProvider: "ollama", // works with zero API keys
		Groq:            ProviderConfig{BaseURL: "https://api.groq.com/openai/v1", Model: "llama-3.1-8b-instant"},
		Gemini:          ProviderConfig{BaseURL: "https://generativelanguage.googleapis.com/v1beta", Model: "gemini-2.5-flash"},
		Ollama:          ProviderConfig{BaseURL: "http://localhost:11434", Model: "llama3.2"},
		Cache: CacheConfig{
			TTL:                 time.Hour,
			Semantic:            true,
			SimilarityThreshold: 0.95,
			EmbeddingModel:      "nomic-embed-text",
		},
		RateLimit: RateLimitConfig{TokensPerSec: 5, Burst: 10},
	}
}

// Load reads path (a missing file is fine: env-only config, e.g. in Docker),
// applies env overrides, and validates the result.
func Load(path string) (*Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if err := cfg.applyEnv(); err != nil {
		return nil, err
	}
	return cfg, cfg.validate()
}

func (c *Config) applyEnv() error {
	str := func(dst *string, key string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	str(&c.PrimaryProvider, "PRIMARY_PROVIDER")
	str(&c.Groq.APIKey, "GROQ_API_KEY")
	str(&c.Groq.Model, "GROQ_MODEL")
	str(&c.Gemini.APIKey, "GEMINI_API_KEY")
	str(&c.Gemini.Model, "GEMINI_MODEL")
	str(&c.Ollama.BaseURL, "OLLAMA_BASE_URL")
	str(&c.Ollama.Model, "OLLAMA_MODEL")
	str(&c.Cache.EmbeddingModel, "EMBEDDING_MODEL")
	if v := os.Getenv("GATEWAY_API_KEYS"); v != "" {
		c.APIKeys = strings.Split(v, ",")
	}

	var errs []error
	parse := func(key string, set func(string) error) {
		if v := os.Getenv(key); v != "" {
			if err := set(v); err != nil {
				errs = append(errs, fmt.Errorf("env %s: %w", key, err))
			}
		}
	}
	parse("GATEWAY_PORT", func(v string) (err error) { c.Port, err = strconv.Atoi(v); return })
	parse("CACHE_TTL", func(v string) (err error) { c.Cache.TTL, err = time.ParseDuration(v); return })
	parse("SEMANTIC_CACHE", func(v string) (err error) { c.Cache.Semantic, err = strconv.ParseBool(v); return })
	parse("SIMILARITY_THRESHOLD", func(v string) (err error) {
		c.Cache.SimilarityThreshold, err = strconv.ParseFloat(v, 64)
		return
	})
	parse("RATE_LIMIT_TPS", func(v string) (err error) { c.RateLimit.TokensPerSec, err = strconv.ParseFloat(v, 64); return })
	parse("RATE_LIMIT_BURST", func(v string) (err error) { c.RateLimit.Burst, err = strconv.Atoi(v); return })
	return errors.Join(errs...)
}

func (c *Config) validate() error {
	var errs []error
	if c.Port < 1 || c.Port > 65535 {
		errs = append(errs, fmt.Errorf("port %d out of range", c.Port))
	}
	if !c.Enabled(c.PrimaryProvider) {
		errs = append(errs, fmt.Errorf("primary_provider %q is unknown or not configured (groq/gemini need an api_key, ollama a base_url)", c.PrimaryProvider))
	}
	if c.Cache.TTL <= 0 {
		errs = append(errs, errors.New("cache.ttl must be > 0"))
	}
	if t := c.Cache.SimilarityThreshold; t <= 0 || t > 1 {
		errs = append(errs, fmt.Errorf("cache.similarity_threshold %v must be in (0, 1]", t))
	}
	if c.RateLimit.TokensPerSec <= 0 || c.RateLimit.Burst < 1 {
		errs = append(errs, errors.New("rate_limit.tokens_per_sec must be > 0 and burst >= 1"))
	}
	return errors.Join(errs...)
}

// Enabled reports whether a provider has enough configuration to be called.
func (c *Config) Enabled(name string) bool {
	switch name {
	case "groq":
		return c.Groq.APIKey != ""
	case "gemini":
		return c.Gemini.APIKey != ""
	case "ollama":
		return c.Ollama.BaseURL != ""
	}
	return false
}

// ProviderOrder is the failover order: the primary first, then every other
// configured provider.
func (c *Config) ProviderOrder() []string {
	order := []string{c.PrimaryProvider}
	for _, name := range []string{"groq", "gemini", "ollama"} {
		if name != c.PrimaryProvider && c.Enabled(name) {
			order = append(order, name)
		}
	}
	return order
}
