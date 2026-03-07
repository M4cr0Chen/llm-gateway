# ADR-003: Use pgvector over a dedicated vector database

## Status
Accepted

## Context
Semantic cache requires vector similarity search to find cached responses for semantically similar prompts. Need to choose between adding pgvector extension to our existing PostgreSQL or deploying a separate vector database (Pinecone, Milvus, Qdrant, Weaviate).

## Decision
Use [pgvector](https://github.com/pgvector/pgvector) extension in PostgreSQL.

## Rationale
- PostgreSQL is already in our stack for API keys and usage records — no new infrastructure to deploy and maintain
- pgvector supports cosine similarity, L2 distance, and inner product
- IVFFlat and HNSW indexing provide good performance up to millions of vectors
- Can combine vector search with SQL filters in a single query (e.g., filter by model, check TTL)
- Transactional guarantees — cache writes are atomic with metadata updates
- Simpler operational story: one database to backup, monitor, and scale
- Our cache size is expected to be < 1M entries, well within pgvector's sweet spot

## Consequences
- Must use PostgreSQL 16+ with pgvector extension installed
- Docker image: `pgvector/pgvector:pg16` instead of standard postgres
- Vector dimension fixed at 1536 (text-embedding-3-small) — changing embedding model requires migration
- IVFFlat index requires `lists` parameter tuning based on data size (start with 100, increase as data grows)
- For >10M vectors, may need to revisit and consider dedicated vector DB

## Alternatives Rejected

### Pinecone
- Managed service — adds external dependency and cost
- Overkill for a cache with < 1M entries
- Adds network latency for every cache lookup

### Milvus / Qdrant / Weaviate
- Powerful but adds operational complexity (another service to deploy, monitor, backup)
- Justified for search-focused applications with billions of vectors
- Not justified for our use case (cache, < 1M entries, combined with SQL queries)

### Redis VSS (Vector Similarity Search)
- Redis already in our stack, would avoid adding vector search to PostgreSQL
- However: less mature than pgvector, limited query capabilities, harder to combine with metadata filters
- Redis data is ephemeral by default — not ideal for a cache that should survive restarts
