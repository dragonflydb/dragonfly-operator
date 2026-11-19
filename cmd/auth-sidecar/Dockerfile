FROM golang:1.27-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/auth-sidecar/ ./cmd/auth-sidecar/
RUN go test ./cmd/auth-sidecar && CGO_ENABLED=0 go build -trimpath -o /auth-sidecar ./cmd/auth-sidecar

FROM alpine:3.24
RUN apk add --no-cache ca-certificates
COPY --from=builder /auth-sidecar /usr/local/bin/auth-sidecar
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/auth-sidecar"]
