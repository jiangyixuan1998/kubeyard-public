# KubeYard 容器镜像：三阶段构建（前端 → 后端 → 运行时）
# 构建上下文为仓库根目录：docker build -t kubeyard:latest .

# ---------- 阶段 1：构建前端 ----------
FROM node:20-alpine AS web

RUN npm config set registry https://registry.npmmirror.com

WORKDIR /web

COPY web/package.json web/package-lock.json ./

RUN npm install --no-audit --no-fund

COPY web/ ./

RUN npm exec vite build
# ---------- 阶段 2：构建后端 ----------
FROM golang:1.26-alpine AS server
ENV GOPROXY=https://goproxy.cn,direct \
    GO111MODULE=on \
    CGO_ENABLED=0
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/kubeyard .

# ---------- 阶段 3：运行时 ----------
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
ENV TZ=Asia/Shanghai \
    KUBEYARD_WEB_DIST=/app/web/dist \
    KUBEYARD_DATA_DIR=/app/data
WORKDIR /app
COPY --from=server /out/kubeyard /app/kubeyard
COPY --from=web /web/dist /app/web/dist
# data 目录：终端录像等落盘内容，建议挂载持久卷
VOLUME ["/app/data"]
EXPOSE 8000
ENTRYPOINT ["/app/kubeyard"]