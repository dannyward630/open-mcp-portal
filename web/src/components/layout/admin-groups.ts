/*
 * The admin sidebar's groups collapse to their headers:
 * the group of the current page opens by itself, the others stay as the
 * person left them (remembered in this browser); Overview has no group.
 * Only groups opened by hand are remembered: a group that opened because its
 * page was current closes again when the person moves on, so open groups
 * don't pile up and push the current item out of view.
 * Groups are remembered by label; labels that no longer exist are forgotten.
 */
import { useCallback, useState } from "react";
import { adminSections } from "./nav";

const storageKey = "omg.adminNavOpen";

/** The remembered open groups that exist now: unknown labels are dropped. */
export function currentGroups(saved: string[]): string[] {
  const known = new Set(adminSections.flatMap((s) => (s.label ? [s.label] : [])));
  return [...new Set(saved)].filter((l) => known.has(l));
}

function read(): string[] {
  try {
    const parsed: unknown = JSON.parse(globalThis.localStorage?.getItem(storageKey) ?? "[]");
    const saved = Array.isArray(parsed) ? parsed.filter((x): x is string => typeof x === "string") : [];
    const now = currentGroups(saved);
    if (now.join("\n") !== saved.join("\n")) write(now);
    return now;
  } catch {
    return [];
  }
}

function write(open: string[]) {
  try {
    globalThis.localStorage?.setItem(storageKey, JSON.stringify(open));
  } catch {
    // Storage can be unavailable (private mode).
  }
}

/**
 * Which admin groups are open: the ones the person opened by hand (remembered), plus the current page's
 * group. That one opens by itself without being remembered, so leaving the page closes it again unless
 * it was opened by hand; closing it by hand hides it until the page is in another group.
 */
export function useAdminGroups(active: string | undefined) {
  const [saved, setSaved] = useState<string[]>(read);
  const [closed, setClosed] = useState<{ active: string | undefined; closed: boolean }>({ active, closed: false });
  // A state update during render: arriving in another group forgets that the last one was closed by hand.
  if (closed.active !== active) setClosed({ active, closed: false });
  const activeClosed = closed.active === active && closed.closed;
  const setOpen = useCallback(
    (label: string, isOpen: boolean) => {
      if (label === active) setClosed({ active, closed: !isOpen });
      setSaved((list) => {
        const next = isOpen ? [...list.filter((l) => l !== label), label] : list.filter((l) => l !== label);
        write(next);
        return next;
      });
    },
    [active],
  );
  return { isOpen: (label: string) => (label === active ? !activeClosed : saved.includes(label)), setOpen };
}
