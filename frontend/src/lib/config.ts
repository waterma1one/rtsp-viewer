const fromEnv = (import.meta.env.VITE_API_URL as string | undefined)?.trim();

/** Base URL of the Go backend, without a trailing slash. */
export const API_URL = (fromEnv || "http://localhost:8080").replace(/\/+$/, "");

/** WebSocket URL that streams one RTSP source. */
export function streamSocketUrl(rtspUrl: string): string {
  const ws = API_URL.replace(/^http/, "ws");
  return `${ws}/ws?url=${encodeURIComponent(rtspUrl)}`;
}
