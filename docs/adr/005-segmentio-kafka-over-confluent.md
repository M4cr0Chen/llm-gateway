# ADR-005: Use segmentio/kafka-go over confluent-kafka-go

## Status
Accepted

## Context
Need a Kafka client library for publishing usage events and consuming them for analytics. Two main options in Go: segmentio/kafka-go (pure Go) and confluent-kafka-go (CGo wrapper around librdkafka).

## Decision
Use [segmentio/kafka-go](https://github.com/segmentio/kafka-go).

## Rationale
- Pure Go implementation — no CGo dependency
- Simpler cross-compilation and Docker builds (no need for librdkafka C library)
- Clean, idiomatic Go API
- Good performance for our throughput needs (thousands of events/sec, not millions)
- Simpler dependency management — `go get` just works
- Supports KRaft mode (no Zookeeper dependency)

## Consequences
- Some advanced Kafka features may not be available (e.g., exactly-once semantics via transactions)
- For our use case (at-least-once delivery with idempotent consumers), this is sufficient
- If we ever need transactional Kafka operations, we'd need to revisit
- Reader and Writer APIs are straightforward:
  ```go
  writer := kafka.Writer{Addr: kafka.TCP("localhost:9092"), Topic: "events"}
  reader := kafka.NewReader(kafka.ReaderConfig{Brokers: []string{"localhost:9092"}, GroupID: "consumer-group", Topic: "events"})
  ```

## Alternatives Rejected

### confluent-kafka-go
- Wraps librdkafka (C library) via CGo
- Higher performance for extreme throughput scenarios
- But: CGo complicates builds, Docker images need C toolchain, cross-compilation is harder
- Overkill for our event volume (< 10K events/sec)
- More complex API surface

### shopify/sarama
- Previously the most popular Go Kafka client
- Has been superseded by segmentio/kafka-go in many projects
- More complex API, harder to use correctly
- IBM/sarama (fork) is maintained but community has shifted
