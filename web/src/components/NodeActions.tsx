import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { MutationError } from "../routes/Resellers";
import { ConfirmDialog } from "./ConfirmDialog";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { formatNumber, t } from "../i18n";

/**
 * The operator actions on a node: take it in and out of service, restart it,
 * force a resync, and the same over a selection.
 *
 * Every endpoint here existed and none was reachable from the browser, so
 * draining a node before maintenance meant the API. They are grouped because
 * they differ in what they cost, and the difference is what an operator needs
 * to see before clicking:
 *
 *   - MAINTENANCE is the gentle one. The node stops taking new connections and
 *     existing sessions continue, which is what "drain" means here.
 *   - DISABLE removes the node from service. Sessions drop.
 *   - RESTART bounces the agent's services. Sessions drop.
 *   - SYNC forces a reconcile and disturbs nothing.
 */

export interface NodeSummary {
  id: number;
  name: string;
  status: string;
}

type Action = "enable" | "disable" | "maintenance" | "restart" | "sync";

/** Actions that drop connected users, so the UI asks first. */
const DISRUPTIVE: Record<Action, boolean> = {
  enable: false,
  disable: true,
  restart: true,
  // Entering maintenance drains rather than cuts: existing sessions continue.
  maintenance: false,
  sync: false,
};

export function NodeActions({ node }: { node: NodeSummary }) {
  const queryClient = useQueryClient();
  const [pending, setPending] = useState<Action | null>(null);
  const [reason, setReason] = useState("");

  const inMaintenance = node.status === "maintenance";
  const disabled = node.status === "disabled";

  const run = useMutation({
    mutationFn: (action: Action) => {
      switch (action) {
        case "maintenance":
          return api.post(`/api/v1/nodes/${node.id}/maintenance`, {
            // Toggle: the same control takes a node in and out, because two
            // buttons where only one applies is a control an operator has to
            // read twice.
            enable: !inMaintenance,
            reason: reason || undefined,
          });
        case "disable":
          return api.post(`/api/v1/nodes/${node.id}/disable`, {
            reason: reason || undefined,
          });
        default:
          return api.post(`/api/v1/nodes/${node.id}/${action}`, {});
      }
    },
    onSuccess: () => {
      setPending(null);
      setReason("");
      queryClient.invalidateQueries({ queryKey: ["nodes"] });
      queryClient.invalidateQueries({ queryKey: ["node", node.id] });
    },
  });

  function invoke(action: Action) {
    if (DISRUPTIVE[action]) setPending(action);
    else run.mutate(action);
  }

  return (
    <div className="flex flex-wrap items-center gap-2">
      {disabled ? (
        <Button size="sm" onClick={() => invoke("enable")} disabled={run.isPending}>
          {t("node.enable")}
        </Button>
      ) : (
        <Button
          size="sm"
          variant="outline"
          onClick={() => invoke("disable")}
          disabled={run.isPending}
        >
          {t("node.disable")}
        </Button>
      )}

      <Button
        size="sm"
        variant="outline"
        onClick={() => invoke("maintenance")}
        disabled={run.isPending}
      >
        {inMaintenance ? t("node.exitMaintenance") : t("node.enterMaintenance")}
      </Button>

      <Button
        size="sm"
        variant="outline"
        onClick={() => invoke("sync")}
        disabled={run.isPending}
      >
        {t("node.sync")}
      </Button>

      <Button
        size="sm"
        variant="outline"
        onClick={() => invoke("restart")}
        disabled={run.isPending}
      >
        {t("node.restart")}
      </Button>

      <MutationError error={run.error} />

      <ConfirmDialog
        open={pending !== null}
        onOpenChange={(open) => !open && setPending(null)}
        title={pending === "restart" ? t("node.confirmRestart") : t("node.confirmDisable")}
        description={
          pending === "restart"
            ? t("node.restartWarning", { name: node.name })
            : t("node.disableWarning", { name: node.name })
        }
        confirmLabel={pending === "restart" ? t("node.restart") : t("node.disable")}
        pending={run.isPending}
        onConfirm={() => pending && run.mutate(pending)}
      />
    </div>
  );
}

interface BulkResult {
  total_nodes: number;
  success_count: number;
  failure_count: number;
  results: { node_id: number; node_name?: string; success: boolean; error?: string }[];
}

/**
 * The same actions over a selection.
 *
 * Uses the panel's own bulk endpoint rather than looping, because that
 * endpoint reports a per-node outcome -- and a fleet action where two of nine
 * nodes failed is the normal case, not the exception. A loop in the browser
 * would have to reinvent that reporting and would drift from it.
 */
export function BulkNodeActions({
  nodes,
  onDone,
}: {
  nodes: NodeSummary[];
  onDone: () => void;
}) {
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState<Action | null>(null);
  const [result, setResult] = useState<BulkResult | null>(null);

  const run = useMutation({
    mutationFn: (action: Action) =>
      api.post<BulkResult>("/api/v1/nodes/bulk/action", {
        node_ids: nodes.map((n) => n.id),
        action,
        // The bulk endpoint takes maintenance as an explicit flag rather than
        // a toggle: over a mixed selection "toggle" has no single meaning, and
        // guessing per node would leave the fleet in a state nobody asked for.
        ...(action === "maintenance" ? { maintenance_enable: true } : {}),
      }),
    onSuccess: (r) => {
      setResult(r);
      setConfirming(null);
      queryClient.invalidateQueries({ queryKey: ["nodes"] });
      onDone();
    },
  });

  if (nodes.length === 0 && result === null) return null;

  function invoke(action: Action) {
    if (DISRUPTIVE[action]) setConfirming(action);
    else run.mutate(action);
  }

  return (
    <div className="mb-2 flex flex-wrap items-center gap-2 rounded border border-border bg-background p-2">
      {nodes.length > 0 && (
        <>
          <span className="text-xs">
            {t("bulk.selected", { count: formatNumber(nodes.length) })}
          </span>
          <Button size="sm" variant="outline" disabled={run.isPending}
            onClick={() => invoke("enable")}>
            {t("node.enable")}
          </Button>
          <Button size="sm" variant="outline" disabled={run.isPending}
            onClick={() => invoke("disable")}>
            {t("node.disable")}
          </Button>
          <Button size="sm" variant="outline" disabled={run.isPending}
            onClick={() => invoke("maintenance")}>
            {t("node.enterMaintenance")}
          </Button>
          <Button size="sm" variant="outline" disabled={run.isPending}
            onClick={() => invoke("sync")}>
            {t("node.sync")}
          </Button>
          <Button size="sm" variant="outline" disabled={run.isPending}
            onClick={() => invoke("restart")}>
            {t("node.restart")}
          </Button>
          <Button size="sm" variant="ghost" className="ms-auto"
            onClick={() => { setResult(null); onDone(); }}>
            {t("bulk.clearSelection")}
          </Button>
        </>
      )}

      <MutationError error={run.error} />

      {result && (
        <div role="status" className="w-full">
          <p className={result.failure_count > 0 ? "text-xs text-warning" : "text-xs text-success"}>
            {t("bulk.result", {
              changed: formatNumber(result.success_count),
              failed: formatNumber(result.failure_count),
            })}
          </p>
          {/* Named per node. "2 failed" over a nine-node fleet is not
              actionable; knowing WHICH two is. */}
          {result.results.filter((r) => !r.success).length > 0 && (
            <ul className="mt-1 space-y-0.5 text-xs text-muted-foreground">
              {result.results
                .filter((r) => !r.success)
                .map((r) => (
                  <li key={r.node_id}>
                    <span className="font-mono">{r.node_name ?? r.node_id}</span>
                    {" — "}
                    {r.error ?? t("bulk.failed")}
                  </li>
                ))}
            </ul>
          )}
        </div>
      )}

      <ConfirmDialog
        open={confirming !== null}
        onOpenChange={(open) => !open && setConfirming(null)}
        title={confirming === "restart" ? t("node.confirmRestart") : t("node.confirmDisable")}
        description={t("node.bulkAffects", { count: formatNumber(nodes.length) })}
        confirmLabel={confirming === "restart" ? t("node.restart") : t("node.disable")}
        pending={run.isPending}
        onConfirm={() => confirming && run.mutate(confirming)}
      />
    </div>
  );
}

/** A reason box for disable and maintenance, so the audit row says why. */
export function ReasonField({
  value,
  onChange,
}: {
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div>
      <label className="block text-xs text-muted-foreground" htmlFor="node-reason">
        {t("node.reason")}
      </label>
      <Input id="node-reason" value={value} onChange={(e) => onChange(e.target.value)} />
    </div>
  );
}
