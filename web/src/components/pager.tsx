/*
 * Page-by-page navigation over a cursor-paged list (Previous / Next), for
 * lists too long for "Load more", such as a crawl's thousands of pages. The
 * cursors of the pages seen so far are kept, so Previous needs no extra API.
 */
import { useState } from "react";
import { Pagination, PaginationContent, PaginationItem, PaginationNext, PaginationPrevious } from "@/components/ui/pagination/pagination";
import s from "../pages/shared.module.css";

export type CursorPager = {
  /** The cursor of the current page (undefined for the first). */
  cursor: string | undefined;
  /** 0-based page number. */
  page: number;
  next: (nextCursor: string) => void;
  prev: () => void;
  reset: () => void;
};

export function useCursorPager(): CursorPager {
  const [stack, setStack] = useState<(string | undefined)[]>([undefined]);
  return {
    cursor: stack[stack.length - 1],
    page: stack.length - 1,
    next: (c) => setStack((st) => [...st, c]),
    prev: () => setStack((st) => (st.length > 1 ? st.slice(0, -1) : st)),
    reset: () => setStack([undefined]),
  };
}

/** "51–100 of 3,210" (or "51–100" when the total isn't known). */
export function rangeText(page: number, pageSize: number, shown: number, total?: number) {
  if (shown === 0) return "";
  const first = page * pageSize + 1;
  const last = page * pageSize + shown;
  return `${first.toLocaleString()}–${last.toLocaleString()}${total !== undefined ? ` of ${total.toLocaleString()}` : ""}`;
}

type PagerBarProps = { pager: CursorPager; pageSize: number; shown: number; total?: number; nextCursor?: string | null; label: string };

/** Previous / Next (bitop-ui Pagination) with the range shown; nothing when everything fits on one page. */
export function PagerBar({ pager, pageSize, shown, total, nextCursor, label }: PagerBarProps) {
  if (pager.page === 0 && !nextCursor) return null;
  return (
    <div className={s.pager}>
      <span className={s.pagerRange} aria-live="polite">
        {rangeText(pager.page, pageSize, shown, total)}
      </span>
      <Pagination label={label}>
        <PaginationContent>
          <PaginationItem>
            <PaginationPrevious size="sm" disabled={pager.page === 0} render={<button type="button" />} onClick={pager.prev} />
          </PaginationItem>
          <PaginationItem>
            <PaginationNext size="sm" disabled={!nextCursor} render={<button type="button" />} onClick={() => nextCursor && pager.next(nextCursor)} />
          </PaginationItem>
        </PaginationContent>
      </Pagination>
    </div>
  );
}
