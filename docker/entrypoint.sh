#!/bin/sh
# Starts the demo RTSP server and publishers (unless disabled), then runs
# the API server in the foreground so it receives signals from tini.
set -eu

if [ "${ENABLE_DEMO:-true}" = "true" ]; then
  /app/mediamtx /app/demo/mediamtx.yml &
  RTSP_HOST=127.0.0.1:8554 SAMPLES_DIR=/app/demo/samples /app/demo/publish.sh &
else
  unset DEMO_STREAMS
fi

exec /app/server
