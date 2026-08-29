# LLM Gateway

[![CI/CD](https://github.com/ThatDeparted2061/LLM-Gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/ThatDeparted2061/LLM-Gateway/actions/workflows/ci.yml)
[![Docker Hub](https://img.shields.io/badge/docker-thatdeparted2061%2Fllm--gateway-2496ED?logo=docker&logoColor=white)](https://hub.docker.com/r/thatdeparted2061/llm-gateway)
[![Image size](https://img.shields.io/docker/image-size/thatdeparted2061/llm-gateway/latest?label=image%20size)](https://hub.docker.com/r/thatdeparted2061/llm-gateway/tags)
[![Docker pulls](https://img.shields.io/docker/pulls/thatdeparted2061/llm-gateway)](https://hub.docker.com/r/thatdeparted2061/llm-gateway)

An OpenAI-compatible gateway in Go that sits in front of **Groq**, **Google Gemini** and a local **Ollama**. It gives clients one endpoint, `POST /v1/chat/completions`, and handles the rest:

- **Rate limiting.** A token bucket per API key.
- **Two-tier caching.** An exact-match SHA-256 cache, plus a semantic cache that uses Ollama embeddings and cosine similarity.
- **Routing.** Failover across providers, with exponential-backoff retries on 429/5xx.
- **Streaming.** SSE passthrough. A completed stream is cached and replayed on later hits.
- **Observability.** Prometheus metrics and a per-provider health check.
- **Shipping.** A ~5 MB multi-arch (amd64/arm64) distroless image on [Docker Hub](https://hub.docker.com/r/thatdeparted2061/llm-gateway). GitHub Actions tests it, smoke-tests the container, and publishes it on every push to `main`.

**Measured on a MacBook Air M4** ([details](#benchmark-results)): **93% cache hit rate** on a mixed 100-user workload, with cache hits at **0.37 ms** median against ~30 s misses on a local 3B model. The cached path sustained **44k req/s**, and the gateway adds **2.3 µs** to a cached request.

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

### Docker Hub image (fastest)

The prebuilt image is [`thatdeparted2061/llm-gateway`](https://hub.docker.com/r/thatdeparted2061/llm-gateway). It runs as non-root on distroless and comes in `linux/amd64` and `linux/arm64`, so it runs natively on Apple Silicon.

```bash
docker pull thatdeparted2061/llm-gateway:latest

# Cloud providers only (no Ollama needed)
docker run -p 8080:8080 \
  -e PRIMARY_PROVIDER=groq -e GROQ_API_KEY=gsk_... -e GEMINI_API_KEY=AIza... \
  -e SEMANTIC_CACHE=false -e GATEWAY_API_KEYS=change-me \
  thatdeparted2061/llm-gateway:latest

# Or use the Ollama on your host as the primary, with semantic caching
# (Docker Desktop; on Linux also add --add-host=host.docker.internal:host-gateway)
docker run -p 8080:8080 \
  -e PRIMARY_PROVIDER=ollama -e OLLAMA_BASE_URL=http://host.docker.internal:11434 \
  thatdeparted2061/llm-gateway:latest
```

| Tag | What it is |
|---|---|
| `latest` | Head of `main` |
| `sha-<commit>` | One immutable tag per commit, for pinning and rollbacks |
| `X.Y.Z`, `X.Y` | Published when a `vX.Y.Z` git tag is pushed |

Configuration is all env vars (see [Configuration](#configuration)). To use a YAML file instead, mount it and pass `-config`: `-v $PWD/config.yaml:/config.yaml thatdeparted2061/llm-gateway -config /config.yaml`.

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
| `llm_requests_total` | `provider`, `status` | One per upstream attempt. `status` is `ok`, the upstream HTTP code (`429`, `503`, …), `canceled` when the client disconnected, or `error` |
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
k6 run -e BASE_URL=http://localhost:8080 k6/load-test.js                          # mixed workload
k6 run -e BASE_URL=http://localhost:8080 -e REPEAT_ONLY=1 -e NO_SLEEP=1 k6/load-test.js   # cached throughput
```

## Benchmark results

Measured on 2 Oct 2026:

- **Hardware:** MacBook Air (Apple M4, 10 cores, 16 GB), with the gateway and k6 v2.3.0 on the same machine.
- **Upstream:** a local Ollama serving `qwen2.5-coder:3b` for chat and `nomic-embed-text` for embeddings. No Groq or Gemini keys were used.
- **Config:** defaults. TTL 1 h, similarity threshold 0.95, and 5 req/s per key with a burst of 10.

### 1. Mixed workload (100 VUs, 30 s)

| Metric | Result |
|---|---|
| Requests | 746, **0 failed** |
| Cache hit rate | **93.0%**: 658 exact and 31 semantic hits out of 741 |
| Cache-hit latency | **p50 0.37 ms**, p95 12.3 ms |
| Cache-miss latency | p50 30.5 s, p95 55.0 s |
| Upstream calls | 57 completed; 70 cancelled by k6 when the test ended |
| Upstream tokens | 4,906 used; **about 59k avoided by cache hits**, roughly 92% of demand at 86 tokens per answer |

Misses are slow because one 3B model on a laptop GPU was working through a queue of up to 100 concurrent requests. Each VU waits for its answer, so those misses also cap the overall rate at 11.5 req/s. The gateway answered cache hits in under a millisecond at the median.

<details>
<summary>Raw k6 summary</summary>

```
  █ THRESHOLDS

    checks
    ✓ 'rate>0.9' rate=100.00%

    latency_cache_hit
    ✓ 'p(95)<250' p(95)=12.32ms


  █ TOTAL RESULTS

    checks_total.......: 741     11.437183/s
    checks_succeeded...: 100.00% 741 out of 741
    checks_failed......: 0.00%   0 out of 741

    ✓ status is 200

    CUSTOM
    cache_exact_hits...............: 658    10.156095/s
    cache_hit_rate.................: 92.98% 689 out of 741
    cache_misses...................: 52     0.802609/s
    cache_semantic_hits............: 31     0.478479/s
    latency_cache_hit..............: avg=15.7ms min=140µs    med=365µs    max=554.97ms p(90)=4ms      p(95)=12.32ms
    latency_cache_miss.............: avg=29.59s min=1.89s    med=30.5s    max=57.96s   p(90)=51.75s   p(95)=54.98s

    HTTP
    http_req_duration..............: avg=2.08s  min=140µs    med=371µs    max=57.96s   p(90)=335.14ms p(95)=15.26s
      { expected_response:true }...: avg=2.08s  min=140µs    med=371µs    max=57.96s   p(90)=335.14ms p(95)=15.26s
    http_req_failed................: 0.00%  0 out of 746
    http_reqs......................: 746    11.514357/s

    EXECUTION
    iteration_duration.............: avg=2.44s  min=201.43ms med=372.38ms max=58.29s   p(90)=607.07ms p(95)=15.78s
    iterations.....................: 741    11.437183/s
    vus............................: 70     min=0          max=100
    vus_max........................: 100    min=100        max=100
```
</details>

### 2. Cached throughput (100 VUs, 30 s, no think time)

The gateway ran with `RATE_LIMIT_TPS=1000000 RATE_LIMIT_BURST=1000000`, so this measures the cache-hit path rather than the limiter.

| Metric | Result |
|---|---|
| Throughput | **44,460 req/s**: 1.56M requests in 30 s |
| Latency | p50 1.19 ms, p90 3.23 ms, p95 4.22 ms |
| Failures | 0 |

k6 itself was competing for the same 10 cores, so a dedicated host would do better.

### 3. Semantic threshold check

These are `nomic-embed-text` cosine similarities between each k6 paraphrase and its original prompt:

| Measure | Result |
|---|---|
| Paraphrases at or above 0.95 (become semantic hits) | 11 of 15; scores ranged from 0.904 to 0.996 |
| Highest similarity between two different topics | 0.58 |
| Highest similarity of an unrelated prompt to any topic | 0.63 |

At 0.95 this sample produced no false hits. Every paraphrase here scored above 0.90 and unrelated prompts stayed below 0.65, so 0.90 would also work on this data. Tune it against your own traffic.

### 4. Go micro-benchmarks

Run with `go test -run '^$' -bench . -benchmem ./internal/...` on the Apple M4:

| Benchmark | Result |
|---|---|
| Exact-cache hit through the whole handler: auth, rate limit, JSON decode, SHA-256 key, lookup, JSON encode | **2.3 µs/op**, 45 allocations |
| Cache key: SHA-256 of model + messages | 261 ns/op |
| Semantic lookup in a full cache of 10k 768-dim vectors | 6.3 ms/op |
| Rate limiter `Allow` across 1,000 keys, called from 10 goroutines at once | 144 ns/op |

### 5. Tests

`go test -race ./...` runs 18 tests, all passing, with no data races. Statement coverage across `internal/` is 78.4%:

| Package | Coverage |
|---|---|
| ratelimit | 96% |
| cache | 85% |
| router | 84% |
| handlers | 81% |
| config | 71% |
| providers | 71% |

## Development

```bash
go test -race -cover ./...
go vet ./...
go test -run '^$' -bench . -benchmem ./internal/...
```

## CI/CD

[`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs on every push and pull request:

1. **test**: gofmt check, `go vet`, `go test -race`, and a build.
2. **docker**: builds the image, then smoke-tests the real container. It must serve `/metrics` and report `503` on `/health` when no provider is reachable.
3. **publish** (pushes to `main` and `v*` tags only): cross-compiles `linux/amd64` + `linux/arm64` and pushes to [Docker Hub](https://hub.docker.com/r/thatdeparted2061/llm-gateway/tags) as `latest` and `sha-<commit>`. A tag `vX.Y.Z` also pushes `X.Y.Z` and `X.Y`.

```
push / PR ──► test (fmt, vet, race tests) ──► docker build ──► container smoke test ──► publish to Docker Hub
                                                                                      (main and v* tags only)
```

To cut a release: `git tag v1.0.0 && git push origin v1.0.0`.

Publishing reads the repo variable `DOCKERHUB_USERNAME` and the repo secret `DOCKERHUB_TOKEN`. A fork without them still builds and smoke-tests the image; it just skips the push.

## Design notes and known limits

- **Semantic search is a linear scan.** A full 10k-entry cache takes about 6 ms per lookup (benchmark 4), which is small next to an LLM call. To hold more entries, use an ANN index (HNSW, pgvector, Qdrant).
- **The exact cache has no size cap.** Entries expire after the TTL. Add an LRU bound if the number of distinct prompts gets large.
- **No request coalescing.** Identical concurrent misses each call upstream. Add `singleflight` if stampedes matter.
- **The cache key leaves out sampling parameters** (temperature, max_tokens), so a prompt has one canonical cached answer. Partial or failed streams are never cached.
- **Text-only messages.** Multimodal content arrays are rejected with a 400. Tool calls are not proxied.
- **The rate limiter refills from one goroutine under a global lock.** That's fine for thousands of keys. For more, refill lazily inside `Allow`.
