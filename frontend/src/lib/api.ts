import { API_URL } from "./config";

export interface DemoStream {
  name: string;
  url: string;
}

export type ValidateResult = { ok: true; url: string } | { ok: false; error: string };

/** Asks the backend whether it will accept this RTSP URL. */
export async function validateStreamUrl(url: string, signal?: AbortSignal): Promise<ValidateResult> {
  let res: Response;
  try {
    res = await fetch(`${API_URL}/api/validate?url=${encodeURIComponent(url)}`, { signal });
  } catch (err) {
    if ((err as Error).name === "AbortError") throw err;
    return { ok: false, error: "Can't reach the server. Check your connection and try again." };
  }
  const body = (await res.json().catch(() => ({}))) as { url?: string; error?: string };
  if (res.ok && body.url) return { ok: true, url: body.url };
  return { ok: false, error: body.error ?? `Server returned ${res.status}` };
}

export async function fetchDemoStreams(signal?: AbortSignal): Promise<DemoStream[]> {
  const res = await fetch(`${API_URL}/api/demo-streams`, { signal });
  if (!res.ok) throw new Error(`Server returned ${res.status}`);
  return (await res.json()) as DemoStream[];
}

export async function checkHealth(signal?: AbortSignal): Promise<boolean> {
  try {
    const res = await fetch(`${API_URL}/healthz`, { signal, cache: "no-store" });
    return res.ok;
  } catch {
    return false;
  }
}

/** Quick client-side check so obvious typos never reach the server. */
export function quickCheckUrl(raw: string): string | null {
  const value = raw.trim();
  if (!value) return "Enter an RTSP URL.";
  if (!/^rtsps?:\/\//i.test(value)) return "The URL must start with rtsp:// or rtsps://";
  try {
    const u = new URL(value);
    if (!u.hostname) return "The URL is missing a host name.";
  } catch {
    return "That doesn't look like a valid URL.";
  }
  return null;
}

/** A readable default name: "camera.local / live". Credentials are dropped. */
export function nameFromUrl(raw: string): string {
  try {
    const u = new URL(raw);
    const path = u.pathname.split("/").filter(Boolean).pop();
    return path ? `${u.hostname} / ${decodeURIComponent(path)}` : u.hostname;
  } catch {
    return raw;
  }
}

/** Hides the password in a URL shown on screen. */
export function displayUrl(raw: string): string {
  try {
    const u = new URL(raw);
    if (u.password) u.password = "•••";
    return u.toString();
  } catch {
    return raw;
  }
}
