import { useCallback, useEffect, useState } from "react";

export interface StreamEntry {
  id: string;
  url: string;
  name: string;
}

const STORAGE_KEY = "stream-wall:streams:v1";

function load(): StreamEntry[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    if (!Array.isArray(parsed)) return [];
    return parsed.filter(
      (s): s is StreamEntry =>
        typeof s?.id === "string" && typeof s?.url === "string" && typeof s?.name === "string",
    );
  } catch {
    return []; // storage blocked or corrupt: start empty
  }
}

function save(streams: StreamEntry[]) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(streams));
  } catch {
    /* private mode or quota: the wall still works for this visit */
  }
}

/** The streams on the wall, remembered across visits in this browser. */
export function useStreamList() {
  const [streams, setStreams] = useState<StreamEntry[]>(load);

  useEffect(() => save(streams), [streams]);

  const has = useCallback((url: string) => streams.some((s) => s.url === url), [streams]);

  const add = useCallback((entries: Omit<StreamEntry, "id">[]) => {
    setStreams((prev) => {
      const fresh = entries.filter((e) => !prev.some((p) => p.url === e.url));
      return [...prev, ...fresh.map((e) => ({ ...e, id: crypto.randomUUID() }))];
    });
  }, []);

  const remove = useCallback((id: string) => {
    setStreams((prev) => prev.filter((s) => s.id !== id));
  }, []);

  const clear = useCallback(() => setStreams([]), []);

  return { streams, has, add, remove, clear };
}
