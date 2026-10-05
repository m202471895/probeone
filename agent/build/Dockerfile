FROM alpine:3.20
RUN apk add --no-cache ca-certificates && \
    adduser -D -u 10001 probeone
COPY probeone-agent /usr/local/bin/probeone-agent
USER probeone
ENTRYPOINT ["/usr/local/bin/probeone-agent"]
# 注意：Agent 镜像不需要 root，也不需要挂载 docker.sock。
# 它只读采集本机指标并外连服务端，能力边界见 PRD 1.1。
