import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { can, useSession } from "../lib/session";
import { MutationError } from "../routes/Resellers";
import { ConfirmDialog } from "./ConfirmDialog";
import { Button } from "./ui/button";
import { Badge } from "./ui/badge";
import { formatNumber, formatTimestamp, t } from "../i18n";

interface Preview {
  node_id: number;
  current_revision: number;
  target_revision: number;
  current_doc_sha256: string;
  target_doc_sha256: string;
}

interface Validation {
  valid: boolean;
  conflicts: string[];
  warnings: string[];
}

interface Deployment {
  id: number;
  node_id: number;
  revision_id: number;
  strategy: string;
  status: string;
  created_at: number;
  started_at: number | null;
  completed_at: number | null;
  error: string;
}

/** Statuses that are still moving, and so worth refetching for. */
const IN_FLIGHT = new Set(["pending", "validating", "in_progress"]);

/**
 * DeploymentPanel: preview, validate, apply, roll back.
 *
 * Four endpoints that existed and had no client. They are shown in the order
 * §3.1 asks a mutation to run -- see what will change, hear whether it is
 * valid, then decide -- rather than as four buttons of equal weight. An
 * operator who has not previewed has not been told what they are about to do.
 *
 * Deploying is a restart-class change, so it is confirmed. Rollback doubly so:
 * it moves a node back to a document it has already left.
 */
export function DeploymentPanel({
  nodeId,
  targetRevision,
}: {
  nodeId: number;
  /**
   * The node's desired revision -- what a deployment would apply.
   *
   * Passed in rather than defaulted to zero: the preview endpoint looks the
   * revision up by (node_id, revision) and revision 0 does not exist, so a
   * placeholder would 404 on every click.
   */
  targetRevision: number;
}) {
  const session = useSession();
  const queryClient = useQueryClient();
  const [preview, setPreview] = useState<Preview | null>(null);
  const [validation, setValidation] = useState<Validation | null>(null);
  const [strategy, setStrategy] = useState("all_at_once");
  const [confirmingDeploy, setConfirmingDeploy] = useState(false);
  const [rollingBack, setRollingBack] = useState<Deployment | null>(null);

  const mayWrite = can(session.data, "node:write");

  const deployments = useQuery({
    queryKey: ["deployments", nodeId],
    queryFn: async () => {
      const all = await api.get<{ deployments: Deployment[] }>("/api/v1/deployments");
      // The endpoint returns everything in the caller's scope; this panel is
      // about one node. Filtering here rather than adding a query parameter
      // keeps the server's scope predicate the only filter that matters.
      return (all.deployments ?? []).filter((d) => d.node_id === nodeId);
    },
    // A deployment runs in the background after the 201, so a static list would
    // show "pending" until the operator reloaded.
    refetchInterval: (query) =>
      (query.state.data ?? []).some((d) => IN_FLIGHT.has(d.status)) ? 2000 : false,
  });

  const runPreview = useMutation({
    mutationFn: async () => {
      return api.post<Preview>("/api/v1/deployments/preview", {
        node_id: nodeId,
        revision: targetRevision,
      });
    },
    onSuccess: (p) => {
      setPreview(p);
      setValidation(null);
    },
  });

  const runValidate = useMutation({
    mutationFn: () =>
      api.post<Validation>("/api/v1/deployments/validate", {
        node_id: nodeId,
        revision: preview?.target_revision ?? targetRevision,
      }),
    onSuccess: setValidation,
  });

  const deploy = useMutation({
    mutationFn: () =>
      api.post<{ deployment_id: number }>("/api/v1/deployments", {
        node_id: nodeId,
        strategy,
      }),
    onSuccess: () => {
      setConfirmingDeploy(false);
      setPreview(null);
      setValidation(null);
      queryClient.invalidateQueries({ queryKey: ["deployments", nodeId] });
    },
  });

  const rollback = useMutation({
    mutationFn: (id: number) => api.post(`/api/v1/deployments/${id}/rollback`),
    onSuccess: () => {
      setRollingBack(null);
      queryClient.invalidateQueries({ queryKey: ["deployments", nodeId] });
    },
  });

  return (
    <section>
      <h3 className="mb-2 text-xs uppercase tracking-wide text-muted-foreground">
        {t("deploy.title")}
      </h3>

      {mayWrite && targetRevision === 0 && (
        <p className="mb-3 text-xs text-muted-foreground">{t("deploy.noRevision")}</p>
      )}

      {mayWrite && targetRevision > 0 && (
        <div className="mb-3 space-y-2 rounded-lg border border-border bg-card p-3">
          <div className="flex flex-wrap items-center gap-2">
            <Button
              size="sm"
              variant="outline"
              onClick={() => runPreview.mutate()}
              disabled={runPreview.isPending}
            >
              {t("deploy.preview")}
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => runValidate.mutate()}
              // Validation answers a question about a specific revision, and
              // until the preview names one there is nothing to ask about.
              disabled={preview === null || runValidate.isPending}
            >
              {t("deploy.validate")}
            </Button>
            <select
              value={strategy}
              onChange={(e) => setStrategy(e.target.value)}
              aria-label={t("deploy.strategy")}
              className="h-8 rounded-md border border-input bg-background px-2 text-xs"
            >
              <option value="all_at_once">{t("deploy.allAtOnce")}</option>
              <option value="canary">{t("deploy.canary")}</option>
              <option value="staged">{t("deploy.staged")}</option>
              <option value="rolling">{t("deploy.rolling")}</option>
            </select>
            <Button
              size="sm"
              onClick={() => setConfirmingDeploy(true)}
              disabled={deploy.isPending}
            >
              {t("deploy.apply")}
            </Button>
          </div>

          {preview && (
            <dl className="grid grid-cols-2 gap-x-4 gap-y-1 font-mono text-xs">
              <dt className="text-muted-foreground">{t("deploy.currentRevision")}</dt>
              <dd>{formatNumber(preview.current_revision)}</dd>
              <dt className="text-muted-foreground">{t("deploy.targetRevision")}</dt>
              <dd>{formatNumber(preview.target_revision)}</dd>
              <dt className="text-muted-foreground">{t("deploy.documentHash")}</dt>
              {/* Truncated for the eye and selectable in full, because the only
                  use for a hash here is comparing it against another one. */}
              <dd className="select-all truncate" title={preview.target_doc_sha256}>
                {preview.target_doc_sha256.slice(0, 12)}
              </dd>
            </dl>
          )}

          {validation && (
            <div className="text-xs" role="status">
              {validation.valid ? (
                <Badge variant="success">{t("deploy.valid")}</Badge>
              ) : (
                <Badge variant="destructive">{t("deploy.invalid")}</Badge>
              )}
              {validation.conflicts?.map((c) => (
                <p key={c} className="mt-1 text-destructive">
                  {c}
                </p>
              ))}
              {validation.warnings?.map((wm) => (
                <p key={wm} className="mt-1 text-warning">
                  {wm}
                </p>
              ))}
            </div>
          )}

          <MutationError error={runPreview.error ?? runValidate.error ?? deploy.error} />
        </div>
      )}

      {deployments.data && deployments.data.length === 0 && (
        <p className="text-xs text-muted-foreground">{t("deploy.none")}</p>
      )}

      {deployments.data && deployments.data.length > 0 && (
        <table className="w-full border-collapse text-xs">
          <thead>
            <tr className="border-b border-border text-muted-foreground">
              <th className="py-1 pe-3 text-start">{t("deploy.strategy")}</th>
              <th className="pe-3 text-start">{t("deploy.status")}</th>
              <th className="pe-3 text-start">{t("reseller.created")}</th>
              <th className="text-start">{t("actions")}</th>
            </tr>
          </thead>
          <tbody>
            {deployments.data.map((d) => (
              <tr key={d.id} className="border-b border-border/50 align-top">
                <td className="py-1 pe-3 font-mono">{d.strategy}</td>
                <td className="pe-3">
                  <DeploymentStatus status={d.status} />
                  {d.error !== "" && (
                    <p className="mt-0.5 text-destructive">{d.error}</p>
                  )}
                </td>
                <td className="pe-3 font-mono text-muted-foreground">
                  {formatTimestamp(d.created_at)}
                </td>
                <td>
                  {/* Only a finished deployment can be rolled back; one still
                      running has no settled state to return to. */}
                  {mayWrite && !IN_FLIGHT.has(d.status) && d.status !== "rolled_back" && (
                    <button
                      type="button"
                      onClick={() => setRollingBack(d)}
                      className="text-destructive hover:underline"
                    >
                      {t("deploy.rollback")}
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <MutationError error={rollback.error} />

      <ConfirmDialog
        open={confirmingDeploy}
        onOpenChange={setConfirmingDeploy}
        title={t("deploy.confirmApply")}
        description={t("deploy.disruption")}
        confirmLabel={t("deploy.apply")}
        pending={deploy.isPending}
        onConfirm={() => deploy.mutate()}
      />
      <ConfirmDialog
        open={rollingBack !== null}
        onOpenChange={(open) => !open && setRollingBack(null)}
        title={t("deploy.confirmRollback")}
        description={t("deploy.disruption")}
        confirmLabel={t("deploy.rollback")}
        pending={rollback.isPending}
        onConfirm={() => rollingBack && rollback.mutate(rollingBack.id)}
      />
    </section>
  );
}

function DeploymentStatus({ status }: { status: string }) {
  if (status === "completed") return <Badge variant="success">{status}</Badge>;
  if (status === "failed") return <Badge variant="destructive">{status}</Badge>;
  if (status === "rolled_back") return <Badge variant="warning">{status}</Badge>;
  return <Badge variant="outline">{status}</Badge>;
}
