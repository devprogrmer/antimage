import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { can, useSession } from "../lib/session";
import { MutationError } from "../routes/Resellers";
import { ConfirmDialog } from "./ConfirmDialog";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { formatNumber, formatTimestamp, t } from "../i18n";

interface Reconciliation {
  node_id: number;
  node_name: string;
  status: string;
  desired_revision: number;
  applied_revision: number;
  drift_detected: boolean;
  needs_sync: boolean;
  last_sync_at?: number;
  last_sync_error?: string;
  recent_runs: Array<{
    id: number;
    outcome: string;
    started_at: number;
    finished_at?: number;
  }>;
}

/**
 * Whether the node is doing what it was told, and the controls to make it.
 *
 * §3.3 asks the panel to answer "why is this node not converged" from state it
 * already holds. This endpoint answers exactly that -- drift, whether a sync is
 * needed, when the last one ran and why it failed -- and had no client, so the
 * answer was reachable only by reading the database.
 *
 * The actions beside it are the ones an operator reaches for having read it.
 * Each is a mutation on the node, so each is confirmed and each states what it
 * disrupts.
 */
export function NodeReconciliation({ nodeId }: { nodeId: number }) {
  const session = useSession();
  const queryClient = useQueryClient();
  const [pending, setPending] = useState<null | "restart" | "sync" | "maintenance">(null);

  const mayWrite = can(session.data, "node:write");

  const state = useQuery({
    queryKey: ["node", nodeId, "reconciliation"],
    queryFn: () =>
      api.get<Reconciliation>(`/api/v1/nodes/${nodeId}/reconciliation`),
  });

  const act = useMutation({
    mutationFn: (action: "restart" | "sync" | "maintenance") =>
      api.post(`/api/v1/nodes/${nodeId}/${action}`),
    onSuccess: () => {
      setPending(null);
      // The whole node family: an action here moves the revision, the runs and
      // the status, and refetching one of them would leave the others stale.
      queryClient.invalidateQueries({ queryKey: ["node", nodeId] });
    },
  });

  const data = state.data;

  return (
    <div className="space-y-4">
      <MutationError error={state.error} />

      {data && (
        <dl className="grid grid-cols-2 gap-x-6 gap-y-1 text-xs sm:grid-cols-4">
          <dt className="text-muted-foreground">{t("node.status")}</dt>
          <dd>
            <Badge variant={data.status === "online" ? "success" : "outline"}>
              {data.status}
            </Badge>
          </dd>
          <dt className="text-muted-foreground">{t("node.revision")}</dt>
          <dd className="font-mono">
            {formatNumber(data.applied_revision)} / {formatNumber(data.desired_revision)}
          </dd>
          <dt className="text-muted-foreground">{t("node.drift")}</dt>
          <dd>
            {data.drift_detected ? (
              <Badge variant="warning">{t("node.driftDetected")}</Badge>
            ) : (
              <Badge variant="success">{t("node.converged")}</Badge>
            )}
          </dd>
          <dt className="text-muted-foreground">{t("node.lastSync")}</dt>
          <dd className="font-mono text-muted-foreground">
            {data.last_sync_at ? formatTimestamp(data.last_sync_at) : t("node.never")}
          </dd>
        </dl>
      )}

      {/* Shown verbatim. The node's own reason for failing is more use to an
          operator than a sentence written here about failures in general. */}
      {data?.last_sync_error && (
        <p className="rounded border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
          {data.last_sync_error}
        </p>
      )}

      {mayWrite && (
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="outline" onClick={() => setPending("sync")}>
            {t("node.sync")}
          </Button>
          <Button size="sm" variant="outline" onClick={() => setPending("restart")}>
            {t("node.restart")}
          </Button>
          <Button size="sm" variant="outline" onClick={() => setPending("maintenance")}>
            {t("node.maintenance")}
          </Button>
        </div>
      )}
      <MutationError error={act.error} />

      <ConfirmDialog
        open={pending !== null}
        onOpenChange={(open) => !open && setPending(null)}
        title={
          pending === "restart"
            ? t("node.confirmRestart")
            : pending === "maintenance"
              ? t("node.confirmMaintenance")
              : t("node.confirmSync")
        }
        // Sync re-applies the desired document and does not by itself drop
        // anything; restart and maintenance do. Saying "sessions drop" for all
        // three would train an operator to ignore the sentence.
        description={
          pending === "sync" ? t("node.syncEffect") : t("node.restartEffect")
        }
        destructive={pending !== "sync"}
        pending={act.isPending}
        onConfirm={() => pending && act.mutate(pending)}
      />
    </div>
  );
}
