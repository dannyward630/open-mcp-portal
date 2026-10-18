/*
 * The team overview's Attention card: failing servers, held-back capability changes, lapsed sign-ins, servers waiting
 * for review and, for owners and admins, expiring service account keys and tokens and failing webhooks. Hidden while
 * empty for members; owners and admins get "All clear". An archived team shows only its notice (nothing is asked
 * for). A failed load is an inline error with Retry; the rest of the page keeps working.
 */
import { useQuery } from "@tanstack/react-query";
import { BadgeCheck } from "lucide-react";
import { teamAttentionQuery } from "../../../api/attention";
import { AttentionList } from "../../../components/attention/attention-list";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { Card } from "@/components/ui/card/card";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import { useTeam } from "../common";

export function AttentionCard() {
  const { slug, archived, isManager } = useTeam();
  const attention = useQuery(teamAttentionQuery(slug, !archived));
  if (archived) return null;
  if (attention.error) {
    return (
      <Card title="Needs attention">
        <ErrorAlert error={attention.error} title="Couldn't load what needs attention" onRetry={() => void attention.refetch()} />
      </Card>
    );
  }
  const data = attention.data;
  if (!data) return null;
  if (data.attention.length === 0) {
    if (!isManager) return null;
    return (
      <Card title="Needs attention">
        <EmptyState size="compact" icon={<BadgeCheck />} title="All clear" description="No server is failing and nothing is waiting or about to expire." />
      </Card>
    );
  }
  return (
    <Card title="Needs attention" description="Most severe first. Each button opens where it is dealt with." actions={<Badge tone="warning">{data.attention.length}</Badge>}>
      <AttentionList items={data.attention} totals={data.totals} moreTo={{ kind: "team", team: slug }} />
    </Card>
  );
}
