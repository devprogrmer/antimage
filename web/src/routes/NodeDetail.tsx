import { useQuery } from "@tanstack/react-query";
import { api } from "../lib/api";
import { formatNumber, formatTimestamp, t } from "../i18n";
import { StatusBadge, type NodeStatus } from "../components/StatusBadge";
import { EgressPanel } from "../components/EgressPanel";
import { DeploymentPanel } from "../components/DeploymentPanel";
import { NodeAdapters } from "../components/NodeAdapters";
import { NodeHealth } from "../components/NodeHealth";
import { NodeReconciliation } from "../components/NodeReconciliation";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../components/ui/tabs";
import { InboundStudio } from "../components/InboundStudio";

interface NodeDetailData {
  id: number;
  name: string;
  address: string;
  status: NodeStatus;
  desired_revision: number;
  applied_revision: number;
  online: boolean;
}

interface Revision {
  revision: number;
  created_at: number;
  actor_type: string;
  actor_label: string;
  reason: string;
  sha256: string;
}

interface ApplyStep {
  seq: number;
  kind: string;
  disruption: string;
  outcome: string;
  error: string;
  duration_ms: number;
}

interface ApplyRun {
  id: number;
  target_revision: number;
  started_at: number;
  outcome: string;
  steps: ApplyStep[];
}

/** The screen the whole design exists to make possible: desired versus applied
 *  revision, drift, who changed what and why, and the last apply run expanded
 *  to per-step results with disruption level and the failing step's stderr. */
export function NodeDetail({ nodeId }: { nodeId: number }) {
  const node = useQuery({
    queryKey: ["node", nodeId],
    queryFn: () => api.get<NodeDetailData>(`/api/v1/nodes/${nodeId}`),
  });
  const revisions = useQuery({
    queryKey: ["node", nodeId, "revisions"],
    queryFn: () => api.get<{ revisions: Revision[] }>(`/api/v1/nodes/${nodeId}/revisions`),
  });
  const runs = useQuery({
    queryKey: ["node", nodeId, "runs"],
    queryFn: () => api.get<{ runs: ApplyRun[] }>(`/api/v1/nodes/${nodeId}/apply-runs`),
  });

  if (!node.data) return null;
  // Derived here rather than read from the row: the summary DTO has no drift
  // field, and this is the same comparison the server makes on the stream.
  const drift = node.data.applied_revision !== node.data.desired_revision;

  return (
    <div className="space-y-6 p-4 text-sm text-foreground">
      <header className="flex items-center gap-3">
        <h2 className="font-mono text-base">{node.data.name}</h2>
        <StatusBadge status={node.data.status} />
        <span className="font-mono text-xs text-muted-foreground">{node.data.address}</span>
      </header>

      <section className="flex gap-6 border-y border-border py-2 font-mono text-xs">
        <span>
          {t("node.revision")}: {formatNumber(node.data.applied_revision)} /{" "}
          {formatNumber(node.data.desired_revision)}
        </span>
        {drift && <span className="text-warning">{t("node.drift")}</span>}
      </section>

      <Tabs defaultValue="overview">
        <TabsList>
          <TabsTrigger value="overview">{t("node.tabOverview")}</TabsTrigger>
          <TabsTrigger value="inbounds">{t("studio.title")}</TabsTrigger>
          <TabsTrigger value="egress">{t("egress.title")}</TabsTrigger>
          <TabsTrigger value="deployments">{t("deploy.title")}</TabsTrigger>
          <TabsTrigger value="adapters">{t("node.adapters")}</TabsTrigger>
          <TabsTrigger value="health">{t("health.tab")}</TabsTrigger>
          <TabsTrigger value="history">{t("node.tabHistory")}</TabsTrigger>
        </TabsList>

        <TabsContent value="overview">
          <NodeReconciliation nodeId={nodeId} />
        </TabsContent>

        <TabsContent value="inbounds">
          <InboundStudio nodeId={nodeId} />
        </TabsContent>

        <TabsContent value="egress">
          <EgressPanel nodeId={nodeId} />
        </TabsContent>

        <TabsContent value="deployments">
          <DeploymentPanel
            nodeId={nodeId}
            targetRevision={node.data.desired_revision}
          />
        </TabsContent>

        <TabsContent value="adapters">
          <NodeAdapters nodeId={nodeId} />
        </TabsContent>

        <TabsContent value="health">
          <NodeHealth nodeId={nodeId} />
        </TabsContent>

        <TabsContent value="history" className="space-y-6">
      <section>
        <h3 className="mb-1 text-xs uppercase tracking-wide text-muted-foreground">
          {t("node.revisions")}
        </h3>
        <table className="w-full border-collapse font-mono text-xs">
          <tbody>
            {revisions.data?.revisions.map((rev) => (
              <tr key={rev.revision} className="border-b border-border">
                <td className="py-1 pe-3 text-muted-foreground">{formatNumber(rev.revision)}</td>
                <td className="pe-3 text-muted-foreground">{formatTimestamp(rev.created_at)}</td>
                <td className="pe-3">{rev.actor_label || rev.actor_type}</td>
                <td className="pe-3 text-muted-foreground">{rev.reason}</td>
                <td className="text-muted-foreground">{rev.sha256.slice(0, 12)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>

      <section>
        <h3 className="mb-1 text-xs uppercase tracking-wide text-muted-foreground">
          {t("node.applyRuns")}
        </h3>
        {runs.data?.runs.map((run) => (
          <details key={run.id} className="border-b border-border py-1">
            <summary className="cursor-pointer font-mono text-xs">
              <span className="text-muted-foreground">{formatNumber(run.target_revision)}</span>{" "}
              <span className="text-muted-foreground">{formatTimestamp(run.started_at)}</span>{" "}
              <span
                className={run.outcome === "converged" ? "text-success" : "text-warning"}
              >
                {run.outcome}
              </span>
            </summary>
            <table className="mt-1 w-full border-collapse font-mono text-[11px]">
              <tbody>
                {run.steps.map((step) => (
                  <tr key={step.seq} className="border-t border-border">
                    <td className="py-0.5 pe-3 text-muted-foreground">{formatNumber(step.seq)}</td>
                    <td className="pe-3">{step.kind}</td>
                    <td className="pe-3 text-muted-foreground">{step.disruption}</td>
                    <td
                      className={`pe-3 ${
                        step.outcome === "ok" ? "text-success" : "text-destructive"
                      }`}
                    >
                      {step.outcome}
                    </td>
                    <td className="pe-3 text-muted-foreground">
                      {formatNumber(step.duration_ms)}
                      {t("unit.ms")}
                    </td>
                    {/* The step's stderr, verbatim: the whole point of keeping
                        it is that the operator reads what the tool said. */}
                    <td className="whitespace-pre-wrap text-destructive">{step.error}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </details>
        ))}
      </section>
        </TabsContent>
      </Tabs>
    </div>
  );
}
