import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../lib/api";
import { formatNumber, formatTimestamp, t } from "../i18n";

interface FleetSummary {
  total_nodes: number;
  by_status: Record<string, number>;
  active_alerts: Record<string, number>;
  avg_fleet_rtt_ms: number | null;
  nodes_with_issues: number;
}

interface Alert {
  id: number;
  alert_type: string;
  severity: "info" | "warning" | "critical";
  target_type: string;
  target_id: number;
  state: string;
  first_seen_at: string;
  last_seen_at: string;
  resolved_at: string | null;
  threshold_value: string;
  current_value: string;
  metadata: Record<string, unknown>;
}

interface AlertsResponse {
  alerts: Alert[];
  total: number;
  limit: number;
  offset: number;
}

interface HistoryDataPoint {
  timestamp: string;
  value?: number;
  avg?: number;
  min?: number;
  max?: number;
  samples?: number;
}

interface NodeHistoryResponse {
  metric: string;
  granularity: string;
  node_id: number;
  data: HistoryDataPoint[];
  total: number;
}

const severityColors: Record<string, string> = {
  info: "border-primary text-primary",
  warning: "border-warning text-warning",
  critical: "border-destructive text-destructive",
};

export function Observability() {
  const [timeRange, setTimeRange] = useState<"1h" | "6h" | "24h" | "7d">("24h");
  const [alertFilter, setAlertFilter] = useState<"all" | "critical">("all");

  const fleet = useQuery({
    queryKey: ["fleet-summary"],
    queryFn: () => api.get<FleetSummary>("/api/v1/fleet/summary"),
    refetchInterval: 30000,
  });

  const alerts = useQuery({
    queryKey: ["alerts", alertFilter],
    queryFn: () =>
      api.get<AlertsResponse>(
        `/api/v1/alerts?state=active${alertFilter === "critical" ? "&severity=critical" : ""}&limit=50`
      ),
    refetchInterval: 15000,
  });

  const fleetRTT = useQuery({
    queryKey: ["fleet-rtt", timeRange],
    queryFn: () =>
      api.get<NodeHistoryResponse>(
        `/api/v1/nodes/1/history?metric=rtt&granularity=${timeRange === "1h" ? "raw" : "hourly"}&limit=100`
      ),
    enabled: (fleet.data?.total_nodes ?? 0) > 0,
    refetchInterval: 60000,
  });

  const summary = fleet.data;
  const totalNodes = summary?.total_nodes ?? 0;
  const onlineNodes = summary?.by_status?.online ?? 0;
  const degradedNodes = summary?.by_status?.degraded ?? 0;
  const offlineNodes = summary?.by_status?.offline ?? 0;
  const integrityNodes = summary?.by_status?.integrity ?? 0;
  const criticalAlerts = summary?.active_alerts?.critical ?? 0;
  const warningAlerts = summary?.active_alerts?.warning ?? 0;
  const avgRTT = summary?.avg_fleet_rtt_ms;
  const nodesWithIssues = summary?.nodes_with_issues ?? 0;

  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between">
        <h2 className="font-mono text-lg font-semibold text-foreground">
          {t("observability.title")}
        </h2>
        <div className="flex gap-2">
          {(["1h", "6h", "24h", "7d"] as const).map((range) => (
            <button
              key={range}
              type="button"
              onClick={() => setTimeRange(range)}
              className={`rounded-full px-3 py-1 text-xs font-medium ${
                timeRange === range
                  ? "bg-primary/20 text-primary"
                  : "bg-card text-muted-foreground hover:text-foreground"
              }`}
            >
              {range}
            </button>
          ))}
        </div>
      </div>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          label={t("observability.totalNodes")}
          value={formatNumber(totalNodes)}
          detail={
            onlineNodes > 0
              ? `${formatNumber(onlineNodes)} ${t("observability.online")}`
              : undefined
          }
          severity="info"
        />
        <StatCard
          label={t("observability.nodesWithIssues")}
          value={formatNumber(nodesWithIssues)}
          detail={
            degradedNodes + offlineNodes + integrityNodes > 0
              ? `${formatNumber(degradedNodes)}/${formatNumber(offlineNodes)}/${formatNumber(integrityNodes)}`
              : undefined
          }
          severity={nodesWithIssues > 0 ? "warning" : "ok"}
        />
        <StatCard
          label={t("observability.activeAlerts")}
          value={formatNumber(criticalAlerts + warningAlerts)}
          detail={
            criticalAlerts > 0
              ? `${formatNumber(criticalAlerts)} ${t("observability.critical")}`
              : undefined
          }
          severity={criticalAlerts > 0 ? "critical" : warningAlerts > 0 ? "warning" : "ok"}
        />
        <StatCard
          label={t("observability.avgFleetRTT")}
          value={avgRTT != null ? `${formatNumber(avgRTT)} ms` : "—"}
          severity="info"
        />
      </div>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <section className="rounded-xl border border-border bg-[#0A0D12] p-4">
          <div className="mb-3 flex items-center justify-between">
            <h3 className="font-mono text-sm font-semibold text-foreground">
              {t("observability.fleetStatus")}
            </h3>
          </div>
          <div className="space-y-2">
            <StatusRow label={t("status.online")} count={onlineNodes} severity="ok" />
            <StatusRow label={t("status.degraded")} count={degradedNodes} severity="warning" />
            <StatusRow label={t("status.offline")} count={offlineNodes} severity="warn" />
            <StatusRow label={t("status.integrity")} count={integrityNodes} severity="alert" />
          </div>
        </section>

        <section className="rounded-xl border border-border bg-[#0A0D12] p-4">
          <div className="mb-3 flex items-center justify-between">
            <h3 className="font-mono text-sm font-semibold text-foreground">
              {t("observability.fleetRTT")}
            </h3>
          </div>
          {fleetRTT.data && fleetRTT.data.data.length > 0 ? (
            <MiniSparkline data={fleetRTT.data.data} />
          ) : (
            <div className="flex h-24 items-center justify-center text-xs text-muted-foreground">
              {t("observability.noData")}
            </div>
          )}
        </section>
      </div>

      <section className="rounded-xl border border-border bg-[#0A0D12] p-4">
        <div className="mb-3 flex items-center justify-between">
          <h3 className="font-mono text-sm font-semibold text-foreground">
            {t("observability.activeAlerts")}
          </h3>
          <div className="flex gap-2">
            <button
              type="button"
              onClick={() => setAlertFilter("all")}
              className={`rounded-full px-2 py-0.5 text-xs ${
                alertFilter === "all"
                  ? "bg-secondary text-foreground"
                  : "text-muted-foreground hover:text-foreground"
              }`}
            >
              {t("observability.all")}
            </button>
            <button
              type="button"
              onClick={() => setAlertFilter("critical")}
              className={`rounded-full px-2 py-0.5 text-xs ${
                alertFilter === "critical"
                  ? "bg-secondary text-foreground"
                  : "text-muted-foreground hover:text-foreground"
              }`}
            >
              {t("observability.criticalOnly")}
            </button>
          </div>
        </div>

        {alerts.data && alerts.data.alerts.length > 0 ? (
          <div className="space-y-2">
            {alerts.data.alerts.map((alert) => (
              <div
                key={alert.id}
                className="flex items-start gap-3 rounded-lg border border-border/50 bg-background p-3 text-xs"
              >
                <span
                  className={`inline-block rounded-full border px-2 py-0.5 font-mono text-[10px] uppercase ${
                    severityColors[alert.severity] ?? "border-border text-muted-foreground"
                  }`}
                >
                  {alert.severity}
                </span>
                <div className="flex-1 space-y-1">
                  <div className="font-mono text-foreground">{alert.alert_type}</div>
                  <div className="text-muted-foreground">
                    {alert.target_type} #{alert.target_id}
                  </div>
                  <div className="text-muted-foreground">
                    {t("observability.firstSeen")}: {formatTimestamp(new Date(alert.first_seen_at).getTime() / 1000)}
                  </div>
                </div>
                <div className="text-end text-muted-foreground">
                  <div>{t("observability.current")}: {alert.current_value}</div>
                  <div>{t("observability.threshold")}: {alert.threshold_value}</div>
                </div>
              </div>
            ))}
          </div>
        ) : (
          <div className="flex h-24 items-center justify-center text-xs text-muted-foreground">
            {t("observability.noActiveAlerts")}
          </div>
        )}
      </section>
    </div>
  );
}

function StatCard({
  label,
  value,
  detail,
  severity,
}: {
  label: string;
  value: string;
  detail?: string;
  severity: "ok" | "info" | "warning" | "critical";
}) {
  const colors = {
    ok: "border-success/30 bg-success/10",
    info: "border-border bg-[#0A0D12]",
    warning: "border-warning/30 bg-warning/10",
    critical: "border-destructive/30 bg-destructive/10",
  };

  return (
    <div className={`rounded-xl border p-4 ${colors[severity]}`}>
      <div className="text-xs uppercase tracking-wide text-muted-foreground">{label}</div>
      <div className="mt-1 font-mono text-2xl font-semibold text-foreground">{value}</div>
      {detail && <div className="mt-1 text-xs text-muted-foreground">{detail}</div>}
    </div>
  );
}

function StatusRow({
  label,
  count,
  severity,
}: {
  label: string;
  count: number;
  severity: "ok" | "warning" | "warn" | "alert";
}) {
  const colors = {
    ok: "bg-success",
    warning: "bg-warning",
    warn: "bg-warning",
    alert: "bg-destructive",
  };

  const total = 100;
  const percentage = total > 0 ? (count / total) * 100 : 0;

  return (
    <div className="flex items-center gap-3">
      <div className="w-24 text-xs text-muted-foreground">{label}</div>
      <div className="flex-1">
        <div className="h-2 overflow-hidden rounded-full bg-card">
          <div
            className={`h-full ${colors[severity]}`}
            style={{ width: `${Math.min(percentage, 100)}%` }}
          />
        </div>
      </div>
      <div className="w-12 text-end font-mono text-xs text-foreground">{formatNumber(count)}</div>
    </div>
  );
}

function MiniSparkline({ data }: { data: HistoryDataPoint[] }) {
  const values = data.map((d) => d.avg ?? d.value ?? 0);
  const max = Math.max(...values);
  const min = Math.min(...values);
  const range = max - min || 1;

  const points = values
    .map((v, i) => {
      const x = (i / (values.length - 1)) * 100;
      const y = 100 - ((v - min) / range) * 100;
      return `${x},${y}`;
    })
    .join(" ");

  return (
    <div className="relative h-24">
      <svg viewBox="0 0 100 100" preserveAspectRatio="none" className="h-full w-full">
        <polyline
          points={points}
          fill="none"
          stroke="rgb(34 211 238)"
          strokeWidth="2"
          vectorEffect="non-scaling-stroke"
        />
      </svg>
      <div className="absolute inset-x-0 bottom-0 flex justify-between text-[10px] text-muted-foreground">
        <span>{formatNumber(min)} ms</span>
        <span>{formatNumber(max)} ms</span>
      </div>
    </div>
  );
}
