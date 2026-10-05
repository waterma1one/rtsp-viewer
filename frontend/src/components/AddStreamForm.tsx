import { useId, useRef, useState, type FormEvent } from "react";
import { nameFromUrl, quickCheckUrl, validateStreamUrl } from "../lib/api";
import { Icon } from "./Icon";
import styles from "./AddStreamForm.module.css";

interface Props {
  isOnWall: (url: string) => boolean;
  onAdd: (entry: { url: string; name: string }) => void;
}

export function AddStreamForm({ isOnWall, onAdd }: Props) {
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [checking, setChecking] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const errorId = useId();

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (checking) return;
    const quick = quickCheckUrl(value);
    if (quick) {
      setError(quick);
      inputRef.current?.focus();
      return;
    }
    setChecking(true);
    setError(null);
    try {
      const res = await validateStreamUrl(value.trim());
      if (!res.ok) {
        setError(res.error);
        inputRef.current?.focus();
        return;
      }
      if (isOnWall(res.url)) {
        setError("This stream is already on the wall.");
        return;
      }
      onAdd({ url: res.url, name: nameFromUrl(res.url) });
      setValue("");
    } finally {
      setChecking(false);
    }
  };

  return (
    <form className={styles.form} onSubmit={submit} noValidate>
      <label className="visually-hidden" htmlFor={`${errorId}-input`}>
        RTSP stream URL
      </label>
      <div className={styles.row}>
        <input
          ref={inputRef}
          id={`${errorId}-input`}
          className={styles.input}
          type="url"
          inputMode="url"
          autoComplete="off"
          spellCheck={false}
          placeholder="rtsp://camera.example.com:554/live"
          value={value}
          onChange={(e) => {
            setValue(e.target.value);
            if (error) setError(null);
          }}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? errorId : undefined}
        />
        <button className={styles.add} type="submit" disabled={checking}>
          <Icon name="plus" size={18} />
          {checking ? "Checking" : "Add stream"}
        </button>
      </div>
      {error && (
        <p id={errorId} className={styles.error} role="alert">
          {error}
        </p>
      )}
    </form>
  );
}
