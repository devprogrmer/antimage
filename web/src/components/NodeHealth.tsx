import { useQuery } from "@tanstack/react-query";

import { api } from "../lib/api";
import { MutationError } from "../routes/Resellers";
import { formatNumber, formatTimestamp, t } from "../i18n";

interface Metrics {
  reconnect_count: number;
  last_reconcile_duration_ms: number | null;
  failed_reconcile_streak: number;
  avg_rtt_ms: number | null;
}

interface HealthSample {
  timestamp: number;
  cpu_percent: number;
  memory_used_bytes: number;
  memory_total_bytes: number;
  disk_used_bytes: number;
  disk_total_bytes: number;
  network_rx_bytes: number;
  network_tx_bytes: number;
  active_connections: number;
  latency_ms: number;
}

/**
 * The node's control-plane counters and its recorded health history.
 *
 * Two endpoints with no client. They answer different questions: the counters
 * are about the node's relationship with the panel -- how often it reconnects,
 * how long a reconcile takes, how many have failed in a row -- and the history
 * is about the host itself.
 *
 * failed_reconcile_streak is the number that matters most and is the easiest to
 * miss in a list, so it is called out when it is non-zero: a node reconnecting
 * happily while failing every reconcile looks healthy from every other angle.
 */
export function NodeHealth({ nodeId }: { nodeId: number }) {
  const metrics = useQuery({
    queryKey: ["node", nodeId, "metrics"],
    queryFn: () => api.get<Metrics>(`/api/v1/nodes/${nodeId}/metrics`),
  });
  const history = useQuery({
    queryKey: ["node", nodeId, "health-history"],
    queryFn: () =>
      api.get<{ metrics: HealthSample[]; count: number }>(
        `/api/v1/nodes/${nodeId}/health/history?limit=100`,
      ),
  });

  const samples = history.data?.metrics ?? [];
  const latest = samples.length > 0 ? samples[samples.length - 1] : null;

  return (
    <div className="space-y-6">
      <section>
        <h3 className="mb-2 text-xs uppercase tracking-wide text-muted-foreground">
          {t("health.controlPlane")}
        </h3>
        <MutationError error={metrics.error} />
        {metrics.data && (
          <dl className="grid grid-cols-2 gap-x-6 gap-y-1 text-xs sm:grid-cols-4">
            <dt className="text-muted-foreground">{t("health.reconnects")}</dt>
            <dd className="font-mono">{formatNumber(metrics.data.reconnect_count)}</dd>

            <dt className="text-muted-foreground">{t("health.reconcileMs")}</dt>
            <dd className="font-mono">
              {metrics.data.last_reconcile_duration_ms === null
                ? t("node.never")
                : formatNumber(metrics.data.last_reconcile_duration_ms) + t("unit.ms")}
            </dd>

            <dt className="text-muted-foreground">{t("health.rtt")}</dt>
            <dd className="font-mono">
              {metrics.data.avg_rtt_ms === null
                ? t("node.never")
                : formatNumber(metrics.data.avg_rtt_ms) + t("unit.ms")}
            </dd>

            <dt className="text-muted-foreground">{t("health.failedStreak")}</dt>
            {/* Called out when non-zero. A node that reconnects happily while
                failing every reconcile looks healthy from every other angle. */}
            <dd
              className={
                metrics.data.failed_reconcile_streak > 0
                  ? "font-mono font-semibold text-destructive"
                  : "font-mono"
              }
            >
              {formatNumber(metrics.data.failed_reconcile_streak)}
            </dd>
          </dl>
        )}
      </section>

      <section>
        <h3 className="mb-2 text-xs uppercase tracking-wide text-muted-foreground">
          {t("health.host")}
        </h3>
        <MutationError error={history.error} />
        {samples.length === 0 && (
          // Distinct from "the node is unhealthy": nothing has been recorded,
          // which usually means the agent has not reported yet.
          <p className="text-xs text-muted-foreground">{t("health.noHistory")}</p>
        )}

        {latest && (
          <>
            <dl className="grid grid-cols-2 gap-x-6 gap-y-1 text-xs sm:grid-cols-4">
              <dt className="text-muted-foreground">{t("dashboard.cpu")}</dt>
              <dd className="font-mono">{`${latest.cpu_percent.toFixed(1)}%`}</dd>
              <dt className="text-muted-foreground">{t("dashboard.ram")}</dt>
              <dd className="font-mono">{percent(latest.memory_used_bytes, latest.memory_total_bytes)}</dd>
              <dt className="text-muted-foreground">{t("health.disk")}</dt>
              <dd className="font-mono">{percent(latest.disk_used_bytes, latest.disk_total_bytes)}</dd>
              <dt className="text-muted-foreground">{t("health.connections")}</dt>
              <dd className="font-mono">{formatNumber(latest.active_connections)}</dd>
            </dl>

            <div className="mt-3 space-y-2">
              <Sparkline
                label={t("dashboard.cpu")}
                values={samples.map((s) => s.cpu_percent)}
                suffix="%"
              />
              <Sparkline
                label={t("health.latency")}
                values={samples.map((s) => s.latency_ms)}
                suffix={t("unit.ms")}
              />
            </div>

            <p className="mt-2 font-mono text-[11px] text-muted-foreground">
              {t("health.lastSample")}: {formatTimestamp(latest.timestamp)}
            </p>
          </>
        )}
      </section>
    </div>
  );
}

/** percent renders used/total, or a dash when the total is unknown. */
function percent(used: number, total: number): string {
  // Dividing by a zero total would render NaN%, which reads as a broken panel
  // rather than as a node that has not reported its memory size.
  if (!total) return "—";
  return ((used / total) * 100).toFixed(1) + "%";
}

/**
 * A minimal sparkline.
 *
 * An inline SVG rather than a charting dependency: this is one polyline over a
 * fixed viewBox, and the smallest chart library in the ecosystem is larger than
 * the rest of this component put together. It carries its range in text so the
 * shape is not the only way to read it.
 */
function Sparkline({
  label,
  values,
  suffix,
}: {
  label: string;
  values: number[];
  suffix: string;
}) {
  if (values.length < 2) return null;
  const min = Math.min(...values);
  const max = Math.max(...values);
  // A flat series has no range to scale against; drawing it down the middle is
  // truer than dividing by zero and getting a line at the top.
  const span = max - min || 1;
  const points = values
    .map((v, i) => {
      const x = (i / (values.length - 1)) * 100;
      const y = 20 - ((v - min) / span) * 20;
      return `${x.toFixed(2)},${y.toFixed(2)}`;
    })
    .join(" ");

  return (
    <div className="flex items-center gap-2">
      <span className="w-20 shrink-0 text-xs text-muted-foreground">{label}</span>
      <svg
        viewBox="0 0 100 20"
        preserveAspectRatio="none"
        className="h-6 flex-1"
        role="img"
        aria-label={`${label} ${min.toFixed(1)}${suffix} – ${max.toFixed(1)}${suffix}`}
      >
        <polyline
          points={points}
          fill="none"
          stroke="currentColor"
          strokeWidth="1"
          vectorEffect="non-scaling-stroke"
          className="text-primary"
        />
      </svg>
      <span className="w-24 shrink-0 text-end font-mono text-[11px] text-muted-foreground">
        {min.toFixed(1)}–{max.toFixed(1)}
        {suffix}
      </span>
    </div>
  );
}
