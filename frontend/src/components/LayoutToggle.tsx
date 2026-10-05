import styles from "./LayoutToggle.module.css";

export type Layout = "auto" | 1 | 2 | 3;

const OPTIONS: { value: Layout; label: string; hint: string }[] = [
  { value: "auto", label: "Auto", hint: "Fit as many as the screen allows" },
  { value: 1, label: "1", hint: "One per row" },
  { value: 2, label: "2", hint: "Two per row" },
  { value: 3, label: "3", hint: "Three per row" },
];

export function LayoutToggle({ value, onChange }: { value: Layout; onChange: (v: Layout) => void }) {
  return (
    <fieldset className={styles.group}>
      <legend className={styles.legend}>Columns</legend>
      <div className={styles.options}>
        {OPTIONS.map((o) => (
          <label key={o.value} className={styles.option} title={o.hint}>
            <input
              type="radio"
              name="layout"
              value={o.value}
              checked={value === o.value}
              onChange={() => onChange(o.value)}
            />
            <span>{o.label}</span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}
