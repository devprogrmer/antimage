import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api } from "../lib/api";
import { can, useSession } from "../lib/session";
import { MutationError } from "../routes/Resellers";
import { formatTimestamp, formatNumber, t } from "../i18n";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";

interface NodeWithDrift {
  id: number;
  name: string;
  address: string;
  desired_revision: number;
  applied_revision: number;
  config_drift: number;
  last_sync_at: number | null;
  last_sync_error: string;
  online: boolean;
}

/**
 * Drift detection dashboard: show nodes with config drift, one-click sync.
 * 
 * Lists all nodes where applied_revision != desired_revision, with quick
 * sync actions and drift details.
 */
export function DriftDetectionDashboard() {
  const session = useSession();
  const queryClient = useQueryClient();

  const nodes = useQuery({
    queryKey: ["nodes"],
    queryFn: () => api.get<{ nodes: NodeWithDrift[] }>("/api/v1/nodes"),
    refetchInterval: 5000,
  });

  const sync = useMutation({
    mutationFn: (nodeId: number) =>
      api.post(`/api/v1/nodes/${nodeId}/sync`, {}),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["nodes"] });
    },
  });

  const syncAll = useMutation({
    mutationFn: async (nodeIds: number[]) => {
      // Sync all drifted nodes in parallel
      await Promise.all(nodeIds.map((id) => api.post(`/api/v1/nodes/${id}/sync`, {})));
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["nodes"] });
    },
  });

  const mayWrite = can(session.data, "node:write");
  const allNodes = nodes.data?.nodes ?? [];
  const driftedNodes = allNodes.filter(
    (n) => n.desired_revision !== n.applied_revision
  );
  const driftedOnline = driftedNodes.filter((n) => n.online);

  if (nodes.isLoading) {
    return (
      <div className="py-8 text-center text-sm text-muted-foreground">
        {t("common.loading")}...
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">{t("drift.title")}</h3>
          <p className="text-xs text-muted-foreground">{t("drift.description")}</p>
        </div>
        <div className="flex items-center gap-2">
          <Badge variant={driftedNodes.length > 0 ? "warning" : "success"}>
            {formatNumber(driftedNodes.length)} {t("drift.drifted")}
          </Badge>
          {mayWrite && driftedOnline.length > 0 && (
            <Button
              size="sm"
              onClick={() => syncAll.mutate(driftedOnline.map((n) => n.id))}
              disabled={syncAll.isPending}
            >
              {t("drift.syncAll")}
            </Button>
          )}
        </div>
      </header>

      <MutationError error={nodes.error || sync.error || syncAll.error} />

      {driftedNodes.length === 0 && (
        <div className="rounded-lg border border-success/30 bg-success/10 p-6 text-center">
          <p className="text-sm font-medium text-success">{t("drift.noDrift")}</p>
          <p className="mt-1 text-xs text-muted-foreground">
            {t("drift.noDriftDesc")}
          </p>
        </div>
      )}

      {driftedNodes.length > 0 && (
        <div className="space-y-2">
          {driftedNodes.map((node) => (
            <DriftCard
              key={node.id}
              node={node}
              mayWrite={mayWrite}
              onSync={() => sync.mutate(node.id)}
              isSyncing={sync.isPending}
            />
          ))}
        </div>
      )}
    </div>
  );
}

interface DriftCardProps {
  node: NodeWithDrift;
  mayWrite: boolean;
  onSync: () => void;
  isSyncing: boolean;
}

function DriftCard({ node, mayWrite, onSync, isSyncing }: DriftCardProps) {
  const revisionDelta = node.desired_revision - node.applied_revision;

  return (
    <div className="flex gap-3 rounded-lg border border-warning/50 bg-warning/5 p-3">
      <div className="flex-1 space-y-2">
        <div className="flex items-center gap-2">
          <h4 className="font-mono text-sm font-medium">{node.name}</h4>
          {!node.online && (
            <Badge variant="destructive" className="text-[10px]">
              {t("node.offline")}
            </Badge>
          )}
          {node.online && (
            <Badge variant="success" className="text-[10px]">
              {t("node.online")}
            </Badge>
          )}
        </div>

        <p className="font-mono text-xs text-muted-foreground">{node.address}</p>

        <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs sm:grid-cols-4">
          <dt className="text-muted-foreground">{t("drift.applied")}:</dt>
          <dd className="font-mono">{formatNumber(node.applied_revision)}</dd>

          <dt className="text-muted-foreground">{t("drift.desired")}:</dt>
          <dd className="font-mono">{formatNumber(node.desired_revision)}</dd>

          <dt className="text-muted-foreground">{t("drift.delta")}:</dt>
          <dd className="font-mono text-warning">
            +{formatNumber(revisionDelta)}
          </dd>

          <dt className="text-muted-foreground">{t("drift.lastSync")}:</dt>
          <dd className="font-mono text-muted-foreground">
            {node.last_sync_at ? formatTimestamp(node.last_sync_at) : t("common.never")}
          </dd>
        </dl>

        {node.last_sync_error && (
          <div className="rounded bg-destructive/10 p-2 text-xs text-destructive">
            <strong>{t("drift.lastError")}:</strong> {node.last_sync_error}
          </div>
        )}
      </div>

      {mayWrite && node.online && (
        <div className="flex shrink-0 items-center">
          <Button
            size="sm"
            variant="outline"
            onClick={onSync}
            disabled={isSyncing}
            className="border-success text-success hover:bg-success/10"
          >
            {isSyncing ? t("common.loading") : t("node.sync")}
          </Button>
        </div>
      )}
    </div>
  );
}
