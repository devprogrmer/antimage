import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { MutationError } from "../routes/Resellers";
import { formatNumber, formatTimestamp, t } from "../i18n";
import { Badge } from "./ui/badge";

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
  node_id: number;
  target_revision: number;
  started_at: number;
  completed_at: number | null;
  outcome: string;
  error: string;
  steps: ApplyStep[];
}

interface ApplyRunsTimelineProps {
  nodeId: number;
  limit?: number;
}

/**
 * Apply runs history timeline with per-step results.
 * 
 * Visual timeline showing deployment history with expandable per-step details.
 * Color-coded outcomes, duration metrics, and error messages.
 */
export function ApplyRunsTimeline({ nodeId, limit = 20 }: ApplyRunsTimelineProps) {
  const [expandedRun, setExpandedRun] = useState<number | null>(null);

  const runs = useQuery({
    queryKey: ["node", nodeId, "apply-runs"],
    queryFn: () => api.get<{ runs: ApplyRun[] }>(`/api/v1/nodes/${nodeId}/apply-runs?limit=${limit}`),
    refetchInterval: (query) => {
      // Refetch while any run is in progress
      const hasInProgress = (query.state.data?.runs ?? []).some(
        (r) => !r.completed_at
      );
      return hasInProgress ? 2000 : false;
    },
  });

  const allRuns = runs.data?.runs ?? [];

  if (runs.isLoading) {
    return (
      <div className="py-8 text-center text-sm text-muted-foreground">
        {t("common.loading")}...
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <header className="flex items-center justify-between">
        <h3 className="text-sm font-semibold">{t("node.applyRuns")}</h3>
        <span className="text-xs text-muted-foreground">
          {formatNumber(allRuns.length)} {t("deploy.runs")}
        </span>
      </header>

      <MutationError error={runs.error} />

      {allRuns.length === 0 && (
        <p className="py-8 text-center text-xs text-muted-foreground">
          {t("deploy.noRuns")}
        </p>
      )}

      <div className="relative space-y-2">
        {/* Timeline line */}
        {allRuns.length > 1 && (
          <div className="absolute left-[13px] top-8 bottom-8 w-px bg-border" />
        )}

        {allRuns.map((run, idx) => (
          <TimelineItem
            key={run.id}
            run={run}
            isFirst={idx === 0}
            isLast={idx === allRuns.length - 1}
            expanded={expandedRun === run.id}
            onToggle={() => setExpandedRun(expandedRun === run.id ? null : run.id)}
          />
        ))}
      </div>
    </div>
  );
}

interface TimelineItemProps {
  run: ApplyRun;
  isFirst: boolean;
  isLast: boolean;
  expanded: boolean;
  onToggle: () => void;
}

function TimelineItem({ run, expanded, onToggle }: TimelineItemProps) {
  const inProgress = !run.completed_at;
  const duration = run.completed_at
    ? Math.round((run.completed_at - run.started_at) * 1000)
    : null;

  // Outcome badge color
  const outcomeColor =
    run.outcome === "converged"
      ? "success"
      : run.outcome === "failed"
      ? "destructive"
      : "warning";

  return (
    <div className="relative">
      <div
        className={`flex cursor-pointer gap-3 rounded-lg border p-3 transition-all ${
          expanded
            ? "border-primary bg-accent"
            : "border-border bg-card hover:border-primary/50"
        }`}
        onClick={onToggle}
      >
        {/* Timeline dot */}
        <div className="relative flex h-6 w-6 shrink-0 items-center justify-center">
          <div
            className={`h-3 w-3 rounded-full border-2 ${
              run.outcome === "converged"
                ? "border-success bg-success"
                : run.outcome === "failed"
                ? "border-destructive bg-destructive"
                : "border-warning bg-warning"
            } ${inProgress ? "animate-pulse" : ""}`}
          />
        </div>

        <div className="flex-1 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-xs text-muted-foreground">
              {t("node.revision")} {formatNumber(run.target_revision)}
            </span>
            <Badge variant={outcomeColor} className="text-[10px]">
              {run.outcome}
            </Badge>
            {inProgress && (
              <Badge variant="outline" className="text-[10px]">
                {t("deploy.inProgress")}
              </Badge>
            )}
            {duration !== null && (
              <span className="font-mono text-[10px] text-muted-foreground">
                {formatNumber(duration)}ms
              </span>
            )}
          </div>

          <div className="text-xs text-muted-foreground">
            {formatTimestamp(run.started_at)}
            {run.completed_at && ` → ${formatTimestamp(run.completed_at)}`}
          </div>

          {run.error && (
            <p className="text-xs text-destructive">{run.error}</p>
          )}

          {expanded && run.steps.length > 0 && (
            <div className="mt-3 space-y-1 rounded border border-border bg-background p-2">
              <StepsTable steps={run.steps} />
            </div>
          )}
        </div>

        <div className="shrink-0 text-xs text-muted-foreground">
          {expanded ? "▼" : "▶"}
        </div>
      </div>
    </div>
  );
}

function StepsTable({ steps }: { steps: ApplyStep[] }) {
  return (
    <table className="w-full border-collapse text-[10px]">
      <thead>
        <tr className="border-b border-border text-muted-foreground">
          <th className="py-1 pe-2 text-start">#</th>
          <th className="pe-2 text-start">{t("deploy.step.kind")}</th>
          <th className="pe-2 text-start">{t("deploy.step.disruption")}</th>
          <th className="pe-2 text-start">{t("deploy.step.outcome")}</th>
          <th className="pe-2 text-start">{t("deploy.step.duration")}</th>
        </tr>
      </thead>
      <tbody>
        {steps.map((step) => (
          <tr key={step.seq} className="border-b border-border/50">
            <td className="py-1 pe-2 font-mono text-muted-foreground">
              {formatNumber(step.seq)}
            </td>
            <td className="pe-2 font-mono">{step.kind}</td>
            <td className="pe-2">
              <DisruptionBadge level={step.disruption} />
            </td>
            <td
              className={`pe-2 font-medium ${
                step.outcome === "ok"
                  ? "text-success"
                  : step.outcome === "skipped"
                  ? "text-muted-foreground"
                  : "text-destructive"
              }`}
            >
              {step.outcome}
            </td>
            <td className="pe-2 font-mono text-muted-foreground">
              {formatNumber(step.duration_ms)}ms
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function DisruptionBadge({ level }: { level: string }) {
  const variant =
    level === "none"
      ? "outline"
      : level === "reload"
      ? "secondary"
      : level === "restart"
      ? "warning"
      : "destructive";

  return (
    <Badge variant={variant} className="text-[9px] px-1">
      {level}
    </Badge>
  );
}
