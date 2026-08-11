FROM golang:1.23-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY *.go webui.html webui.js ./

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -trimpath -o /FreebuffProxy .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /FreebuffProxy /usr/local/bin/FreebuffProxy
RUN ln -s /usr/local/bin/FreebuffProxy
# Expose proxy port
EXPOSE 16880

ENTRYPOINT ["FreebuffProxy"]
