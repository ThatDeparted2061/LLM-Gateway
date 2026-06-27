// Command gateway runs the LLM gateway HTTP server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/harsh-791/git-Projects/llm-gateway/internal/cache"
	"github.com/harsh-791/git-Projects/llm-gateway/internal/config"
	"github.com/harsh-791/git-Projects/llm-gateway/internal/handlers"
	"github.com/harsh-791/git-Projects/llm-gateway/internal/providers"
	"github.com/harsh-791/git-Projects/llm-gateway/internal/ratelimit"
	"github.com/harsh-791/git-Projects/llm-gateway/internal/router"
	"github.com/harsh-791/git-Projects/llm-gateway/internal/server"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "YAML config file (optional; env vars override it)")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("invalid config", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var ps []providers.Provider
	for _, name := range cfg.ProviderOrder() {
		switch name {
		case "groq":
			ps = append(ps, providers.NewGroq(cfg.Groq.BaseURL, cfg.Groq.APIKey, cfg.Groq.Model))
		case "gemini":
			ps = append(ps, providers.NewGemini(cfg.Gemini.BaseURL, cfg.Gemini.APIKey, cfg.Gemini.Model))
		case "ollama":
			ps = append(ps, providers.NewOllama(cfg.Ollama.BaseURL, cfg.Ollama.Model))
		}
	}

	chat := &handlers.Chat{
		Router:  router.New(ps...),
		Limiter: ratelimit.New(ctx, cfg.RateLimit.TokensPerSec, cfg.RateLimit.Burst),
		Exact:   cache.NewExact(ctx, cfg.Cache.TTL),
		APIKeys: map[string]bool{},
	}
	for _, k := range cfg.APIKeys {
		if k = strings.TrimSpace(k); k != "" {
			chat.APIKeys[k] = true
		}
	}
	if cfg.Cache.Semantic {
		chat.Semantic = cache.NewSemantic(cfg.Ollama.BaseURL, cfg.Cache.EmbeddingModel, cfg.Cache.SimilarityThreshold, cfg.Cache.TTL)
	}

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           server.New(chat),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: it would cut off long SSE streams.
	}
	go func() {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()
	slog.Info("llm-gateway listening", "port", cfg.Port, "providers", cfg.ProviderOrder(),
		"semantic_cache", cfg.Cache.Semantic, "auth", len(chat.APIKeys) > 0)

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown", "err", err)
	}
}
