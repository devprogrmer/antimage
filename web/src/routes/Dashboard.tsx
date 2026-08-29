import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { t } from "../i18n";
import { api } from "../lib/api";
import { Card, CardContent, CardHeader, CardTitle } from "../components/ui/card";
import { Badge } from "../components/ui/badge";
import { formatTraffic } from "../components/QuotaBar";
import { Link } from "react-router-dom";

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

interface SubjectStats {
  total: number;
  active: number;
  online: number;
  expired: number;
  disabled: number;
  limited: number;
  expiring_soon: number;
  traffic_used: number;
  traffic_remaining: number;
}

export function Dashboard() {
  const [metrics, setMetrics] = useState<DashboardMetrics | null>(null);
  const [connected, setConnected] = useState(false);

  useEffect(() => {
    const es = new EventSource("/api/v1/dashboard/stream");
    es.addEventListener("metrics", (ev) => {
      try {
        const data = JSON.parse(ev.data);
        setMetrics(data);
        setConnected(true);
      } catch {}
    });
    es.addEventListener("heartbeat", () => setConnected(true));
    es.onerror = () => setConnected(false);
    es.onopen = () => setConnected(true);
    return () => es.close();
  }, []);

  const subjectsStats = useQuery({
    queryKey: ["dashboard-subjects"],
    queryFn: async () => {
      const all = await api.get<{ total: number; subjects: any[] }>("/api/v2/subjects?page=1&page_size=1");
      const active = await api.get<{ total: number }>("/api/v2/subjects?status=active&page=1&page_size=1");
      const expired = await api.get<{ total: number }>("/api/v2/subjects?status=expired&page=1&page_size=1");
      const disabled = await api.get<{ total: number }>("/api/v2/subjects?status=disabled&page=1&page_size=1");
      const online = await api.get<{ total: number }>("/api/v2/subjects?status=online&page=1&page_size=1");
      const limited = await api.get<{ total: number }>("/api/v2/subjects?status=limited&page=1&page_size=1");
      const expiring = await api.get<{ total: number }>("/api/v2/subjects?status=expiring_soon&page=1&page_size=1");
      return {
        total: all.total,
        active: active.total,
        expired: expired.total,
        disabled: disabled.total,
        online: online.total,
        limited: limited.total,
        expiring_soon: expiring.total,
      } as SubjectStats;
    },
  });

  const trafficDaily = useQuery({
    queryKey: ["dashboard-traffic-daily"],
    queryFn: () => api.get<{ points: { ts: number; total: number }[]; daily: { date: string; total: number }[] }>("/api/v1/dashboard/traffic-chart?days=7"),
  });

  const auditRecent = useQuery({
    queryKey: ["dashboard-audit-recent"],
    queryFn: () => api.get<{ entries: any[] }>("/api/v1/audit?limit=10"),
  });

  return (
    <div className="space-y-6 p-1">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-3xl font-bold">{t("dashboard.title")}</h1>
          <p className="text-sm text-muted-foreground">{t("dashboard.subtitle")}</p>
        </div>
        <div className="flex items-center gap-2">
          <div className={`w-2 h-2 rounded-full ${connected ? "bg-green-500" : "bg-red-500"}`} />
          <span className="text-xs text-muted-foreground">{connected ? t("dashboard.live") : t("dashboard.disconnected")}</span>
        </div>
      </div>

      {/* User-oriented KPIs */}
      <div className="grid grid-cols-2 md:grid-cols-4 lg:grid-cols-7 gap-3">
        <KpiCard title={t("dashboard.totalUsers")} value={subjectsStats.data?.total ?? "—"} to="/subjects" color="primary" />
        <KpiCard title={t("dashboard.activeUsers")} value={subjectsStats.data?.active ?? "—"} to="/subjects?status=active" color="green" />
        <KpiCard title={t("dashboard.onlineUsers")} value={subjectsStats.data?.online ?? metrics?.active_users ?? "—"} to="/subjects?status=online" color="green" />
        <KpiCard title={t("dashboard.expiringSoon")} value={subjectsStats.data?.expiring_soon ?? "—"} to="/subjects?status=expiring_soon" color="yellow" />
        <KpiCard title={t("dashboard.expiredUsers")} value={subjectsStats.data?.expired ?? "—"} to="/subjects?status=expired" color="red" />
        <KpiCard title={t("dashboard.disabledUsers")} value={subjectsStats.data?.disabled ?? "—"} to="/subjects?status=disabled" color="gray" />
        <KpiCard title={t("dashboard.limitedUsers")} value={subjectsStats.data?.limited ?? "—"} to="/subjects?status=limited" color="orange" />
      </div>

      <div className="grid md:grid-cols-4 gap-4">
        <Card><CardContent className="p-4"><div className="text-xs text-muted-foreground">{t("dashboard.trafficToday")}</div><div className="text-xl font-bold">{metrics ? `${metrics.traffic_today_gb.toFixed(2)} GB` : "—"}</div><div className="text-xs text-muted-foreground">{metrics ? `${metrics.bandwidth_mbps.toFixed(1)} Mbps` : ""}</div></CardContent></Card>
        <Card><CardContent className="p-4"><div className="text-xs text-muted-foreground">{t("dashboard.activeNodes")}</div><div className="text-xl font-bold">{metrics ? `${metrics.nodes_online} / ${metrics.nodes_total}` : "—"}</div><div className="text-xs">{metrics && metrics.nodes_total - metrics.nodes_online > 0 ? <span className="text-red-500">{metrics.nodes_total - metrics.nodes_online} offline</span> : <span className="text-green-600">All healthy</span>}</div></CardContent></Card>
        <Card><CardContent className="p-4"><div className="text-xs text-muted-foreground">{t("dashboard.alerts")}</div><div className="text-xl font-bold">{metrics?.alerts_count ?? "—"}</div><div className="text-xs text-muted-foreground">{metrics ? `${metrics.frozen_count} frozen` : ""}</div></CardContent></Card>
        <Card><CardContent className="p-4"><div className="text-xs text-muted-foreground">{t("dashboard.trafficUsed")}</div><div className="text-xl font-bold">{trafficDaily.data?.daily ? formatTraffic(trafficDaily.data.daily.reduce((a: any, b: any) => a + (b.total||0), 0)) : trafficDaily.data?.points ? formatTraffic(trafficDaily.data.points.reduce((a: any, b: any) => a + (b.total||0), 0)) : "—"}</div><div className="text-xs text-muted-foreground">Last 7 days</div></CardContent></Card>
      </div>

      <div className="grid lg:grid-cols-3 gap-6">
        {/* Nodes */}
        <Card className="lg:col-span-2">
          <CardHeader><CardTitle className="text-sm">{t("dashboard.nodes")}</CardTitle></CardHeader>
          <CardContent>
            <div className="grid md:grid-cols-2 gap-3">
              {(metrics?.nodes ?? []).map((node) => (
                <div key={node.id} className="border rounded p-3 flex justify-between items-center">
                  <div>
                    <div className="font-medium text-sm">{node.name}</div>
                    <div className="text-xs text-muted-foreground">{node.user_count} users • {node.cpu_percent.toFixed(1)}% CPU • {node.ram_percent.toFixed(1)}% RAM</div>
                  </div>
                  <Badge variant={node.status === "online" ? "default" : node.status === "degraded" ? "outline" : "destructive"}>{node.status}</Badge>
                </div>
              ))}
              {!metrics && <div className="text-sm text-muted-foreground">{t("loading")}</div>}
            </div>
          </CardContent>
        </Card>

        {/* Daily Traffic */}
        <Card>
          <CardHeader><CardTitle className="text-sm">{t("dashboard.dailyTraffic")}</CardTitle></CardHeader>
          <CardContent>
            {!trafficDaily.data || trafficDaily.data.daily.length === 0 ? (
              <div className="text-sm text-muted-foreground">{t("dashboard.noTraffic")}</div>
            ) : (
              <div className="space-y-2">
                {trafficDaily.data.daily.map((d, i) => {
                  const max = Math.max(...trafficDaily.data!.daily.map((x) => x.total), 1);
                  const pct = Math.round((d.total / max) * 100);
                  return (
                    <div key={i} className="flex items-center gap-2 text-xs">
                      <span className="w-20 font-mono">{d.date}</span>
                      <div className="flex-1 h-2 bg-muted rounded overflow-hidden"><div className="h-full bg-primary" style={{ width: `${pct}%` }} /></div>
                      <span className="w-16 font-mono text-right">{formatTraffic(d.total)}</span>
                    </div>
                  );
                })}
              </div>
            )}
          </CardContent>
        </Card>
      </div>

      <div className="grid lg:grid-cols-2 gap-6">
        {/* Recent Activity */}
        <Card>
          <CardHeader><CardTitle className="text-sm">{t("dashboard.recentActivity")}</CardTitle></CardHeader>
          <CardContent>
            {!auditRecent.data || auditRecent.data.entries.length === 0 ? (
              <div className="text-sm text-muted-foreground">{t("dashboard.noActivity")}</div>
            ) : (
              <div className="space-y-2">
                {auditRecent.data.entries.slice(0, 8).map((a: any, i: number) => (
                  <div key={i} className="flex items-center gap-2 text-xs border-b py-1">
                    <Badge variant="outline" className="text-xs">{a.action}</Badge>
                    <span className="font-mono">{a.target_type}#{a.target_id}</span>
                    <span className="text-muted-foreground">{a.actor_type}#{a.actor_id}</span>
                    <span className="text-muted-foreground">{new Date(a.created_at * 1000).toLocaleString()}</span>
                  </div>
                ))}
              </div>
            )}
            <div className="mt-3">
              <Link to="/audit" className="text-xs text-primary hover:underline">{t("dashboard.viewAllAudit")}</Link>
            </div>
          </CardContent>
        </Card>

        {/* Quick Actions */}
        <Card>
          <CardHeader><CardTitle className="text-sm">{t("dashboard.quickActions")}</CardTitle></CardHeader>
          <CardContent className="grid grid-cols-2 gap-2">
            <Link to="/subjects" className="p-3 border rounded hover:bg-muted text-sm font-medium">{t("subjects.create")} →</Link>
            <Link to="/services" className="p-3 border rounded hover:bg-muted text-sm font-medium">{t("services.title")} →</Link>
            <Link to="/nodes" className="p-3 border rounded hover:bg-muted text-sm font-medium">{t("nodes.title")} →</Link>
            <Link to="/traffic" className="p-3 border rounded hover:bg-muted text-sm font-medium">{t("traffic.title")} →</Link>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

function KpiCard({ title, value, to, color }: { title: string; value: string | number; to: string; color: string }) {
  const colorMap: Record<string, string> = {
    primary: "border-primary/30 bg-primary/5",
    green: "border-green-500/30 bg-green-500/5",
    yellow: "border-yellow-500/30 bg-yellow-500/5",
    red: "border-red-500/30 bg-red-500/5",
    gray: "border-border bg-muted/50",
    orange: "border-orange-500/30 bg-orange-500/5",
  };
  return (
    <Link to={to}>
      <Card className={`${colorMap[color] ?? colorMap.gray} hover:opacity-80 transition-opacity`}>
        <CardContent className="p-3">
          <div className="text-xs text-muted-foreground truncate">{title}</div>
          <div className="text-xl font-bold">{value}</div>
        </CardContent>
      </Card>
    </Link>
  );
}
