// k6 run k6/load-test.js            (gateway on localhost:8080)
// k6 run -e BASE_URL=http://host:8080 k6/load-test.js
//
// 100 VUs for 30s, mixing repeated prompts (exact-cache hits), paraphrased
// prompts (semantic-cache hits) and unique prompts (always miss). The gateway's
// X-Cache response header (exact | semantic | miss) feeds the custom metrics.
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

const cacheHitRate = new Rate('cache_hit_rate');
const exactHits = new Counter('cache_exact_hits');
const semanticHits = new Counter('cache_semantic_hits');
const misses = new Counter('cache_misses');
const rateLimited = new Counter('rate_limited');
const hitLatency = new Trend('latency_cache_hit', true);
const missLatency = new Trend('latency_cache_miss', true);

export const options = {
  vus: 100,
  duration: '30s',
  thresholds: {
    checks: ['rate>0.9'],
    latency_cache_hit: ['p(95)<250'],
  },
};

// Each topic has one canonical prompt (sent verbatim -> exact hits) and
// paraphrases (-> semantic hits once the canonical answer is cached).
const TOPICS = [
  {
    prompt: 'What is the capital of France?',
    paraphrases: ['What is the capital city of France?', "What's the capital of France?", 'Which city is the capital of France?'],
  },
  {
    prompt: 'Explain what a goroutine is in one sentence.',
    paraphrases: ['Explain in one sentence what a goroutine is.', 'In one sentence, what is a goroutine?', 'Describe a goroutine in a single sentence.'],
  },
  {
    prompt: 'What is the boiling point of water at sea level?',
    paraphrases: ['At sea level, what is the boiling point of water?', "What's water's boiling point at sea level?", 'At what temperature does water boil at sea level?'],
  },
  {
    prompt: 'Name three benefits of unit testing.',
    paraphrases: ['List three benefits of unit testing.', 'Give me three advantages of unit testing.', 'What are three benefits of writing unit tests?'],
  },
  {
    prompt: 'What does HTTP status code 429 mean?',
    paraphrases: ['What is the meaning of HTTP status 429?', 'What does a 429 HTTP response mean?', 'Explain the HTTP 429 status code.'],
  },
];

const pick = (arr) => arr[Math.floor(Math.random() * arr.length)];

function chat(content, apiKey) {
  return http.post(
    `${BASE_URL}/v1/chat/completions`,
    JSON.stringify({ messages: [{ role: 'user', content }], max_tokens: 64 }),
    {
      // One key per VU, so the per-key rate limiter doesn't throttle the whole test.
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${apiKey}` },
      timeout: '60s',
    },
  );
}

// Warm the cache with each canonical prompt so 100 VUs don't all miss at once.
export function setup() {
  for (const t of TOPICS) chat(t.prompt, 'k6-setup');
}

export default function () {
  const topic = pick(TOPICS);
  const roll = Math.random();
  // 60% verbatim repeats, 30% paraphrases, 10% unique prompts
  const content =
    roll < 0.6 ? topic.prompt
    : roll < 0.9 ? pick(topic.paraphrases)
    : `In one sentence, what is interesting about the number ${Math.floor(Math.random() * 1e6)}?`;

  const res = chat(content, `k6-vu-${__VU}`);
  if (res.status === 429) {
    rateLimited.add(1);
  } else {
    check(res, { 'status is 200': (r) => r.status === 200 });
    const cache = res.headers['X-Cache'];
    cacheHitRate.add(cache === 'exact' || cache === 'semantic');
    if (cache === 'exact') exactHits.add(1);
    if (cache === 'semantic') semanticHits.add(1);
    if (cache === 'miss') misses.add(1);
    (cache === 'miss' ? missLatency : hitLatency).add(res.timings.duration);
  }
  sleep(0.2 + Math.random() * 0.3); // ~2-4 req/s per VU, under the default 5 tokens/sec limit
}
