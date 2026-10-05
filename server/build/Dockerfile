# 阶段 1：构建前端
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# 阶段 2：构建后端
FROM golang:1.23-alpine AS build
RUN apk add --no-cache protobuf-dev
WORKDIR /src

# 先只拷贝依赖清单，利用 Docker 层缓存
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
# 前端产物拷入后由 embed.FS 内嵌进二进制
COPY --from=web /web/dist ./web/dist

# CGO_ENABLED=0 → 纯静态二进制，方便迁移
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w" \
    -o /out/probeone ./cmd/probeone

# 阶段 3：运行
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget && \
    adduser -D -u 10001 probeone && \
    mkdir -p /data && chown probeone:probeone /data
COPY --from=build /out/probeone /usr/local/bin/probeone
USER probeone
EXPOSE 8000 8008
VOLUME /data
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8000/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/probeone"]
