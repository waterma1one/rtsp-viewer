import { useEffect, useState, type CSSProperties } from "react";
import { AddStreamForm } from "./components/AddStreamForm";
import { LayoutToggle, type Layout } from "./components/LayoutToggle";
import { StreamTile } from "./components/StreamTile";
import { useServerHealth } from "./hooks/useServerHealth";
import { useStreamList } from "./hooks/useStreamList";
import { fetchDemoStreams, type DemoStream } from "./lib/api";
import { isMseSupported } from "./lib/msePlayer";
import styles from "./App.module.css";

const LAYOUT_KEY = "stream-wall:layout";

function loadLayout(): Layout {
  try {
    const v = localStorage.getItem(LAYOUT_KEY);
    if (v === "1" || v === "2" || v === "3") return Number(v) as Layout;
  } catch {
    /* storage unavailable */
  }
  return "auto";
}

export default function App() {
  const { streams, has, add, remove, clear } = useStreamList();
  const health = useServerHealth();
  const [layout, setLayout] = useState<Layout>(loadLayout);
  const [demos, setDemos] = useState<DemoStream[]>([]);

  useEffect(() => {
    try {
      localStorage.setItem(LAYOUT_KEY, String(layout));
    } catch {
      /* storage unavailable */
    }
  }, [layout]);

  useEffect(() => {
    if (health !== "up") return;
    const ctrl = new AbortController();
    fetchDemoStreams(ctrl.signal)
      .then(setDemos)
      .catch(() => setDemos([]));
    return () => ctrl.abort();
  }, [health]);

  const missingDemos = demos.filter((d) => !has(d.url));
  const addDemos = () => add(missingDemos.map((d) => ({ url: d.url, name: d.name })));

  return (
    <div className={styles.app}>
      <header className={styles.header}>
        <div className={styles.brand}>
          <span className={styles.mark} aria-hidden="true" />
          <h1 className={styles.title}>Stream wall</h1>
        </div>
        <AddStreamForm isOnWall={has} onAdd={(e) => add([e])} />
        <div className={styles.tools}>
          {missingDemos.length > 0 && streams.length > 0 && (
            <button type="button" className={styles.secondary} onClick={addDemos}>
              Add demo streams
            </button>
          )}
          <LayoutToggle value={layout} onChange={setLayout} />
        </div>
      </header>

      <ServerBanner health={health} />
      {!isMseSupported() && (
        <p className={styles.banner} data-tone="fault" role="alert">
          This browser can't play live video. Use a recent version of Chrome, Edge, Firefox or Safari.
        </p>
      )}

      <main className={styles.main}>
        {streams.length === 0 ? (
          <section className={styles.empty} aria-labelledby="empty-title">
            <h2 id="empty-title" className={styles.emptyTitle}>
              Nothing on the wall yet
            </h2>
            <p className={styles.emptyText}>
              Paste an RTSP address above to watch a camera. Every stream you add plays side by side, and
              the wall remembers them next time you visit.
            </p>
            {demos.length > 0 && (
              <button type="button" className={styles.primary} onClick={addDemos}>
                Watch {demos.length} demo streams
              </button>
            )}
          </section>
        ) : (
          <>
            <ul
              className={styles.grid}
              data-layout={layout}
              style={layout === "auto" ? undefined : ({ "--cols": layout } as CSSProperties)}
            >
              {streams.map((s) => (
                <li key={s.id} className={styles.cell}>
                  <StreamTile stream={s} onRemove={remove} />
                </li>
              ))}
            </ul>
            <div className={styles.footer}>
              <span>
                {streams.length} {streams.length === 1 ? "stream" : "streams"}
              </span>
              <button type="button" className={styles.link} onClick={clear}>
                Remove all
              </button>
            </div>
          </>
        )}
      </main>
    </div>
  );
}

function ServerBanner({ health }: { health: ReturnType<typeof useServerHealth> }) {
  if (health === "waking") {
    return (
      <p className={styles.banner} data-tone="standby" role="status">
        Starting the server. The demo host sleeps when idle, so this can take up to a minute.
      </p>
    );
  }
  if (health === "down") {
    return (
      <p className={styles.banner} data-tone="fault" role="alert">
        The server isn't responding. Reload the page to try again.
      </p>
    );
  }
  return null;
}
