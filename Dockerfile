# Backend image: Go server + ffmpeg, plus a loopback-only MediaMTX that
# serves the bundled demo streams (set ENABLE_DEMO=false to omit them).

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM bluenviron/mediamtx:1.21.1 AS mediamtx

FROM alpine:3.22
RUN apk add --no-cache ffmpeg tini \
 && adduser -D -H -u 10001 app
WORKDIR /app
COPY --from=build /out/server /app/server
COPY --from=mediamtx /mediamtx /app/mediamtx
COPY demo/ /app/demo/
COPY docker/entrypoint.sh /app/entrypoint.sh

ENV PORT=8080 \
    ENABLE_DEMO=true \
    ALLOWED_RTSP_HOSTS=127.0.0.1:8554 \
    DEMO_STREAMS="Test pattern=rtsp://127.0.0.1:8554/cam1,Color bars=rtsp://127.0.0.1:8554/cam2,Mandelbrot=rtsp://127.0.0.1:8554/cam3"

USER app
EXPOSE 8080
ENTRYPOINT ["/sbin/tini", "--", "/app/entrypoint.sh"]
