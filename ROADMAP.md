# LLM Gateway — Development Roadmap

## Context

构建一个位于客户端和 LLM Provider 之间的智能代理层（LLM Gateway），处理 routing、caching、rate limiting、cost control 和 observability。目标是打造一个有技术深度、适合北美大厂简历的后端项目。

**技术栈**: Go + chi + PostgreSQL (pgvector) + Redis + Kafka + ClickHouse + Prometheus/Grafana + Docker/K8s

## Project Structure

```
llm-gateway/
├── cmd/gateway/main.go
├── internal/
│   ├── config/          # 配置加载 (YAML + env)
│   ├── server/          # HTTP server, middleware chain
│   ├── handler/         # HTTP handlers
│   ├── middleware/      # Auth, rate limiting, logging
│   ├── model/           # Domain types (OpenAI-compatible)
│   ├── provider/        # Provider adapters
│   │   ├── registry.go
│   │   ├── openai/
│   │   ├── anthropic/
│   │   ├── google/
│   │   └── selfhosted/
│   ├── router/          # Smart routing (cost, latency, fallback)
│   ├── cache/           # Semantic cache (pgvector)
│   ├── ratelimit/       # Redis-backed rate limiter
│   ├── auth/            # API key validation
│   ├── token/           # Token counting, cost calculation
│   ├── budget/          # Budget enforcement
│   ├── streaming/       # SSE proxy, mid-stream counting
│   ├── pipeline/        # Kafka producers/consumers
│   ├── metrics/         # Prometheus collectors
│   └── store/           # Database access
├── pkg/openaicompat/    # Exported OpenAI-compatible types
├── migrations/          # SQL migrations
├── deployments/
│   ├── docker/
│   └── k8s/
├── scripts/
├── tests/
├── configs/gateway.yaml
├── go.mod
└── Makefile
```

---

## Documentation Architecture for Agent-Driven Development

### Overview

本项目全程与 AI Agent 协作开发。Agent 每次 session 无状态，需要从文档重建上下文。文档体系分三层，按 context loading 需求设计：

| Layer | 文档 | 加载时机 | 目的 |
|-------|------|---------|------|
| L1: 自动加载 | `CLAUDE.md` | 每次 session 自动 | Agent "操作手册"，150-200 行 |
| L2: 按需加载 | `docs/*.md` | 处理特定模块时 | 设计详情、接口规范、决策记录 |
| L3: 任务加载 | GitHub Issues | 执行具体 issue 时 | 自包含的执行规范 |

### L1: CLAUDE.md（仓库根目录，自动加载）

Claude Code 每次启动自动读取此文件。这是 agent 效率的最大杠杆。

```markdown
# LLM Gateway

一个位于客户端和 LLM Provider 之间的智能代理层，
处理 routing、caching、rate limiting、cost control、observability。

## Architecture

    Client → LLM Gateway → LLM Providers (OpenAI, Anthropic, Google, Self-hosted)
                 │
      ┌──────────┼──────────────┐
      │          │              │
    Redis    PostgreSQL    Kafka → ClickHouse
  (cache,    (pgvector,   (async events,
  rate limit) API keys,    analytics)
              usage)

## Tech Stack
- Language: Go 1.23+
- HTTP Router: chi (stdlib-compatible)
- Config: koanf (YAML + env overlay)
- Database: PostgreSQL 16 + pgvector extension
- Cache/Rate Limit: Redis 7
- Message Queue: Kafka (segmentio/kafka-go)
- Analytics: ClickHouse
- Monitoring: Prometheus + Grafana
- Logging: slog (stdlib, JSON format)
- Migration: golang-migrate
- HTTP Client: net/http (no SDK, for streaming control)

## Project Structure
(目录树 + 每个 package 一句话职责说明)

## Key Interfaces

Provider interface (internal/provider/provider.go):
  - ChatCompletion(ctx, req) → (resp, error)
  - ChatCompletionStream(ctx, req) → (<-chan StreamEvent, error)
  - Models() → []string

Router interface (internal/router/router.go):
  - Route(ctx, req, meta) → (Provider, error)

CacheStore interface (internal/cache/store.go):
  - Get(ctx, input, embedding, model) → (resp, hit, error)
  - Set(ctx, input, embedding, req, resp, ttl) → error

## Coding Conventions
- Error handling: return errors, don't panic. Wrap with fmt.Errorf("doing X: %w", err)
- Naming: Go standard (camelCase for unexported, PascalCase for exported)
- Context: always pass context.Context as first parameter
- Logging: use slog from request context, never log PII at INFO level
- Testing: table-driven tests, use testify/assert, mock HTTP with httptest.Server
- Middleware: standard signature func(http.Handler) http.Handler

## Common Commands
- make build          — Build binary to ./bin/gateway
- make test           — Run all tests
- make lint           — Run golangci-lint
- make docker-build   — Build Docker image
- docker compose up   — Start full local stack
- make migrate-up     — Run database migrations

## Development Guides
- Implementing a new provider adapter → read docs/provider-adapter.md
- Modifying routing logic → read docs/routing-strategies.md
- Changing database schema → read docs/data-model.md
- Understanding a technical decision → read docs/adr/

## Current Status
- [x] Milestone 1: Transparent Proxy (Foundation)
- [ ] Milestone 2: Multi-Provider Support ← CURRENT
- [ ] Milestone 3: Auth, Rate Limiting & Observability
...
```

### L2: docs/ 目录结构

```
docs/
├── architecture.md          # 详细架构 + 数据流 + 组件交互图
├── api-spec.md              # 完整 API 规范 (所有 endpoint, request/response 示例)
├── provider-adapter.md      # 如何实现新 provider (教程式, 含代码模板)
├── routing-strategies.md    # 路由策略设计, 每种策略的算法和 trade-off
├── semantic-cache.md        # 缓存方案: embedding, 相似度阈值, 缓存策略
├── data-model.md            # 所有表结构, ER 图, migration 策略
├── streaming.md             # SSE streaming 处理: 协议, 中断, token 计数
├── kafka-pipeline.md        # 事件 schema, topic 设计, consumer group 策略
└── adr/                     # Architecture Decision Records
    ├── 001-chi-over-gin.md
    ├── 002-koanf-over-viper.md
    ├── 003-pgvector-over-dedicated-vectordb.md
    ├── 004-slog-over-zap.md
    └── 005-segmentio-kafka-over-confluent.md
```

#### docs/ 文件编写原则

1. **面向 agent 而非面向人** — 不要写散文式叙述，用结构化格式（表格、代码块、清单）
2. **Include runnable examples** — 每个接口附带示例 input/output JSON
3. **说明 "why not"** — agent 需要知道为什么不用某个方案，否则可能会自行 "优化" 成被否决的方案
4. **引用具体文件路径** — 不要写 "在 provider 包中"，写 `internal/provider/openai/openai.go:L45`

#### ADR 格式

每个 ADR 遵循以下格式：
```markdown
# ADR-001: Use chi over gin for HTTP routing

## Status: Accepted
## Context: 需要选择 HTTP router 框架
## Decision: 选择 chi
## Rationale:
- chi 使用标准 `net/http` 接口, middleware 签名是 `func(http.Handler) http.Handler`
- gin 使用自定义 `gin.Context`, 导致 vendor lock-in
- chi 性能与 gin 相当, 但可组合性更好
## Consequences:
- 所有 middleware 必须遵循标准签名
- 可以直接使用任何兼容 net/http 的第三方 middleware
## Alternatives Rejected:
- gin: 自定义 Context 导致与标准库不兼容
- gorilla/mux: 已停止维护
- stdlib only: 路由参数处理不够方便
```

### L3: GitHub Issue Template

在仓库 `.github/ISSUE_TEMPLATE/` 中创建 issue 模板：

#### `.github/ISSUE_TEMPLATE/feature.md`

```markdown
---
name: Feature Implementation
about: Implement a feature from the roadmap
labels: enhancement
---

## Context
<!--
这个 issue 属于哪个 Milestone？它在整体架构中扮演什么角色？
前置 issues 是什么？有哪些已实现的模块与此相关？
-->

## Prerequisites
<!--
开始之前 agent 应该阅读哪些文件：
- CLAUDE.md (自动加载)
- docs/xxx.md (相关设计文档)
- internal/xxx/xxx.go (参考实现或依赖的接口)
-->

## Task Description
<!--
具体要实现什么功能。用清晰、无歧义的语言描述。
包含关键的数据结构、算法、或协议细节。
-->

## Files to Create/Modify
<!--
列出需要创建或修改的每个文件，以及每个文件应包含什么。
- 创建 `internal/xxx/xxx.go` — (描述内容)
- 修改 `internal/xxx/xxx.go` — (描述修改点)
- 创建 `internal/xxx/xxx_test.go` — (描述测试内容)
-->

## Interface Contracts
<!--
如果涉及新接口或修改接口，写出完整的 Go interface 定义。
如果是实现已有接口，引用接口定义的文件路径。
-->

## Acceptance Criteria
<!--
- [ ] 具体的可验证条件（不要模糊描述）
- [ ] 测试要求
- [ ] 性能要求（如有）
-->

## Out of Scope
<!--
明确列出 不要做 的事情，防止 agent 过度工程。
-->

## References
<!--
相关文档、外部 API 文档链接、参考实现等。
-->
```

### Development Workflow

每个 Milestone 遵循以下流程：

```
Phase 1: 准备
├── 细化该 Milestone 所有 issues (填充 issue template)
├── 更新 docs/ 中相关设计文档 (如有新模块)
├── 确认 issue 依赖关系和执行顺序
└── 如有新 ADR，创建对应文件

Phase 2: 开发 (Agent 执行, 你 review)
├── Agent 按依赖顺序逐个处理 issue
├── 每个 issue 产出一个 PR
├── 你 review PR，学习代码，反馈修改意见
└── Agent 修改后合并

Phase 3: 收尾
├── 更新 CLAUDE.md 中的 "Current Status"
├── 如果实现中产生了新 pattern/约定，更新 CLAUDE.md
├── 粗写下一个 Milestone 的 issues
└── 回顾 docs/ 是否需要更新
```

### Issue 细化的节奏

**不要一次细化所有 issues。** 遵循以下节奏：
- 当前 Milestone: 所有 issues 完全细化 (填满 template 每个字段)
- 下一个 Milestone: issues 粗写 (ROADMAP.md 中的简介级别即可)
- 更远的 Milestone: 仅保留 ROADMAP.md 中的标题和 Goal

原因: 远期 issues 的具体实现依赖近期 issues 的代码结果。过早细化会导致规范与实际脱节。

### Agent 上下文管理策略

为了在 agent context limit 内最大化有效信息：

1. **CLAUDE.md 是唯一的 "必读"** — 控制在 200 行内，只放最关键的信息
2. **Issue 中用 "Prerequisites" 字段显式列出需要读的文件** — agent 按需加载，不用猜
3. **docs/ 中的设计文档按模块独立** — agent 做 cache issue 时只需读 `docs/semantic-cache.md`，不需读 `docs/routing-strategies.md`
4. **接口定义集中在少数文件** — `internal/provider/provider.go`、`internal/router/router.go` 等，agent 读几个文件就能理解系统边界
5. **每个 PR 合并后在 issue 中留 comment 总结实际实现** — 下一个 issue 的 agent 可以快速了解前置工作的结果

### 落地计划: Milestone 0 — Documentation Bootstrap

在开始 Milestone 1 开发前，先完成以下文档搭建工作：

**Issue 0.1: Create CLAUDE.md**
- 按上述模板创建 CLAUDE.md
- 包含项目描述、架构图、技术栈、目录结构、关键接口、编码约定、常用命令
- 初始 "Current Status" 为空

**Issue 0.2: Create docs/ design documents**
- `docs/architecture.md` — 详细架构说明、请求生命周期、组件交互
- `docs/api-spec.md` — OpenAI-compatible API 完整规范 (endpoints, request/response 示例)
- `docs/provider-adapter.md` — Provider adapter 实现指南 (接口定义、代码模板、测试模式)
- `docs/data-model.md` — 数据库表结构设计 (目前为规划，随实现更新)
- 其余 docs (routing, cache, streaming, kafka) 在对应 Milestone 开始前创建

**Issue 0.3: Create ADRs for initial technical decisions**
- ADR-001 到 ADR-005 (chi, koanf, segmentio/kafka-go, golang-migrate, slog)
- 每个 ADR 遵循 Status/Context/Decision/Rationale/Consequences/Alternatives 格式

**Issue 0.4: Create GitHub infrastructure**
- `.github/ISSUE_TEMPLATE/feature.md` — issue 模板
- `.github/PULL_REQUEST_TEMPLATE.md` — PR 模板
- GitHub Milestones (M1-M10)
- GitHub Labels: `milestone:1` 到 `milestone:10`, `type:feature`, `type:infra`, `type:docs`

---

## Milestone 1: Transparent Proxy (Foundation)

**Goal**: 最小可用代理 — 接收 OpenAI 格式请求，转发到 OpenAI，返回响应。

### Issue 1.1: Project Scaffolding and Build Pipeline
- Go module 初始化，目录结构创建
- Makefile: `build`, `run`, `test`, `lint`, `docker-build`
- Dockerfile (multi-stage build)
- GitHub Actions CI: lint + test + build
- **验收**: `make build` 产出二进制，CI 通过

### Issue 1.2: Define Core Domain Types
- 定义 OpenAI-compatible 的 request/response/streaming 结构体 (`internal/model/chat.go`)
- 包含: `ChatCompletionRequest`, `Message`, `ChatCompletionResponse`, `Choice`, `Usage`, `ChatCompletionChunk`, `APIError`
- **验收**: JSON round-trip 测试通过，能正确反序列化 OpenAI 真实响应

### Issue 1.3: Provider Interface and OpenAI Adapter
- 定义 `Provider` interface: `ChatCompletion()`, `ChatCompletionStream()`, `Models()`
- 定义 `StreamEvent` struct 和 `Registry` (model name → provider 映射)
- 实现 OpenAI adapter (用 `net/http`，不用第三方 SDK)
- 支持 streaming: 读取 SSE `data:` 行，解析为 chunk，通过 channel 发送
- **验收**: mock HTTP server 单元测试通过，context cancel 能在 100ms 内终止 streaming goroutine

### Issue 1.4: HTTP Server and Chat Completions Handler
- chi router 搭建，基础 middleware (request ID, panic recovery, request logging)
- `POST /v1/chat/completions` handler: 解析请求 → 解析 provider → 调用 → 返回
- SSE streaming proxy: 设置正确 headers，逐 chunk flush
- `GET /v1/models`, `GET /health`
- **验收**: curl 能成功调用并得到响应，streaming 格式正确

### Issue 1.5: Configuration and Docker Compose
- 用 koanf 加载 YAML + env 覆盖
- 配置项: server port, provider API keys, log level
- Docker Compose: gateway service
- **验收**: `docker compose up` 启动并响应 health check

---

## Milestone 2: Multi-Provider Support

**Goal**: 支持 OpenAI + Anthropic + Google Gemini，根据 model name 自动路由。

### Issue 2.1: Anthropic Provider Adapter
- 转换 OpenAI format ↔ Anthropic Messages API
- system message 提取为顶层字段
- stop_reason 映射 (`end_turn` → `stop`, `max_tokens` → `length`)
- Streaming: 处理 `message_start`, `content_block_delta`, `message_stop` 等事件类型
- **验收**: mock Anthropic server 测试通过（streaming + non-streaming）

### Issue 2.2: Google Gemini Provider Adapter
- 转换 OpenAI format ↔ Gemini `generateContent` API
- role 映射 (`assistant` → `model`)，content → parts 转换
- Streaming: Gemini JSON stream → SSE chunks
- **验收**: mock Gemini server 测试通过

### Issue 2.3: Model-to-Provider Routing via Configuration
- 配置驱动的 model → provider 映射
- 支持 model aliases (`"claude"` → `"claude-sonnet-4-20250514"`)
- `/v1/models` 聚合所有 provider 的模型列表
- 未知 model 返回 400 + 可用模型列表
- **验收**: 不同 model name 正确路由到不同 provider

### Issue 2.4: Provider Health Checks and Timeout Configuration
- 每个 provider 追踪 `ProviderHealth` (consecutive fails, last success/failure)
- 连续 N 次失败标记 unhealthy，cooldown 后恢复
- Per-provider timeout + retry with exponential backoff (429, 5xx)
- `GET /internal/health` 暴露各 provider 健康状态
- **验收**: provider 故障后自动标记 unhealthy，cooldown 后恢复

---

## Milestone 3: Authentication, Rate Limiting & Observability

**Goal**: 多租户支持 — API key 认证，per-key 限流，Prometheus 监控。

### Issue 3.1: API Key Authentication Middleware
- PostgreSQL 存储 API keys (只存 SHA-256 hash)
- `organizations` + `api_keys` 表，支持 org, scopes, rate limit 配置
- Auth middleware: 提取 Bearer token → hash → 查表 → 注入 context
- 内存缓存 5 min TTL
- Admin endpoints: `POST /internal/admin/keys`, `DELETE /internal/admin/keys/{id}`
- **验收**: 无 key/无效 key/过期 key 返回 401

### Issue 3.2: Redis-Backed Rate Limiting
- Sliding window 算法 (Redis sorted set)
- RPM (请求数/分钟) + TPM (token 数/分钟)
- RPM 在请求前检查，TPM 在响应后更新
- 429 响应 + `Retry-After` header + OpenAI 兼容 error body
- Redis 故障时降级为 allow (不阻塞请求)
- **验收**: 超限请求返回 429，rate limit headers 正确

### Issue 3.3: Prometheus Metrics
- 请求指标: `gateway_requests_total`, `gateway_request_duration_seconds`
- Provider 指标: `gateway_provider_requests_total`, `gateway_provider_request_duration_seconds`
- Token 指标: `gateway_tokens_total`, `gateway_estimated_cost_dollars`
- Rate limit / cache / 系统指标
- 独立 metrics port (9090)
- 导出 Grafana dashboard JSON
- **验收**: `/metrics` 返回有效 Prometheus 格式

### Issue 3.4: Structured Logging with Context Propagation
- 使用 Go `slog` 标准库
- 每个请求一条 INFO log: method, path, status, latency, model, provider, tokens
- 不在 INFO 级别记录任何 PII (消息内容)
- DEBUG 模式可选记录 request/response body
- **验收**: 所有日志为 JSON 格式，包含完整上下文字段

### Issue 3.5: Docker Compose with Full Stack
- 添加: PostgreSQL (pgvector/pgvector:pg16), Redis, Prometheus, Grafana
- 自动运行 database migrations (golang-migrate)
- **验收**: `docker compose up` 启动全部服务，Grafana dashboard 显示数据

---

## Milestone 4: Smart Router with Fallback & A/B Routing

**Goal**: 智能路由 — 基于 cost/latency 选择 provider，自动 fallback，A/B 测试。

### Issue 4.1: Router Interface and Strategy Pattern
- `Router` interface + `Strategy` pattern
- 策略实现: CostOptimized, LatencyOptimized, RoundRobin, Weighted, Priority
- Model groups: 定义等价模型组 (`"fast"`, `"smart"`)，客户端请求 group 名，router 选最优 provider
- 策略可按 org 配置覆盖
- **验收**: 各策略在单元测试中按预期选择 provider

### Issue 4.2: Fallback Chain with Circuit Breaker
- Circuit breaker 三态: Closed → Open (5 次连续失败) → Half-Open (30s cooldown)
- 请求失败自动 fallback 到下一个 provider，最多 3 次
- 客户端错误 (400, 401) 不触发 fallback
- Streaming 请求在第一个 byte 前可 retry，mid-stream 不 retry
- Response header: `X-LLM-Gateway-Provider`, `X-LLM-Gateway-Attempts`
- **验收**: primary 返回 503 时自动 fallback 到 secondary

### Issue 4.3: A/B Routing for Model Evaluation
- 基于 org ID hash 的确定性流量分割
- 配置化实验定义 (variants + weights)
- 实验元数据记录到每条请求日志
- Admin endpoint: `GET /internal/admin/experiments/{name}/stats`
- **验收**: 1000 请求中流量分布偏差 < 5%

---

## Milestone 5: Semantic Cache

**Goal**: 基于语义相似度的 LLM 响应缓存，节省 cost 和 latency。

### Issue 5.1: Embedding Generation Service
- `Embedder` interface，实现 OpenAI `text-embedding-3-small`
- 输入归一化: 拼接 messages → trim → truncate
- Embedding 结果 Redis 缓存 (24h TTL，key = SHA-256 of normalized text)
- **验收**: 相同输入返回相同 embedding (via cache)

### Issue 5.2: pgvector Cache Store
- `semantic_cache` 表: input_hash, embedding vector(1536), request/response JSONB, TTL
- 两级查找: 精确匹配 (input_hash) → 语义匹配 (cosine similarity ≥ 0.95)
- IVFFlat 索引加速向量搜索
- Cache eviction: TTL + max entries
- **验收**: 语义相似请求命中缓存，response header `X-Cache: HIT/MISS`

### Issue 5.3: Cache Middleware Integration
- 请求流: Auth → RateLimit → **CacheCheck** → Router → Provider → **CacheStore** → Response
- `stream: true` 和 `X-Cache-Control: no-cache` 跳过缓存
- Cache store 异步 (不阻塞响应返回)
- Prometheus metrics: cache hit/miss rate, lookup duration
- **验收**: cache miss < 10ms overhead, cache hit < 50ms 返回

---

## Milestone 6: Token Accounting & Budget Enforcement

**Goal**: 追踪每个请求的 token 用量和费用，实施组织级预算控制。

### Issue 6.1: Token Counting and Cost Calculation
- `CostCalculator`: 根据 model pricing table 计算 input/output cost
- Streaming 场景: 累积 chunk 文本 + tiktoken 计算
- Response headers: `X-Cost-USD`, `X-Tokens-Input`, `X-Tokens-Output`
- **验收**: 计算结果与手动计算一致

### Issue 6.2: Usage Tracking Database
- `usage_records` 表 (按月分区)
- 异步批量写入 (buffered channel, 100 records or 5s flush)
- Admin endpoints: 按 org/model/time 查询用量
- **验收**: 每个请求创建 usage record，graceful shutdown 不丢数据

### Issue 6.3: Budget Enforcement
- org 表扩展: monthly_budget_usd, alert_threshold, budget_action
- 预算检查 (Redis 缓存 60s TTL): 超预算 → 402, 接近预算 → warning header
- Budget action 可配置: warn / throttle / block
- **验收**: 超预算请求返回 402，包含 current spend / budget / reset date

---

## Milestone 7: Async Pipeline (Kafka)

**Goal**: 用 Kafka 解耦 usage logging 和 analytics，支持实时分析。

### Issue 7.1: Kafka Producer for Usage Events
- 每个请求完成后发布到 `llm-gateway.usage` topic
- 异步 fire-and-forget + 缓冲，Kafka 故障不阻塞请求
- Topics: usage, errors, audit
- **验收**: 事件可靠发布，graceful shutdown flush

### Issue 7.2: Kafka Consumer for Usage Persistence
- Consumer group 消费 → 批量写入 PostgreSQL + ClickHouse
- At-least-once + idempotent insert (request_id 去重)
- Dead letter topic 处理失败事件
- Docker Compose 添加 Kafka (KRaft) + ClickHouse
- **验收**: 事件在 5s 内写入两个数据库

### Issue 7.3: Analytics API from ClickHouse
- Admin endpoints: usage over time, cost breakdown, latency percentiles, top models
- 支持 granularity (1m, 5m, 1h, 1d) 和 period 参数
- **验收**: 查询 < 500ms (百万级记录)

---

## Milestone 8: Streaming Enhancements

**Goal**: 增强 SSE streaming — 实时 token 计数，streaming 限流，断连处理。

### Issue 8.1: Mid-Stream Token Counting
- 边 stream 边估算 token 数 (字符数估算 + 最终修正)
- 在 `[DONE]` 前注入 usage event chunk
- 支持预算超限时中止 stream
- **验收**: 实时 token count 误差 < 10%

### Issue 8.2: Streaming-Aware Rate Limiting
- Streaming 期间周期性更新 Redis TPM counter (每 50 tokens 或 5s)
- 当前 streams 占用大部分 TPM 时拒绝新请求
- 进行中的 stream 不中断 (graceful)
- **验收**: TPM 在 streaming 期间实时更新

### Issue 8.3: Client Disconnect Handling
- `r.Context().Done()` 检测断连 → 取消上游请求
- 记录 partial usage
- Slow client 缓冲 100 chunks 后断开
- Provider 30s 无 chunk 超时
- Graceful shutdown drain active streams
- **验收**: 断连后无 goroutine 泄漏

---

## Milestone 9: Self-Hosted Model Support

**Goal**: 支持 vLLM / Ollama 等自部署模型，混合 cloud + self-hosted routing。

### Issue 9.1: Self-Hosted Provider Adapter
- OpenAI-compatible API 透传 (自部署服务器多数兼容)
- Per-endpoint 并发控制 (semaphore，保护 GPU 容量)
- Health check via `/health` 或 `/v1/models`
- Token counting fallback to tiktoken
- **验收**: streaming + non-streaming 均正常工作

### Issue 9.2: GPU-Aware Routing
- 多个 self-hosted endpoint 按负载路由 (active requests, queue depth)
- Self-hosted 容量耗尽时 overflow 到 cloud provider
- **验收**: 自动 overflow + dashboard 展示 self-hosted vs cloud 分布

---

## Milestone 10: Production Hardening

**Goal**: 生产级部署、安全加固、负载测试、运维文档。

### Issue 10.1: Kubernetes Deployment
- Deployment + HPA + PDB + Network Policies
- Kustomize overlays (dev/prod)
- Readiness/liveness probes, resource limits
- **验收**: `kubectl apply` 部署，HPA 正常扩缩容

### Issue 10.2: Load Testing Suite
- k6 脚本: baseline, streaming, cache, failover, rate limiting, budget
- 目标: P99 < 100ms gateway overhead @ 1000 RPS
- **验收**: 所有场景测试通过并产出报告

### Issue 10.3: Security Hardening
- API key 不存明文不记日志，admin endpoint 独立认证
- Request body size limit (10MB), SQL injection 防护
- TLS, govulncheck
- **验收**: 零 high/critical 漏洞

### Issue 10.4: Operational Documentation
- 架构文档、部署指南、运维 runbook、OpenAPI spec
- **验收**: 新人可仅凭文档完成部署和运维

---

## Issue Dependency Graph

```
M1: 1.1 → 1.2 → 1.3 → 1.4 → 1.5

M2: 1.3 → 2.1 (Anthropic)
    1.3 → 2.2 (Google)
    2.1 + 2.2 → 2.3 → 2.4

M3: 1.4 → 3.1 → 3.2
    1.4 → 3.3
    3.1 → 3.4
    3.1 + 3.2 + 3.3 → 3.5

M4: 2.3 + 2.4 → 4.1 → 4.2
    4.1 + 3.4 → 4.3

M5: 3.5 → 5.1 → 5.2 → 5.3

M6: 1.2 → 6.1 → 6.2 → 6.3

M7: 6.1 → 7.1 → 7.2 → 7.3

M8: 1.4 + 6.1 → 8.1 → 8.2, 8.3

M9: 1.3 + 4.2 → 9.1 → 9.2

M10: All → 10.1, 10.2, 10.3, 10.4
```

## Parallelization

M1 完成后，M2 / M3 / M6 可并行推进：
- **Track A**: M2 → M4 → M9 (provider + routing)
- **Track B**: M3 → M5 (auth + cache)
- **Track C**: M6 → M7 (cost + pipeline)
- **Shared**: M8 (streaming, 依赖 M3 + M6), M10 (全部完成后)

## Key Technical Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| HTTP router | chi | stdlib 兼容，middleware 组合灵活 |
| Config | koanf | API 比 viper 更简洁，支持 env overlay |
| Kafka client | segmentio/kafka-go | 纯 Go，无 CGo 依赖 |
| DB migration | golang-migrate | 社区广泛采用 |
| Logging | slog (stdlib) | 零依赖，结构化，高性能 |
| HTTP client | net/http | 完全控制 streaming / timeout / cancel |
| Vector DB | pgvector | 已用 PostgreSQL，避免引入新数据库 |
| Analytics DB | ClickHouse | 专为时序聚合查询优化 |
| Token counting | tiktoken-go | OpenAI 模型精确，其他模型合理近似 |

## Verification Strategy

每个 Milestone 完成后:
1. `make test` — 全部单元测试通过
2. `make lint` — 零 lint 错误
3. `docker compose up` — 端到端验证
4. 对应 Milestone 的集成测试/负载测试通过
5. CI pipeline green
