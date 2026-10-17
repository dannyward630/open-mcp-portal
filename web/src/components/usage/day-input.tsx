/*
 * A whole UTC day for the usage dashboards' custom range, as three selects named "From month", "From day",
 * "From year" (or "To …"). A native date input announces its parts only as "Month", "Day" and "Year", so the
 * From and To inputs side by side couldn't be told apart. The value is YYYY-MM-DD, or "" until all three parts
 * are chosen; the day is kept within the chosen month (31 January → February gives 28 or 29 February).
 */
import { CircleAlert } from "lucide-react";
import { useEffect, useId, useState } from "react";
import { NativeSelect } from "@/components/ui/input/input";
import u from "./usage.module.css";

type Parts = { y: string; m: string; d: string };

const monthNames = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];
const pad = (n: number) => String(n).padStart(2, "0");

function split(value: string): Parts | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  return m ? { y: m[1]!, m: m[2]!, d: m[3]! } : null;
}

/** Days in a month (1-12) of a year, in the Gregorian calendar. */
export const daysIn = (year: number, month: number) => new Date(Date.UTC(year, month, 0)).getUTCDate();

/** The years offered: this UTC year and the ten before it (usage is kept at most 3,650 days), plus the value's. */
function yearsFor(now: Date, current: string): string[] {
  const thisYear = now.getUTCFullYear();
  const years = Array.from({ length: 11 }, (_, i) => String(thisYear - i));
  if (current && !years.includes(current)) years.push(current);
  return years.sort((a, b) => Number(b) - Number(a));
}

type Props = {
  /** "From" or "To": the group is "From (UTC)", its selects "From month", "From day" and "From year". */
  name: string;
  value: string;
  onChange: (value: string) => void;
  description?: string;
  error?: string;
  now?: Date;
};

export function DayInput({ name, value, onChange, description, error, now = new Date() }: Props) {
  const id = useId();
  const [parts, setParts] = useState<Parts>(() => split(value) ?? { y: "", m: "", d: "" });
  // A new value from outside (the URL, the range select) replaces the parts; "" (sent while incomplete) keeps them.
  useEffect(() => {
    const next = split(value);
    if (next) setParts(next);
  }, [value]);

  const update = (patch: Partial<Parts>) => {
    const next = { ...parts, ...patch };
    if (next.y && next.m && next.d) next.d = pad(Math.min(Number(next.d), daysIn(Number(next.y), Number(next.m))));
    setParts(next);
    onChange(next.y && next.m && next.d ? `${next.y}-${next.m}-${next.d}` : "");
  };

  const dayCount = parts.y && parts.m ? daysIn(Number(parts.y), Number(parts.m)) : 31;
  const describedBy = [description && `${id}-desc`, error && `${id}-err`].filter(Boolean).join(" ") || undefined;
  const invalid = error ? true : undefined;
  return (
    <div role="group" aria-labelledby={`${id}-label`} aria-describedby={describedBy} className={u.day}>
      <span id={`${id}-label`} className={u.dayLabel}>{`${name} (UTC)`}</span>
      <div className={u.dayParts}>
        <NativeSelect aria-label={`${name} month`} aria-invalid={invalid} value={parts.m} onChange={(e) => update({ m: e.target.value })}>
          {!parts.m && <option value="">Month</option>}
          {monthNames.map((label, i) => (
            <option key={label} value={pad(i + 1)}>
              {label}
            </option>
          ))}
        </NativeSelect>
        <NativeSelect aria-label={`${name} day`} aria-invalid={invalid} value={parts.d} onChange={(e) => update({ d: e.target.value })}>
          {!parts.d && <option value="">Day</option>}
          {Array.from({ length: dayCount }, (_, i) => (
            <option key={i} value={pad(i + 1)}>
              {i + 1}
            </option>
          ))}
        </NativeSelect>
        <NativeSelect aria-label={`${name} year`} aria-invalid={invalid} value={parts.y} onChange={(e) => update({ y: e.target.value })}>
          {!parts.y && <option value="">Year</option>}
          {yearsFor(now, parts.y).map((y) => (
            <option key={y} value={y}>
              {y}
            </option>
          ))}
        </NativeSelect>
      </div>
      {description && (
        <p id={`${id}-desc`} className={u.dayDescription}>
          {description}
        </p>
      )}
      {error && (
        <p id={`${id}-err`} className={u.dayError}>
          <CircleAlert aria-hidden />
          <span>{error}</span>
        </p>
      )}
    </div>
  );
}
