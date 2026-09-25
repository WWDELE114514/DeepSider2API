FROM golang:1.23-alpine AS build
WORKDIR /src
ENV GOPROXY=https://goproxy.cn,direct
COPY . .
# go.sum is generated on first build (no vendor directory is committed).
RUN go mod tidy
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/deepsider2api ./cmd/server

FROM alpine:3.20
RUN sed -i 's|dl-cdn.alpinelinux.org|mirrors.aliyun.com|g' /etc/apk/repositories \
 && apk add --no-cache wget ca-certificates tzdata \
 && adduser -D -u 10001 app \
 && mkdir -p /app/data /app/auths \
 && chown -R app:app /app
WORKDIR /app
COPY --from=build /out/deepsider2api /app/deepsider2api
COPY config.example.json /app/config.json
USER app
EXPOSE 7863
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:7863/healthz || exit 1
ENTRYPOINT ["/app/deepsider2api", "-config", "/app/config.json"]
