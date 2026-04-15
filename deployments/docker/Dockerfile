FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /gateway ./cmd/gateway

FROM alpine:latest
RUN apk --no-cache add ca-certificates
COPY --from=builder /gateway /gateway
COPY configs/ /configs/
ENV GATEWAY_CONFIG_PATH=/configs/gateway.yaml
EXPOSE 8080
ENTRYPOINT ["/gateway"]
