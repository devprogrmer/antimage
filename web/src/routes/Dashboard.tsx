import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import {
  Activity,
  AlertTriangle,
  Clock,
  GitCompareArrows,
  Percent,
  ScrollText,
  Server,
  Users,
} from "lucide-react";

import { api } from "../lib/api";
import { formatNumber, formatRelativeTime, formatTimestamp, t } from "../i18n";
import { can, useSession } from "../lib/session";
import { Card, CardContent, CardHeader, CardTitle } from "../components/ui/card";
import { Badge } from "../components/ui/badge";
import { daysLeft, QuotaBar } from "../components/QuotaBar";

interface DashboardMetrics {
  timestamp: number;
  active_users: number;
  total_subjects: number;
  nodes_online: number;
  nodes_total: number;
  traffic_today_gb: number;
  bandwidth_mbps: number;
  alerts_count: number;
  frozen_count: number;
  nodes: NodeMetric[];
}

interface NodeMetric {
  id: number;
  name: string;
  status: string;
  cpu_percent: number;
  ram_percent: number;
  user_count: number;
}

export function Dashboard() {
  const session = useSession();
  const [metrics, setMetrics] = useState<DashboardMetrics | null>(null);
  const [connected, setConnected] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const eventSource = new EventSource("/api/v1/dashboard/stream");

    eventSource.addEventListener("metrics", (event) => {
      try {
        const data = JSON.parse(event.data);
        setMetrics(data);
        setConnected(true);
        setError(null);
      } catch (err) {
        console.error("Failed to parse metrics:", err);
        setError("Failed to parse metrics data");
      }
    });

    eventSource.addEventListener("heartbeat", () => {
      setConnected(true);
    });

    eventSource.onerror = () => {
      setConnected(false);
      setError("Connection lost. Reconnecting...");
    };

    eventSource.onopen = () => {
      setConnected(true);
      setError(null);
    };

    return () => {
      eventSource.close();
    };
  }, []);

  if (error && !metrics) {
    return (
      <div className="rounded-lg border border-destructive/40 bg-destructive/10 p-4">
        <p className="text-sm text-destructive">{error}</p>
      </div>
    );
  }

  if (!metrics) {
    return (
      <div className="animate-pulse space-y-4">
        <div className="h-6 w-40 rounded bg-muted" />
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-4">
          {[1, 2, 3, 4].map((i) => (
            <div key={i} className="h-24 rounded-lg bg-muted" />
          ))}
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">{t("dashboard.title")}</h2>
        <Badge variant={connected ? "success" : "destructive"}>
          {connected ? t("dashboard.live") : t("dashboard.disconnected")}
        </Badge>
      </div>

      {/* Live snapshot, streamed. */}
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-4">
        <MetricCard
          icon={Users}
          title={t("dashboard.active_users")}
          value={formatNumber(metrics.active_users)}
          total={formatNumber(metrics.total_subjects)}
        />
        <MetricCard
          icon={Server}
          title={t("dashboard.nodes_online")}
          value={formatNumber(metrics.nodes_online)}
          total={formatNumber(metrics.nodes_total)}
        />
        <MetricCard
          icon={Activity}
          title={t("dashboard.traffic_today")}
          value={`${metrics.traffic_today_gb.toFixed(2)} GB`}
          subtitle={`${metrics.bandwidth_mbps.toFixed(1)} Mbps`}
        />
        <MetricCard
          icon={AlertTriangle}
          title={t("dashboard.alerts")}
          value={formatNumber(metrics.alerts_count)}
          subtitle={`${formatNumber(metrics.frozen_count)} ${t("dashboard.frozen")}`}
          tone={metrics.alerts_count > 0 ? "warning" : undefined}
        />
      </div>

      {/* Fleet-wide state antimage already tracks -- reconciliation drift,
          quota pressure, expiries -- but that never reached the dashboard
          before. Each card is a courtesy link to the screen that owns it. */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <FleetSyncCard />
        <QuotaCard />
      </div>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <ExpiringUsersCard />
        {can(session.data, "audit:read") && <RecentActivityCard />}
      </div>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <TrafficChart />
        <TopUsers />
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t("dashboard.nodes")}</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-1 gap-3 md:grid-cols-2 lg:grid-cols-3">
            {metrics.nodes.map((node) => (
              <NodeCard key={node.id} node={node} />
            ))}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}

interface FleetNode {
  id: number;
  name: string;
  desired_revision: number;
  applied_revision: number;
}

/** Whether the fleet's desired configuration has actually been applied --
 *  the antimage-specific question a Rebecca-shaped dashboard has no
 *  equivalent for, since reconciliation is antimage's own architecture. */
function FleetSyncCard() {
  const nodes = useQuery({
    queryKey: ["nodes"],
    queryFn: () => api.get<{ nodes: FleetNode[] }>("/api/v1/nodes"),
  });

  const rows = nodes.data?.nodes ?? [];
  const drifted = rows.filter((n) => n.desired_revision !== n.applied_revision);

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between gap-2 space-y-0">
        <CardTitle className="flex items-center gap-2">
          <GitCompareArrows className="size-4 text-muted-foreground" />
          {t("dashboard.fleetSync")}
        </CardTitle>
        <Link to="/nodes" className="text-xs text-primary hover:underline">
          {t("dashboard.viewAll")}
        </Link>
      </CardHeader>
      <CardContent>
        {nodes.isLoading ? (
          <p className="text-xs text-muted-foreground">{t("loading")}</p>
        ) : rows.length === 0 ? (
          <p className="text-xs text-muted-foreground">{t("node.none")}</p>
        ) : drifted.length === 0 ? (
          <p className="text-sm text-success">
            {t("dashboard.allInSync", { count: formatNumber(rows.length) })}
          </p>
        ) : (
          <div className="space-y-1.5">
            <p className="text-sm text-warning">
              {t("dashboard.nodesDrifted", { count: formatNumber(drifted.length) })}
            </p>
            <ul className="space-y-1">
              {drifted.slice(0, 4).map((n) => (
                <li key={n.id} className="flex items-center justify-between text-xs">
                  <Link to={`/nodes/${n.id}`} className="font-mono hover:underline">
                    {n.name}
                  </Link>
                  <span className="font-mono text-muted-foreground">
                    {formatNumber(n.applied_revision)} / {formatNumber(n.desired_revision)}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

interface QuotaOverview {
  quota: {
    total_bytes: number | null;
    used_bytes: number | null;
    utilization_pct: number | null;
  };
}

function QuotaCard() {
  const overview = useQuery({
    queryKey: ["dashboard", "overview"],
    queryFn: () => api.get<QuotaOverview>("/api/v1/dashboard/overview"),
  });
  const quota = overview.data?.quota;

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Percent className="size-4 text-muted-foreground" />
          {t("dashboard.quotaUsage")}
        </CardTitle>
      </CardHeader>
      <CardContent>
        {overview.isLoading ? (
          <p className="text-xs text-muted-foreground">{t("loading")}</p>
        ) : !quota || quota.total_bytes === null ? (
          <p className="text-xs text-muted-foreground">{t("dashboard.noQuota")}</p>
        ) : (
          <div className="flex items-center gap-3">
            <QuotaBar used={quota.used_bytes ?? 0} total={quota.total_bytes} />
          </div>
        )}
      </CardContent>
    </Card>
  );
}

interface ExpiringSubject {
  id: number;
  name: string;
  expires_at: number | null;
  expired_at: number | null;
}

function ExpiringUsersCard() {
  const subjects = useQuery({
    queryKey: ["subjects"],
    queryFn: () => api.get<{ subjects: ExpiringSubject[] }>("/api/v1/subjects"),
  });

  const soon = (subjects.data?.subjects ?? [])
    .filter((s) => s.expired_at === null && s.expires_at !== null)
    .map((s) => ({ ...s, left: daysLeft(s.expires_at) }))
    .filter((s) => s.left !== null && s.left <= 7)
    .sort((a, b) => (a.left ?? 0) - (b.left ?? 0))
    .slice(0, 5);

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between gap-2 space-y-0">
        <CardTitle className="flex items-center gap-2">
          <Clock className="size-4 text-muted-foreground" />
          {t("dashboard.expiringUsers")}
        </CardTitle>
        <Link to="/subjects" className="text-xs text-primary hover:underline">
          {t("dashboard.viewAll")}
        </Link>
      </CardHeader>
      <CardContent>
        {subjects.isLoading ? (
          <p className="text-xs text-muted-foreground">{t("loading")}</p>
        ) : soon.length === 0 ? (
          <p className="text-xs text-muted-foreground">{t("dashboard.noExpiring")}</p>
        ) : (
          <ul className="space-y-1.5">
            {soon.map((s) => (
              <li key={s.id} className="flex items-center justify-between text-xs">
                <Link to={`/subjects/${s.id}`} className="font-mono hover:underline">
                  {s.name}
                </Link>
                <span className={s.left! <= 1 ? "text-destructive" : "text-warning"}>
                  {formatNumber(s.left ?? 0)} {t("subject.daysLeft")}
                </span>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

interface AuditEntry {
  id: number;
  at: number;
  actor_name: string;
  actor_label: string;
  actor_type: string;
  action: string;
  result: string;
}

function RecentActivityCard() {
  const entries = useQuery({
    queryKey: ["audit", { limit: 6 }],
    queryFn: () => api.get<{ entries: AuditEntry[] }>("/api/v1/audit?limit=6"),
  });

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between gap-2 space-y-0">
        <CardTitle className="flex items-center gap-2">
          <ScrollText className="size-4 text-muted-foreground" />
          {t("dashboard.recentActivity")}
        </CardTitle>
        <Link to="/audit" className="text-xs text-primary hover:underline">
          {t("dashboard.viewAll")}
        </Link>
      </CardHeader>
      <CardContent>
        {entries.isLoading ? (
          <p className="text-xs text-muted-foreground">{t("loading")}</p>
        ) : (entries.data?.entries ?? []).length === 0 ? (
          <p className="text-xs text-muted-foreground">{t("audit.none")}</p>
        ) : (
          <ul className="space-y-1.5">
            {(entries.data?.entries ?? []).map((e) => (
              <li key={e.id} className="flex items-center justify-between gap-2 text-xs">
                <span className="flex min-w-0 items-center gap-2">
                  <ResultDot result={e.result} />
                  <span className="truncate font-mono">{e.action}</span>
                  <span className="truncate text-muted-foreground">
                    {e.actor_name || e.actor_label || e.actor_type}
                  </span>
                </span>
                <span
                  className="shrink-0 font-mono text-[11px] text-muted-foreground"
                  title={formatTimestamp(e.at)}
                >
                  {formatRelativeTime(e.at)}
                </span>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

function ResultDot({ result }: { result: string }) {
  const color =
    result === "ok" ? "bg-success" : result === "denied" ? "bg-warning" : "bg-destructive";
  return <span className={`size-1.5 shrink-0 rounded-full ${color}`} aria-hidden="true" />;
}

interface TrafficPoint {
  timestamp: number;
  uplink_bytes: number;
  downlink_bytes: number;
}

interface TrafficChartResponse {
  period: "24h" | "7d" | "30d";
  granularity: "hour" | "day";
  data_points: TrafficPoint[];
}

/**
 * Traffic over time, drawn as an SVG bar chart to avoid pulling a chart
 * library for two axes and one legend. Bars stack uplink over downlink so
 * total-per-bucket is visible without a second read.
 */
function TrafficChart() {
  const [period, setPeriod] = useState<"24h" | "7d" | "30d">("24h");
  const chart = useQuery({
    queryKey: ["dashboard", "traffic-chart", period],
    queryFn: () =>
      api.get<TrafficChartResponse>(`/api/v1/dashboard/traffic-chart?period=${period}`),
  });

  const points = chart.data?.data_points ?? [];
  const totals = points.map((p) => p.uplink_bytes + p.downlink_bytes);
  const max = Math.max(1, ...totals);

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between gap-2 space-y-0">
        <CardTitle>{t("dashboard.trafficChart")}</CardTitle>
        <select
          value={period}
          onChange={(e) => setPeriod(e.target.value as typeof period)}
          className="rounded border border-input bg-background px-2 py-1 text-xs"
        >
          <option value="24h">{t("dashboard.last24h")}</option>
          <option value="7d">{t("dashboard.last7d")}</option>
          <option value="30d">{t("dashboard.last30d")}</option>
        </select>
      </CardHeader>
      <CardContent>
        {chart.isLoading ? (
          <p className="text-xs text-muted-foreground">{t("loading")}</p>
        ) : points.length === 0 ? (
          <p className="text-xs text-muted-foreground">{t("dashboard.noTraffic")}</p>
        ) : (
          <div
            className="flex h-40 items-end gap-0.5"
            role="img"
            aria-label={t("dashboard.trafficChart")}
          >
            {points.map((p) => {
              const h = Math.max(1, Math.round(((p.uplink_bytes + p.downlink_bytes) / max) * 100));
              return (
                <div
                  key={p.timestamp}
                  className="flex-1 bg-primary/70 transition-colors hover:bg-primary"
                  style={{ height: `${h}%` }}
                  title={`${new Date(p.timestamp * 1000).toLocaleString()}  ↑ ${formatBytes(p.uplink_bytes)}  ↓ ${formatBytes(p.downlink_bytes)}`}
                />
              );
            })}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

interface TopUser {
  subject_id: number;
  subject_name: string;
  total_bytes: number;
  uplink_bytes: number;
  downlink_bytes: number;
}

function TopUsers() {
  const users = useQuery({
    queryKey: ["dashboard", "top-users"],
    queryFn: () => api.get<{ users: TopUser[] }>(`/api/v1/dashboard/top-users?limit=10`),
  });

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("dashboard.topUsers")}</CardTitle>
      </CardHeader>
      <CardContent>
        {users.isLoading ? (
          <p className="text-xs text-muted-foreground">{t("loading")}</p>
        ) : (users.data?.users ?? []).length === 0 ? (
          <p className="text-xs text-muted-foreground">{t("dashboard.noUsers")}</p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-border text-xs text-muted-foreground">
                <th className="py-2 text-start">{t("subject.name")}</th>
                <th className="text-end">{t("dashboard.traffic")}</th>
              </tr>
            </thead>
            <tbody>
              {(users.data?.users ?? []).map((u) => (
                <tr key={u.subject_id} className="border-b border-border/50">
                  <td className="py-1.5 font-mono text-xs">{u.subject_name}</td>
                  <td className="text-end font-mono text-xs">
                    <span title={`↑ ${formatBytes(u.uplink_bytes)} / ↓ ${formatBytes(u.downlink_bytes)}`}>
                      {formatBytes(u.total_bytes)}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </CardContent>
    </Card>
  );
}

function formatBytes(n: number): string {
  if (n === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.floor(Math.log(n) / Math.log(1024));
  return `${(n / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

function MetricCard({
  icon: Icon,
  title,
  value,
  total,
  subtitle,
  tone,
}: {
  icon: typeof Users;
  title: string;
  value: string;
  total?: string;
  subtitle?: string;
  tone?: "warning";
}) {
  return (
    <Card>
      <CardContent className="p-4">
        <div className="mb-2 flex items-center justify-between">
          <span className="text-xs text-muted-foreground">{title}</span>
          <Icon className={`size-4 ${tone === "warning" ? "text-warning" : "text-muted-foreground"}`} />
        </div>
        <div className="text-2xl font-semibold">
          {value}
          {total !== undefined && (
            <span className="text-base font-normal text-muted-foreground"> / {total}</span>
          )}
        </div>
        {subtitle && <div className="mt-0.5 text-xs text-muted-foreground">{subtitle}</div>}
      </CardContent>
    </Card>
  );
}

function NodeCard({ node }: { node: NodeMetric }) {
  const variant =
    node.status === "online" ? "success" : node.status === "degraded" ? "warning" : "destructive";

  return (
    <div className="rounded-lg border border-border bg-muted/40 p-3">
      <div className="mb-2 flex items-center justify-between gap-2">
        <span className="truncate text-sm font-medium">{node.name}</span>
        <Badge variant={variant}>{node.status}</Badge>
      </div>
      <div className="space-y-1 text-xs">
        <div className="flex items-center justify-between">
          <span className="text-muted-foreground">{t("dashboard.users")}</span>
          <span className="font-mono">{formatNumber(node.user_count)}</span>
        </div>
        {node.cpu_percent > 0 && (
          <div className="flex items-center justify-between">
            <span className="text-muted-foreground">{t("dashboard.cpu")}</span>
            <span className="font-mono">{node.cpu_percent.toFixed(1)}%</span>
          </div>
        )}
        {node.ram_percent > 0 && (
          <div className="flex items-center justify-between">
            <span className="text-muted-foreground">{t("dashboard.ram")}</span>
            <span className="font-mono">{node.ram_percent.toFixed(1)}%</span>
          </div>
        )}
      </div>
    </div>
  );
}
