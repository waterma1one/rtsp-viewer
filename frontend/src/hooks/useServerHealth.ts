import { useEffect, useState } from "react";
import { checkHealth } from "../lib/api";

export type ServerHealth = "checking" | "up" | "waking" | "down";

const SLOW_MS = 2500; // a healthy server answers well within this
const RETRY_MS = 3000;
const GIVE_UP_MS = 120_000; // Render free instances take about a minute to wake

/**
 * Tracks whether the backend is reachable. Free hosting tiers sleep when
 * idle, so a slow first answer is reported as "waking" rather than an error.
 */
export function useServerHealth(): ServerHealth {
  const [health, setHealth] = useState<ServerHealth>("checking");

  useEffect(() => {
    const ctrl = new AbortController();
    const started = Date.now();
    let slowTimer: number | undefined;
    let retryTimer: number | undefined;

    const attempt = async () => {
      slowTimer = window.setTimeout(() => setHealth((h) => (h === "checking" ? "waking" : h)), SLOW_MS);
      const ok = await checkHealth(ctrl.signal);
      window.clearTimeout(slowTimer);
      if (ctrl.signal.aborted) return;
      if (ok) {
        setHealth("up");
        return;
      }
      if (Date.now() - started > GIVE_UP_MS) {
        setHealth("down");
        return;
      }
      setHealth("waking");
      retryTimer = window.setTimeout(attempt, RETRY_MS);
    };
    attempt();

    return () => {
      ctrl.abort();
      window.clearTimeout(slowTimer);
      window.clearTimeout(retryTimer);
    };
  }, []);

  return health;
}
