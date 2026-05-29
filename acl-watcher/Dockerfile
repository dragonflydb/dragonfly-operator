FROM golang:1.25-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./

ARG TARGETOS
ARG TARGETARCH

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \\
    go build -trimpath -ldflags="-s -w" -o /out/acl-watcher ./main.go

FROM gcr.io/distroless/static:nonroot

COPY --from=builder /out/acl-watcher /acl-watcher

USER nonroot:nonroot

ENTRYPOINT ["/acl-watcher"]
