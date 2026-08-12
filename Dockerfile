FROM golang:1.25-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
COPY vendor ./vendor
# 使用 vendored 依赖, 无需网络下载 (x/net 用于 socks5)
RUN go mod download 2>/dev/null || true

COPY *.go webui.html webui.js ./

RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -ldflags="-s -w" -trimpath -o /FreebuffProxy .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /FreebuffProxy /usr/local/bin/FreebuffProxy
RUN ln -s /usr/local/bin/FreebuffProxy
# Expose proxy port
EXPOSE 16880

ENTRYPOINT ["FreebuffProxy"]
