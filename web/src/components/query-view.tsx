/* The loading / error / empty / content switch that list cards repeat for every query. */
import type { ReactNode } from "react";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { Loading } from "@/components/ui/spinner/spinner";
import s from "../pages/shared.module.css";

type QueryViewProps = {
  /** A TanStack Query result (only isLoading and error are read). */
  query: { isLoading: boolean; error: unknown };
  loadingLabel?: string;
  /** Shown instead of the children when truthy, e.g. `list.length === 0 && <EmptyState … />`. */
  empty?: ReactNode;
  children: ReactNode;
};

/**
 * Renders a spinner while the query loads, a padded error alert if it failed,
 * `empty` when given, and the children otherwise. Meant for flush cards:
 * `<Card flush><QueryView …><Table …/></QueryView></Card>`.
 */
export function QueryView({ query, loadingLabel, empty, children }: QueryViewProps) {
  if (query.isLoading) return <Loading label={loadingLabel} />;
  if (query.error) {
    return (
      <div className={s.pad}>
        <ErrorAlert error={query.error} />
      </div>
    );
  }
  return <>{empty || children}</>;
}

/** The "Load more" button under a paged list (a TanStack infinite query); nothing when there are no more pages. */
export function LoadMore({ query }: { query: { hasNextPage: boolean; isFetchingNextPage: boolean; fetchNextPage: () => unknown } }) {
  if (!query.hasNextPage) return null;
  return (
    <div className={s.more}>
      <Button variant="secondary" size="sm" loading={query.isFetchingNextPage} onClick={() => query.fetchNextPage()}>
        Load more
      </Button>
    </div>
  );
}
