#!/bin/sh
# Loops each sample clip into MediaMTX as rtsp://<host>:8554/<clip name>.
# Clips are pre-encoded H.264 with a 1 s GOP, so publishing is a pure remux
# (-c copy) and costs almost no CPU. Each publisher restarts if it exits.
set -eu

RTSP_HOST="${RTSP_HOST:-127.0.0.1:8554}"
SAMPLES_DIR="${SAMPLES_DIR:-$(dirname "$0")/samples}"

for clip in "$SAMPLES_DIR"/*.mp4; do
  name="$(basename "$clip" .mp4)"
  (
    while true; do
      ffmpeg -hide_banner -loglevel error -re -stream_loop -1 -i "$clip" \
        -c copy -f rtsp -rtsp_transport tcp "rtsp://$RTSP_HOST/$name" || true
      echo "publisher $name exited, restarting in 2s" >&2
      sleep 2
    done
  ) &
done
wait
