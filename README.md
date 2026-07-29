# LLM Gateway

[![CI/CD](https://github.com/ThatDeparted2061/LLM-Gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/ThatDeparted2061/LLM-Gateway/actions/workflows/ci.yml)

An OpenAI-compatible gateway in Go that sits in front of **Groq**, **Google Gemini** and a local **Ollama**. It gives clients one endpoint, `POST /v1/chat/completions`, and handles the rest:

- **Rate limiting.** A token bucket per API key.
- **Two-tier caching.** An exact-match SHA-256 cache, plus a semantic cache that uses Ollama embeddings and cosine similarity.
- **Routing.** Failover across providers, with exponential-backoff retries on 429/5xx.
- **Streaming.** SSE passthrough. A completed stream is cached and replayed on later hits.
- **Observability.** Prometheus metrics and a per-provider health check.

## Architecture

```
                         ┌───────────────────────────── llm-gateway ─────────────────────────────┐
                         │                                                                       │
  client ── POST ───────►│  auth (optional allowlist)                                            │
  /v1/chat/completions   │      │                                                                │
  Authorization: Bearer  │      ▼                                                                │
                         │  token bucket ── empty ──► 429 + Retry-After                          │
                         │  (per API key)                                                        │
                         │      │                                                                │
                         │      ▼                          hit                                   │
                         │  exact cache ───────────────────────────────┐                        │
                         │  SHA256(model+messages), TTL                │                        │
                         │      │ miss                                 │                        │
                         │      ▼                          hit         ▼                        │
                         │  semantic cache ─────────────────────► respond (JSON, or SSE replay) │
                         │  embed prompt (Ollama /api/embed),          ▲   X-Cache: exact|semantic|miss
                         │  cosine ≥ 0.95                              │                        │
                         │      │ miss                                 │                        │
                         │      ▼                                      │                        │
                         │  router ── primary ─► fallback ─► fallback ─┤  store in both caches  │
                         │  retry 429/5xx with exp. backoff + jitter   │                        │
                         └──────┬──────────────┬──────────────┬────────┴────────────────────────┘
                                ▼              ▼              ▼
                             Groq API      Gemini API      Ollama
                          (OpenAI-compat)  (mapped to/from  (/v1, OpenAI-compat;
                                            OpenAI format)   also serves embeddings)

  GET /metrics ──► Prometheus  (llm_requests_total, llm_cache_hits_total,
                                llm_request_duration_seconds, llm_tokens_used_total)
  GET /health  ──► pings every provider concurrently
```

| Package | Responsibility |
|---|---|
| `cmd/gateway` | Entry point: load config, build providers in failover order, start server, graceful shutdown |
| `internal/config` | YAML + env var config, validation, provider order |
| `internal/providers` | `Provider` interface; Groq and Ollama via a shared OpenAI-compatible client; Gemini with request/response mapping; SSE parsing |
| `internal/router` | Ordered failover, exponential backoff on 429/5xx |
| `internal/cache` | `exact.go` (sync.Map + TTL janitor), `semantic.go` (Ollama embeddings + cosine search) |
| `internal/ratelimit` | Per-key token bucket with a refill goroutine |
| `internal/metrics` | Prometheus collectors |
| `internal/handlers` | Chat completions (JSON + SSE) and health endpoints |
| `internal/server` | chi routes and middleware |

## Quick start

### Docker Hub image

CI publishes a multi-arch (amd64/arm64) image on every push to `main`. This is the smallest way to run it, with cloud providers only:

```bash
docker run -p 8080:8080 \
  -e PRIMARY_PROVIDER=groq -e GROQ_API_KEY=gsk_... -e GEMINI_API_KEY=AIza... \
  -e SEMANTIC_CACHE=false -e GATEWAY_API_KEYS=change-me \
  <dockerhub-user>/llm-gateway:latest
```

### Docker Compose (gateway + Ollama + Prometheus)

```bash
git clone https://github.com/ThatDeparted2061/LLM-Gateway.git && cd LLM-Gateway
docker compose up --build
```

With no API keys this runs entirely on Ollama. On the first run a one-shot `ollama-models` job pulls `llama3.2` and `nomic-embed-text`. To put a cloud provider first, create a `.env` file next to `docker-compose.yml` (git-ignored):

```bash
PRIMARY_PROVIDER=groq
GROQ_API_KEY=gsk_...
GEMINI_API_KEY=AIza...
```

The gateway listens on `:8080`, Ollama on `:11434` and Prometheus on `:9090`.

### Local

```bash
ollama pull llama3.2 && ollama pull nomic-embed-text   # needs a running Ollama
cp config.example.yaml config.yaml                     # optional; env vars work too
go run ./cmd/gateway -config config.yaml
```

## Configuration

Settings are read from `config.yaml` (see [`config.example.yaml`](config.example.yaml)), and environment variables override them.

| Env var | Default | Meaning |
|---|---|---|
| `GATEWAY_PORT` | `8080` | Listen port |
| `PRIMARY_PROVIDER` | `ollama` | `groq`, `gemini` or `ollama`. Other configured providers become fallbacks, in the order groq → gemini → ollama |
| `GATEWAY_API_KEYS` | *(empty)* | Comma-separated client keys. If set, any other key gets a 401 |
| `GROQ_API_KEY` / `GROQ_MODEL` | — / `llama-3.1-8b-instant` | Groq is enabled when the key is set |
| `GEMINI_API_KEY` / `GEMINI_MODEL` | — / `gemini-2.5-flash` | Gemini is enabled when the key is set |
| `OLLAMA_BASE_URL` / `OLLAMA_MODEL` | `http://localhost:11434` / `llama3.2` | Ollama chat model; this server also handles embeddings |
| `CACHE_TTL` | `1h` | TTL for exact and semantic entries |
| `SEMANTIC_CACHE` | `true` | Turns the semantic cache on or off |
| `SIMILARITY_THRESHOLD` | `0.95` | Minimum cosine similarity for a semantic hit |
| `EMBEDDING_MODEL` | `nomic-embed-text` | Ollama embedding model |
| `RATE_LIMIT_TPS` / `RATE_LIMIT_BURST` | `5` / `10` | Token bucket per API key; each request costs one token |

**Models and failover.** The client's `model` is sent only to the primary provider. Model names don't carry across providers, so a fallback always uses its own configured model. You can also leave `model` out and the primary uses its default.

## Usage

```bash
# Chat completion
curl -s localhost:8080/v1/chat/completions \
  -H 'Authorization: Bearer my-key' -H 'Content-Type: application/json' \
  -d '{"messages":[{"role":"user","content":"What is the capital of France?"}]}' -i

# The same request again is answered from the cache. Check the headers:
#   X-Cache: exact          (exact | semantic | miss)
#   X-Provider: ollama

# Streaming (SSE, OpenAI chunk format, ends with data: [DONE])
curl -N localhost:8080/v1/chat/completions \
  -H 'Authorization: Bearer my-key' -H 'Content-Type: application/json' \
  -d '{"stream":true,"messages":[{"role":"user","content":"Count to 5"}]}'

# Any OpenAI SDK works by pointing its base URL at the gateway
#   OpenAI(base_url="http://localhost:8080/v1", api_key="my-key")

# Health: 200 if at least one provider is up, 503 if none
curl -s localhost:8080/health
# {"status":"degraded","providers":{"groq":{"status":"up","latency_ms":112},
#  "ollama":{"status":"down","latency_ms":1,"error":"ollama: ... connection refused"}}}

# Metrics
curl -s localhost:8080/metrics | grep ^llm_
```

## Metrics

| Metric | Labels | Notes |
|---|---|---|
| `llm_requests_total` | `provider`, `status` | One per upstream attempt. `status` is `ok`, the upstream HTTP code (`429`, `503`, …) or `error` |
| `llm_cache_hits_total` | `cache_type` | `exact` or `semantic` |
| `llm_request_duration_seconds` | `provider` | Histogram of upstream latency; time to first byte for streams |
| `llm_tokens_used_total` | `api_key`, `provider` | Upstream tokens. `api_key` is an 8-character SHA-256 prefix so raw keys never reach `/metrics`; cache hits cost 0 |

Useful queries:

```promql
# p50 / p99 upstream latency per provider
histogram_quantile(0.50, sum by (le, provider) (rate(llm_request_duration_seconds_bucket[5m])))
histogram_quantile(0.99, sum by (le, provider) (rate(llm_request_duration_seconds_bucket[5m])))

# share of requests answered from cache
sum(rate(llm_cache_hits_total[5m]))
  / (sum(rate(llm_cache_hits_total[5m])) + sum(rate(llm_requests_total{status="ok"}[5m])))

# upstream error rate by provider
sum by (provider) (rate(llm_requests_total{status!="ok"}[5m])) / sum by (provider) (rate(llm_requests_total[5m]))
```

## Load test

[`k6/load-test.js`](k6/load-test.js) runs 100 VUs for 30 s. Traffic is 60% verbatim repeats (exact hits), 30% paraphrases (semantic hits) and 10% unique prompts (misses). Each VU uses its own API key and the cache is warmed in `setup()`.

```bash
k6 run k6/load-test.js                     # or: k6 run -e BASE_URL=http://host:8080 k6/load-test.js
```

### Benchmark results

> Placeholder. Run the test against your setup and paste the k6 summary here.

```
         /\      Grafana   /‾‾/
    /\  /  \     |\  __   /  /
   /  \/    \    | |/ /  /   ‾‾\
  /          \   |   (  |  (‾)  |
 / __________ \  |_|\_\  \_____/

     execution: local
        script: k6/load-test.js
        output: -

     scenarios: (100.00%) 1 scenario, 100 max VUs, 1m0s max duration (incl. graceful stop):
              * default: 100 looping VUs for 30s (gracefulStop: 30s)

  █ THRESHOLDS

    checks
    ✓ 'rate>0.9' rate=__.__%

    latency_cache_hit
    ✓ 'p(95)<250' p(95)=__ms

  █ TOTAL RESULTS

    checks_total.......................: ____    ___/s
    checks_succeeded...................: __.__%  ____ out of ____

    CUSTOM
    cache_exact_hits...................: ____    ___/s
    cache_hit_rate.....................: __.__%  ____ out of ____
    cache_misses.......................: ____    ___/s
    cache_semantic_hits................: ____    ___/s
    latency_cache_hit..................: avg=__ms  min=__ms  med=__ms  max=__ms  p(90)=__ms  p(95)=__ms
    latency_cache_miss.................: avg=__ms  min=__ms  med=__ms  max=__s   p(90)=__s   p(95)=__s
    rate_limited.......................: ____    ___/s

    HTTP
    http_req_duration..................: avg=__ms  min=__ms  med=__ms  max=__s   p(90)=__ms  p(95)=__ms
    http_req_failed....................: _.__%   __ out of ____
    http_reqs..........................: ____    ___/s
```

| Setup | Req/s | Cache hit rate | p95 hit latency | p95 miss latency | Upstream tokens saved |
|---|---|---|---|---|---|
| *TBD* | — | — | — | — | — |

## Development

```bash
go test ./...
go vet ./...
```

## CI/CD

[`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs on every push and pull request:

1. **test**: gofmt check, `go vet`, `go test -race`, and a build.
2. **docker**: builds the image, then smoke-tests the real container. It must serve `/metrics` and report `503` on `/health` when no provider is reachable.
3. **publish** (pushes to `main` and `v*` tags only): builds `linux/amd64` + `linux/arm64` and pushes to Docker Hub as `latest` and `sha-<commit>`. A tag `vX.Y.Z` also pushes `X.Y.Z` and `X.Y`.

Publishing turns on once these two are set on the GitHub repo. Until then the pipeline still builds and smoke-tests the image, it just skips the push.

```bash
gh variable set DOCKERHUB_USERNAME --body <your-dockerhub-username>
gh secret set DOCKERHUB_TOKEN   # paste a Docker Hub access token (Account settings → Personal access tokens, Read & Write)
```

## Design notes and known limits

- **Semantic search is a linear scan** over at most 10k vectors per model. Past that, use an ANN index (HNSW, pgvector, Qdrant).
- **The exact cache has no size cap.** Entries expire after the TTL. Add an LRU bound if the number of distinct prompts gets large.
- **No request coalescing.** Identical concurrent misses each call upstream. Add `singleflight` if stampedes matter.
- **The cache key leaves out sampling parameters** (temperature, max_tokens), so a prompt has one canonical cached answer. Partial or failed streams are never cached.
- **Text-only messages.** Multimodal content arrays are rejected with a 400. Tool calls are not proxied.
- **The rate limiter refills from one goroutine under a global lock.** That's fine for thousands of keys. For more, refill lazily inside `Allow`.
