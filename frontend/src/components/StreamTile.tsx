import { useEffect, useRef, useState } from "react";
import { MsePlayer, type PlayerStatus } from "../lib/msePlayer";
import { streamSocketUrl } from "../lib/config";
import { displayUrl } from "../lib/api";
import type { StreamEntry } from "../hooks/useStreamList";
import { Icon } from "./Icon";
import styles from "./StreamTile.module.css";

interface Props {
  stream: StreamEntry;
  onRemove: (id: string) => void;
}

const STATUS_TEXT: Record<PlayerStatus["state"], string> = {
  connecting: "Connecting",
  live: "Live",
  reconnecting: "Reconnecting",
  failed: "Not available",
  paused: "Paused",
};

export function StreamTile({ stream, onRemove }: Props) {
  const tileRef = useRef<HTMLElement>(null);
  const videoRef = useRef<HTMLVideoElement>(null);
  const playerRef = useRef<MsePlayer | null>(null);
  const [status, setStatus] = useState<PlayerStatus>({ state: "connecting" });
  const [hasPicture, setHasPicture] = useState(false);
  const liveSince = useLiveSince(status.state === "live");

  useEffect(() => {
    const video = videoRef.current!;
    const player = new MsePlayer(video, streamSocketUrl(stream.url), setStatus);
    playerRef.current = player;
    const onFrame = () => setHasPicture(true);
    const onEmptied = () => setHasPicture(false);
    video.addEventListener("loadeddata", onFrame);
    video.addEventListener("emptied", onEmptied);
    player.play();
    return () => {
      video.removeEventListener("loadeddata", onFrame);
      video.removeEventListener("emptied", onEmptied);
      player.destroy();
      playerRef.current = null;
    };
  }, [stream.url]);

  const paused = status.state === "paused";
  const failed = status.state === "failed";

  const togglePlay = () => {
    const p = playerRef.current;
    if (!p) return;
    if (paused || failed) p.play();
    else p.pause();
  };

  const toggleFullscreen = () => {
    const el = tileRef.current;
    if (!el) return;
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
    else el.requestFullscreen?.().catch(() => {});
  };

  const message = "message" in status ? status.message : undefined;
  const showCurtain = !hasPicture || failed;

  return (
    <article
      ref={tileRef}
      className={styles.tile}
      data-state={status.state}
      aria-label={`${stream.name}, ${STATUS_TEXT[status.state].toLowerCase()}`}
    >
      <div className={styles.tally} aria-hidden="true" />
      <div className={styles.screen}>
        <video ref={videoRef} className={styles.video} muted playsInline onDoubleClick={toggleFullscreen} />
        {showCurtain && (
          <div className={styles.curtain}>
            <p className={styles.curtainTitle}>{curtainTitle(status)}</p>
            {message && <p className={styles.curtainDetail}>{message}</p>}
            {failed && (
              <button type="button" className={styles.retry} onClick={togglePlay}>
                Try again
              </button>
            )}
          </div>
        )}
        {!showCurtain && status.state === "reconnecting" && (
          <p className={styles.notice} role="status">
            {message ?? "Reconnecting"}
          </p>
        )}
      </div>

      <footer className={styles.bar}>
        <div className={styles.identity}>
          <h2 className={styles.name} title={displayUrl(stream.url)}>
            {stream.name}
          </h2>
          <p className={styles.state} aria-live="polite">
            <span className={styles.dot} aria-hidden="true" />
            {STATUS_TEXT[status.state]}
            {liveSince !== null && <span className={styles.clock}>{formatDuration(liveSince)}</span>}
          </p>
        </div>
        <div className={styles.controls}>
          <button
            type="button"
            className={styles.control}
            onClick={togglePlay}
            aria-label={paused || failed ? `Play ${stream.name}` : `Pause ${stream.name}`}
            title={paused || failed ? "Play" : "Pause"}
          >
            <Icon name={paused || failed ? "play" : "pause"} />
          </button>
          <button
            type="button"
            className={styles.control}
            onClick={toggleFullscreen}
            aria-label={`Show ${stream.name} full screen`}
            title="Full screen"
          >
            <Icon name="expand" />
          </button>
          <button
            type="button"
            className={styles.control}
            onClick={() => onRemove(stream.id)}
            aria-label={`Remove ${stream.name}`}
            title="Remove"
          >
            <Icon name="close" />
          </button>
        </div>
      </footer>
    </article>
  );
}

function curtainTitle(status: PlayerStatus): string {
  switch (status.state) {
    case "connecting":
      return "Connecting to camera";
    case "reconnecting":
      return "Waiting for camera";
    case "failed":
      return "Can't play this stream";
    case "paused":
      return "Paused";
    case "live":
      return "Starting video";
  }
}

/** Seconds since the stream went live, ticking once a second; null otherwise. */
function useLiveSince(live: boolean): number | null {
  const [since, setSince] = useState<number | null>(null);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!live) {
      setSince(null);
      return;
    }
    const start = Date.now();
    setSince(start);
    setNow(start);
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, [live]);
  return since === null ? null : Math.max(0, Math.floor((now - since) / 1000));
}

function formatDuration(totalSeconds: number): string {
  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = totalSeconds % 60;
  const mm = String(m).padStart(2, "0");
  const ss = String(s).padStart(2, "0");
  return h > 0 ? `${h}:${mm}:${ss}` : `${mm}:${ss}`;
}
