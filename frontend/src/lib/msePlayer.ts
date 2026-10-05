/**
 * Plays a live stream of fragmented MP4 delivered over a WebSocket through
 * Media Source Extensions. See backend/internal/api/ws.go for the protocol.
 *
 * Live playback rules:
 *  - Only the last few seconds are kept in the SourceBuffer; older data is
 *    removed so memory stays flat however long the tile is open.
 *  - Playback trails the newest data by about one segment. Small drift is
 *    absorbed by playing slightly faster; large lag (tab in background,
 *    segments skipped by the server for a slow connection) jumps forward.
 *  - Pause closes the socket. Play opens a fresh one and starts at the live
 *    edge; resuming stale video makes no sense for a camera feed.
 */

export type PlayerStatus =
  | { state: "connecting"; message?: string }
  | { state: "live" }
  | { state: "reconnecting"; message: string; retryInMs?: number }
  | { state: "failed"; message: string }
  | { state: "paused" };

type ServerMessage =
  | { type: "status"; state: "connecting" | "live" | "reconnecting" | "failed"; message?: string }
  | { type: "init"; codec: string }
  | { type: "error"; message: string };

/** Close codes the server uses for errors that retrying cannot fix. */
const PERMANENT_CLOSE_CODES = new Set([4400]);

const KEEP_BEHIND_S = 10; // buffer kept behind the playhead
// Segments are one GOP (about 1 s) long and arrive whole, so the buffer
// ahead of the playhead swings between ~0 and ~1 s. Playing closer to the
// edge than one segment plus some jitter margin causes stalls.
const TARGET_LATENCY_S = 1.2;
const CATCH_UP_LATENCY_S = 1.8; // above this, play slightly faster
const CATCH_UP_RATE = 1.08;
const MAX_LATENCY_S = 4; // above this, jump straight to the target
const CHASE_INTERVAL_MS = 1000;
const RECONNECT_MIN_MS = 1000;
const RECONNECT_MAX_MS = 15000;

type MediaSourceCtor = typeof MediaSource;

function mediaSourceCtor(): MediaSourceCtor | undefined {
  // iOS Safari only exposes ManagedMediaSource.
  const w = window as unknown as { ManagedMediaSource?: MediaSourceCtor; MediaSource?: MediaSourceCtor };
  return w.MediaSource ?? w.ManagedMediaSource;
}

export function isMseSupported(): boolean {
  return mediaSourceCtor() !== undefined;
}

export class MsePlayer {
  private ws: WebSocket | null = null;
  private mediaSource: MediaSource | null = null;
  private objectUrl: string | null = null;
  private sourceBuffer: SourceBuffer | null = null;
  private queue: ArrayBuffer[] = [];
  /** Error text the server sent just before closing the socket. */
  private serverError: string | null = null;
  private reconnectTimer: number | undefined;
  private chaseTimer: number | undefined;
  private reconnectDelay = RECONNECT_MIN_MS;
  private stopped = true;
  private failed = false;

  constructor(
    private readonly video: HTMLVideoElement,
    private readonly socketUrl: string,
    private readonly onStatus: (s: PlayerStatus) => void,
  ) {
    video.muted = true;
    video.playsInline = true;
    // Required for ManagedMediaSource on iOS.
    (video as HTMLVideoElement & { disableRemotePlayback: boolean }).disableRemotePlayback = true;
  }

  /** Connects (or reconnects) and plays from the live edge. */
  play(): void {
    if (!mediaSourceCtor()) {
      this.fail("This browser cannot play live video (Media Source Extensions unavailable).");
      return;
    }
    this.stopped = false;
    this.failed = false;
    this.reconnectDelay = RECONNECT_MIN_MS;
    this.connect();
    this.chaseTimer ??= window.setInterval(() => this.chaseLiveEdge(), CHASE_INTERVAL_MS);
  }

  /** Stops receiving video but keeps the last frame on screen. */
  pause(): void {
    this.stopped = true;
    this.closeSocket();
    this.video.pause();
    this.onStatus({ state: "paused" });
  }

  /** Releases every resource. The player cannot be used afterwards. */
  destroy(): void {
    this.stopped = true;
    this.closeSocket();
    window.clearInterval(this.chaseTimer);
    this.chaseTimer = undefined;
    this.resetMedia();
  }

  private connect(): void {
    this.closeSocket();
    this.onStatus({ state: "connecting" });
    this.serverError = null;
    const ws = new WebSocket(this.socketUrl);
    ws.binaryType = "arraybuffer";
    this.ws = ws;

    ws.onmessage = (ev) => {
      if (this.ws !== ws) return;
      if (typeof ev.data === "string") this.handleControl(ev.data);
      else this.handleBinary(ev.data as ArrayBuffer);
    };
    ws.onclose = (ev) => {
      if (this.ws !== ws) return;
      this.ws = null;
      if (this.stopped || this.failed) return;
      const message = this.serverError || ev.reason;
      if (PERMANENT_CLOSE_CODES.has(ev.code)) {
        this.fail(message || "The server rejected this stream.");
        return;
      }
      this.scheduleReconnect(message || "Lost connection to the server");
    };
  }

  private closeSocket(): void {
    window.clearTimeout(this.reconnectTimer);
    this.reconnectTimer = undefined;
    const ws = this.ws;
    this.ws = null;
    if (ws && ws.readyState <= WebSocket.OPEN) ws.close(1000);
  }

  private scheduleReconnect(message: string): void {
    const delay = this.reconnectDelay;
    this.reconnectDelay = Math.min(delay * 2, RECONNECT_MAX_MS);
    this.onStatus({ state: "reconnecting", message, retryInMs: delay });
    this.reconnectTimer = window.setTimeout(() => {
      if (!this.stopped) this.connect();
    }, delay);
  }

  private fail(message: string): void {
    this.failed = true;
    this.closeSocket();
    this.onStatus({ state: "failed", message });
  }

  private handleControl(raw: string): void {
    let msg: ServerMessage;
    try {
      msg = JSON.parse(raw) as ServerMessage;
    } catch {
      return;
    }
    switch (msg.type) {
      case "status":
        if (msg.state === "live") {
          this.reconnectDelay = RECONNECT_MIN_MS;
          this.onStatus({ state: "live" });
        } else if (msg.state === "failed") {
          this.fail(msg.message ?? "Stream failed");
        } else if (msg.state === "reconnecting") {
          // The server is retrying the camera; keep the socket open.
          this.onStatus({ state: "reconnecting", message: msg.message ?? "Camera unavailable" });
        } else {
          this.onStatus({ state: "connecting", message: msg.message });
        }
        break;
      case "init":
        this.startMedia(msg.codec);
        break;
      case "error":
        // The close event follows; it reports this message.
        this.serverError = msg.message;
        break;
    }
  }

  /**
   * Every init message means a new decoding session (first connect, or the
   * server restarted ffmpeg, which resets timestamps and may change the
   * resolution), so the MediaSource is rebuilt from scratch.
   */
  private startMedia(codec: string): void {
    const Ctor = mediaSourceCtor()!;
    const mime = `video/mp4; codecs="${codec}"`;
    if (!Ctor.isTypeSupported(mime)) {
      this.fail(`This browser cannot decode the stream's video format (${codec}).`);
      return;
    }
    this.resetMedia();

    const ms = new Ctor();
    this.mediaSource = ms;
    this.objectUrl = URL.createObjectURL(ms);
    this.video.src = this.objectUrl;
    ms.addEventListener(
      "sourceopen",
      () => {
        if (this.mediaSource !== ms) return;
        try {
          const sb = ms.addSourceBuffer(mime);
          sb.mode = "segments";
          sb.addEventListener("updateend", () => this.onUpdateEnd(sb));
          sb.addEventListener("error", () => {
            if (sb === this.sourceBuffer) this.recover("Video decoder error");
          });
          this.sourceBuffer = sb;
          this.appendNext();
        } catch (err) {
          this.fail(`Could not start the video decoder: ${(err as Error).message}`);
        }
      },
      { once: true },
    );
  }

  private handleBinary(data: ArrayBuffer): void {
    if (!this.mediaSource) return; // media before init: nothing can decode it
    this.queue.push(data);
    // Bound the queue if the decoder stalls; old segments are useless live.
    if (this.queue.length > 30) this.queue.splice(1, this.queue.length - 10);
    this.appendNext();
  }

  private appendNext(): void {
    const sb = this.sourceBuffer;
    if (!sb || sb.updating || this.queue.length === 0) return;
    if (this.mediaSource?.readyState !== "open") return;
    const next = this.queue[0];
    try {
      sb.appendBuffer(next);
      this.queue.shift();
    } catch (err) {
      if ((err as DOMException).name === "QuotaExceededError") {
        // Free space and retry on updateend. If nothing can be removed
        // (playhead not advancing), no updateend will come: start over.
        if (!this.trim(true)) this.recover("Video buffer full");
      } else {
        this.recover("Video decoder error");
      }
    }
  }

  private onUpdateEnd(sb: SourceBuffer): void {
    if (sb !== this.sourceBuffer) return;
    if (this.video.paused && !this.stopped && sb.buffered.length > 0) {
      this.chaseLiveEdge();
      this.video.play().catch(() => {
        /* autoplay of muted video is allowed; ignore races with pause() */
      });
    }
    if (!this.trim(false)) this.appendNext();
  }

  /** Removes old buffered data. Returns true if a removal was started. */
  private trim(aggressive: boolean): boolean {
    const sb = this.sourceBuffer;
    if (!sb || sb.updating || sb.buffered.length === 0) return false;
    const start = sb.buffered.start(0);
    const keep = aggressive ? 1 : KEEP_BEHIND_S;
    const cutoff = this.video.currentTime - keep;
    if (cutoff - start < 1) return false;
    sb.remove(start, cutoff);
    return true;
  }

  /** Keeps playback near the newest buffered frame and skips gaps. */
  private chaseLiveEdge(): void {
    const sb = this.sourceBuffer;
    if (!sb || this.stopped || sb.buffered.length === 0) return;
    const ranges = sb.buffered;
    const end = ranges.end(ranges.length - 1);
    const t = this.video.currentTime;
    let inRange = false;
    for (let i = 0; i < ranges.length; i++) {
      if (t >= ranges.start(i) && t <= ranges.end(i)) inRange = true;
    }
    const lag = end - t;
    if (!inRange || lag > MAX_LATENCY_S) {
      const lastStart = ranges.start(ranges.length - 1);
      this.video.currentTime = Math.max(lastStart, end - TARGET_LATENCY_S);
      this.video.playbackRate = 1;
    } else {
      this.video.playbackRate = lag > CATCH_UP_LATENCY_S ? CATCH_UP_RATE : 1;
    }
  }

  /** Rebuilds the decoder by reconnecting; the server resends init. */
  private recover(message: string): void {
    if (this.stopped || this.failed) return;
    this.resetMedia();
    this.closeSocket();
    this.scheduleReconnect(message);
  }

  private resetMedia(): void {
    this.queue = [];
    this.sourceBuffer = null;
    const ms = this.mediaSource;
    this.mediaSource = null;
    if (ms && ms.readyState === "open") {
      try {
        ms.endOfStream();
      } catch {
        /* already ending */
      }
    }
    if (this.objectUrl) {
      URL.revokeObjectURL(this.objectUrl);
      this.objectUrl = null;
    }
    this.video.removeAttribute("src");
    this.video.load();
  }
}
