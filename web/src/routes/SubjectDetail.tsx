import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../lib/api";
import { formatNumber, formatTimestamp, t } from "../i18n";
import { QuotaBar, formatTraffic } from "../components/QuotaBar";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "../components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../components/ui/tabs";
import { Badge } from "../components/ui/badge";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { Link } from "react-router-dom";

interface Subject {
  id: number;
  name: string;
  enabled: boolean;
  expires_at: number | null;
  expired_at: number | null;
  created_at: number;
  note: string;
  quota_bytes: number | null;
  quota_used_bytes: number;
  frozen: boolean;
  max_devices: number | null;
  max_ips: number | null;
  max_connections: number | null;
  auto_delete_in_days: number | null;
  data_limit_reset_strategy: string;
  status: string | null;
  lifetime_used_bytes: number;
  telegram_id: string | null;
  contact_number: string | null;
  last_online_at: number | null;
  is_online: boolean;
  owner_admin_id: number | null;
  primary_service_id: number | null;
  remaining_bytes: number | null;
  remaining_days: number | null;
  service_ids?: number[];
  node_ids?: number[];
}

export function SubjectDetail({ subjectId }: { subjectId: number }) {
  const queryClient = useQueryClient();
  const [activeTab, setActiveTab] = useState("overview");
  const [showCred, setShowCred] = useState<string | null>(null);
  const [credValue, setCredValue] = useState("");
  const [copied, setCopied] = useState<string | null>(null);
  const [addTrafficGB, setAddTrafficGB] = useState(5);
  const [addDays, setAddDays] = useState(30);
  const [showDelete, setShowDelete] = useState(false);
  const [showResetTraffic, setShowResetTraffic] = useState(false);
  const [showRevokeSub, setShowRevokeSub] = useState(false);

  const subject = useQuery({
    queryKey: ["subject", subjectId],
    queryFn: () => api.get<Subject>(`/api/v1/subjects/${subjectId}`),
  });

  const subscription = useQuery({
    queryKey: ["subject-sub", subjectId],
    queryFn: () => api.get<{ url: string; clash_url: string; singbox_url: string; v2ray_url: string; qr_url: string }>(`/api/v1/subjects/${subjectId}/subscription`),
  });

  const traffic = useQuery({
    queryKey: ["subject-traffic", subjectId],
    queryFn: () => api.get<{ hourly: any[]; daily: any[]; node_breakdown: any[]; quota_bytes: number | null; quota_used_bytes: number; lifetime_used: number; total: number }>(`/api/v1/subjects/${subjectId}/traffic`),
    enabled: activeTab === "traffic",
  });

  const ips = useQuery({
    queryKey: ["subject-ips", subjectId],
    queryFn: () => api.get<{ connections: any[] }>(`/api/v1/subjects/${subjectId}/ips`),
    enabled: activeTab === "connections",
  });

  const activity = useQuery({
    queryKey: ["subject-activity", subjectId],
    queryFn: () => api.get<{ events: any[]; devices: any[] }>(`/api/v1/subjects/${subjectId}/activity`),
    enabled: activeTab === "activity",
  });

  const audit = useQuery({
    queryKey: ["subject-audit", subjectId],
    queryFn: () => api.get<{ audit: any[] }>(`/api/v1/subjects/${subjectId}/audit`),
    enabled: activeTab === "audit",
  });

  const devices = useQuery({
    queryKey: ["devices", subjectId],
    queryFn: () => api.get<{ devices: { id: number; fingerprint: string; last_seen_at: number | null; last_ip: string }[] }>(`/api/v1/subjects/${subjectId}/devices`),
  });

  const revokeSub = useMutation({
    mutationFn: () => api.post(`/api/v1/subjects/${subjectId}/subscription/revoke`, {}),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["subject-sub", subjectId] });
      setShowRevokeSub(false);
    },
  });

  const addTrafficMut = useMutation({
    mutationFn: (gb: number) => api.post(`/api/v1/subjects/${subjectId}/add-traffic`, { gb }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["subject", subjectId] }),
  });

  const addDaysMut = useMutation({
    mutationFn: (days: number) => api.post(`/api/v1/subjects/${subjectId}/add-days`, { days }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["subject", subjectId] }),
  });

  const resetTrafficMut = useMutation({
    mutationFn: () => api.post(`/api/v1/subjects/${subjectId}/reset-traffic`, {}),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["subject", subjectId] });
      setShowResetTraffic(false);
    },
  });

  const enableMut = useMutation({
    mutationFn: () => api.post(`/api/v1/subjects/${subjectId}/enable`, {}),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["subject", subjectId] }),
  });

  const disableMut = useMutation({
    mutationFn: () => api.post(`/api/v1/subjects/${subjectId}/disable`, {}),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["subject", subjectId] }),
  });

  const deleteMut = useMutation({
    mutationFn: () => api.del(`/api/v1/subjects/${subjectId}`),
    onSuccess: () => { window.location.href = "/subjects"; },
  });

  const rotateCred = useMutation({
    mutationFn: (kind: string) => api.post(`/api/v1/subjects/${subjectId}/credentials/${kind}/rotate`, {}),
    onSuccess: (data: any) => {
      setCredValue(data.value);
      setShowCred(data.kind);
    },
  });

  const revealCred = async (kind: string) => {
    const resp = await api.get<{ kind: string; value: string }>(`/api/v1/subjects/${subjectId}/credentials/${kind}`);
    setCredValue(resp.value);
    setShowCred(kind);
  };

  const copyText = async (text: string, key: string) => {
    await navigator.clipboard.writeText(text);
    setCopied(key);
    setTimeout(() => setCopied(null), 1500);
  };

  if (!subject.data) return <div className="p-8">{t("loading")}</div>;
  const s = subject.data;

  return (
    <div className="space-y-6 max-w-6xl mx-auto">
      {/* Header */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <Link to="/subjects" className="text-sm text-muted-foreground hover:text-foreground">← {t("subjects.title")}</Link>
          <h1 className="text-2xl font-bold font-mono">{s.name}</h1>
          <Badge variant={s.enabled ? "default" : "secondary"}>{s.enabled ? t("subject.enabled") : t("subject.disabled")}</Badge>
          {s.is_online && <Badge className="bg-green-500">● {t("subject.online")}</Badge>}
          {s.frozen && <Badge variant="destructive">{t("filters.frozen")}</Badge>}
        </div>
        <div className="flex flex-wrap gap-2">
          {s.enabled ? (
            <Button size="sm" variant="destructive" onClick={() => disableMut.mutate()}>{t("subject.disable")}</Button>
          ) : (
            <Button size="sm" onClick={() => enableMut.mutate()}>{t("subject.enable")}</Button>
          )}
          <Button size="sm" variant="outline" onClick={() => setShowResetTraffic(true)}>{t("subject.resetTraffic")}</Button>
          <Button size="sm" variant="outline" onClick={() => setShowRevokeSub(true)}>{t("subject.regenSub")}</Button>
          <Button size="sm" variant="destructive" onClick={() => setShowDelete(true)}>{t("delete")}</Button>
        </div>
      </div>

      {/* Quick Stats */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
        <Card><CardContent className="p-3"><div className="text-xs text-muted-foreground">{t("subject.trafficUsed")}</div><div className="font-mono font-bold">{formatTraffic(s.quota_used_bytes)}</div><QuotaBar used={s.quota_used_bytes} total={s.quota_bytes} /></CardContent></Card>
        <Card><CardContent className="p-3"><div className="text-xs text-muted-foreground">{t("subject.remaining")}</div><div className="font-mono font-bold">{s.remaining_bytes ? formatTraffic(s.remaining_bytes) : t("subject.unlimited")}</div><div className="text-xs">{s.quota_bytes ? `${Math.round((s.quota_used_bytes / s.quota_bytes) * 100)}% used` : "Unlimited"}</div></CardContent></Card>
        <Card><CardContent className="p-3"><div className="text-xs text-muted-foreground">{t("subject.expiry")}</div><div className="font-mono text-sm">{formatTimestamp(s.expires_at)}</div><div className={`text-xs ${s.remaining_days !== null && s.remaining_days <= 3 ? "text-orange-500" : ""}`}>{s.remaining_days !== null ? `${formatNumber(s.remaining_days)} ${t("subject.daysLeft")}` : t("subject.never")}</div></CardContent></Card>
        <Card><CardContent className="p-3"><div className="text-xs text-muted-foreground">{t("subject.lastOnline")}</div><div className="font-mono text-sm">{formatTimestamp(s.last_online_at)}</div><div className="text-xs">{s.is_online ? t("subject.online") : t("subject.offline")}</div></CardContent></Card>
      </div>

      {/* Tabs */}
      <Tabs value={activeTab} onValueChange={setActiveTab}>
        <TabsList className="flex flex-wrap h-auto">
          <TabsTrigger value="overview">{t("subject.overview")}</TabsTrigger>
          <TabsTrigger value="credentials">{t("subject.credentials")}</TabsTrigger>
          <TabsTrigger value="subscription">{t("subject.subscription")}</TabsTrigger>
          <TabsTrigger value="traffic">{t("subject.traffic")}</TabsTrigger>
          <TabsTrigger value="connections">{t("subject.connections")}</TabsTrigger>
          <TabsTrigger value="activity">{t("subject.activity")}</TabsTrigger>
          <TabsTrigger value="audit">{t("subject.audit")}</TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="space-y-4 mt-4">
          <div className="grid md:grid-cols-2 gap-4">
            <Card>
              <CardHeader><CardTitle className="text-sm">{t("subject.details")}</CardTitle></CardHeader>
              <CardContent className="space-y-2 text-sm">
                <div className="flex justify-between"><span className="text-muted-foreground">{t("subject.username")}</span><span className="font-mono">{s.name}</span></div>
                <div className="flex justify-between"><span className="text-muted-foreground">{t("subject.status")}</span><span>{s.enabled ? t("subject.enabled") : t("subject.disabled")} {s.frozen ? "• Frozen" : ""} {s.expired_at ? "• Expired" : ""}</span></div>
                <div className="flex justify-between"><span className="text-muted-foreground">{t("subject.created")}</span><span className="font-mono text-xs">{formatTimestamp(s.created_at)}</span></div>
                <div className="flex justify-between"><span className="text-muted-foreground">{t("subject.owner")}</span><span>{s.owner_admin_id ? `Admin#${s.owner_admin_id}` : "Platform"}</span></div>
                <div className="flex justify-between"><span className="text-muted-foreground">{t("subject.service")}</span><span>{s.primary_service_id ? `#${s.primary_service_id}` : "—"} {s.service_ids?.length ? `(${s.service_ids.length} total)` : ""}</span></div>
                <div className="flex justify-between"><span className="text-muted-foreground">{t("subject.node")}</span><span>{s.node_ids?.length ? `${s.node_ids.length} nodes` : "—"}</span></div>
                <div className="flex justify-between"><span className="text-muted-foreground">{t("subject.ipLimit")}</span><span>{s.max_ips ?? "∞"}</span></div>
                <div className="flex justify-between"><span className="text-muted-foreground">{t("subject.deviceLimit")}</span><span>{s.max_devices ?? "∞"}</span></div>
                {s.note && <div><div className="text-muted-foreground text-xs">{t("subject.note")}</div><div className="mt-1 p-2 bg-muted rounded text-sm">{s.note}</div></div>}
              </CardContent>
            </Card>
            <Card>
              <CardHeader><CardTitle className="text-sm">{t("subject.quickActions")}</CardTitle></CardHeader>
              <CardContent className="space-y-3">
                <div className="flex gap-2">
                  <Input type="number" value={addTrafficGB} onChange={(e) => setAddTrafficGB(Number(e.target.value))} className="w-24" />
                  <Button size="sm" onClick={() => addTrafficMut.mutate(addTrafficGB)}>{t("subject.addTraffic")}</Button>
                  <Button size="sm" variant="outline" onClick={() => addTrafficMut.mutate(-addTrafficGB)}>{t("subject.removeTraffic")}</Button>
                </div>
                <div className="flex gap-2">
                  <Input type="number" value={addDays} onChange={(e) => setAddDays(Number(e.target.value))} className="w-24" />
                  <Button size="sm" onClick={() => addDaysMut.mutate(addDays)}>{t("subject.addDays")}</Button>
                  <Button size="sm" variant="outline" onClick={() => addDaysMut.mutate(-addDays)}>{t("subject.removeDays")}</Button>
                </div>
                <div className="grid grid-cols-2 gap-2 pt-2">
                  <Button size="sm" variant="outline" onClick={() => { api.get<{ url: string }>(`/api/v1/subjects/${s.id}/subscription`).then((d) => { navigator.clipboard.writeText(d.url); setCopied("sub"); setTimeout(() => setCopied(null), 1500); }); }}>{copied === "sub" ? t("subject.copied") : t("subject.copySub")}</Button>
                  <Button size="sm" variant="outline" onClick={() => subscription.data && window.open(subscription.data.qr_url, "_blank")}>{t("subject.showQR")}</Button>
                </div>
              </CardContent>
            </Card>
          </div>
          <Card>
            <CardHeader><CardTitle className="text-sm">{t("subject.devices")}</CardTitle></CardHeader>
            <CardContent>
              {devices.data?.devices.length === 0 ? <div className="text-sm text-muted-foreground">{t("subject.noDevices")}</div> : (
                <table className="w-full text-sm">
                  <thead><tr className="border-b text-xs text-muted-foreground"><th className="text-left py-1">{t("device.fingerprint")}</th><th className="text-left">{t("device.lastSeen")}</th><th className="text-left">{t("device.lastIP")}</th></tr></thead>
                  <tbody>{devices.data?.devices.map((d) => (<tr key={d.id} className="border-b"><td className="py-1 font-mono text-xs">{d.fingerprint}</td><td className="font-mono text-xs">{formatTimestamp(d.last_seen_at)}</td><td className="font-mono text-xs">{d.last_ip}</td></tr>))}</tbody>
                </table>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="credentials" className="space-y-4 mt-4">
          <Card>
            <CardHeader><CardTitle className="text-sm">{t("subject.credentials")}</CardTitle></CardHeader>
            <CardContent className="space-y-3">
              <div className="flex flex-wrap gap-2">
                <Button size="sm" variant="outline" onClick={() => revealCred("uuid")}>{t("subject.revealUUID")}</Button>
                <Button size="sm" variant="outline" onClick={() => revealCred("password")}>{t("subject.revealPassword")}</Button>
                <Button size="sm" variant="outline" onClick={() => rotateCred.mutate("uuid")}>{t("subject.rotateUUID")}</Button>
                <Button size="sm" variant="outline" onClick={() => rotateCred.mutate("password")}>{t("subject.rotatePassword")}</Button>
              </div>
              {showCred && (
                <div className="rounded bg-muted p-3 space-y-2">
                  <div className="text-xs text-muted-foreground">{showCred}</div>
                  <div className="font-mono text-sm break-all select-all">{credValue}</div>
                  <Button size="sm" variant="outline" onClick={() => copyText(credValue, showCred)}>{copied === showCred ? t("common.copied") : t("common.copy")}</Button>
                </div>
              )}
              <div className="text-xs text-muted-foreground">Credentials are sealed with AES-256-GCM. Rotation bumps node revisions and disconnects existing sessions.</div>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="subscription" className="space-y-4 mt-4">
          <Card>
            <CardHeader><CardTitle className="text-sm">{t("subject.subscription")}</CardTitle></CardHeader>
            <CardContent>
              {subscription.data?.url ? (
                <div className="space-y-4">
                  <div className="flex flex-col md:flex-row gap-4">
                    <div className="flex-1 space-y-3">
                      <div className="p-3 bg-muted rounded font-mono text-xs break-all select-all">{subscription.data.url}</div>
                      <div className="flex flex-wrap gap-2">
                        <Button size="sm" onClick={() => copyText(subscription.data!.url, "main")}>{copied === "main" ? t("common.copied") : t("common.copy")}</Button>
                        <Button size="sm" variant="outline" onClick={() => copyText(subscription.data!.clash_url, "clash")}>{copied === "clash" ? t("common.copied") : t("subject.copyClash")}</Button>
                        <Button size="sm" variant="outline" onClick={() => copyText(subscription.data!.singbox_url, "singbox")}>{copied === "singbox" ? t("common.copied") : t("subject.copySingbox")}</Button>
                        <Button size="sm" variant="outline" onClick={() => copyText(subscription.data!.v2ray_url ?? subscription.data!.url, "v2ray")}>{copied === "v2ray" ? t("common.copied") : t("subject.copyV2Ray")}</Button>
                        <a href={subscription.data.url} target="_blank" rel="noopener noreferrer"><Button size="sm" variant="default">{t("subject.openSubPage")}</Button></a>
                      </div>
                    </div>
                    <div className="flex flex-col items-center gap-2">
                      <img src={subscription.data.qr_url} alt="QR" width={180} height={180} className="rounded border bg-white p-2" />
                      <div className="text-xs text-muted-foreground">{t("subject.qrCode")}</div>
                    </div>
                  </div>
                </div>
              ) : <div>{t("loading")}</div>}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="traffic" className="space-y-4 mt-4">
          <div className="grid md:grid-cols-3 gap-3">
            <Card><CardContent className="p-3"><div className="text-xs text-muted-foreground">{t("subject.totalUpload")}</div><div className="font-mono font-bold">{traffic.data ? formatTraffic((traffic.data as any).total_uplink ?? traffic.data.total) : "—"}</div></CardContent></Card>
            <Card><CardContent className="p-3"><div className="text-xs text-muted-foreground">{t("subject.totalDownload")}</div><div className="font-mono font-bold">{traffic.data ? formatTraffic((traffic.data as any).total_downlink ?? traffic.data.total) : "—"}</div></CardContent></Card>
            <Card><CardContent className="p-3"><div className="text-xs text-muted-foreground">{t("subject.totalUsage")}</div><div className="font-mono font-bold">{traffic.data ? formatTraffic(traffic.data.total) : "—"}</div></CardContent></Card>
          </div>
          <Card>
            <CardHeader><CardTitle className="text-sm">{t("subject.usageChart")} (30d)</CardTitle></CardHeader>
            <CardContent>
              {!traffic.data || traffic.data.daily.length === 0 ? <div className="text-sm text-muted-foreground">{t("subject.noTraffic")}</div> : (
                <div className="space-y-1">
                  {traffic.data.daily.slice(0, 15).map((d: any, i: number) => {
                    const max = Math.max(...traffic.data!.daily.map((x: any) => x.total), 1);
                    const pct = Math.round((d.total / max) * 100);
                    return (
                      <div key={i} className="flex items-center gap-2 text-xs">
                        <span className="w-24 font-mono">{formatTimestamp(d.day)}</span>
                        <div className="flex-1 h-2 bg-muted rounded overflow-hidden"><div className="h-full bg-primary" style={{ width: `${pct}%` }} /></div>
                        <span className="w-20 font-mono text-right">{formatTraffic(d.total)}</span>
                      </div>
                    );
                  })}
                </div>
              )}
            </CardContent>
          </Card>
          <Card>
            <CardHeader><CardTitle className="text-sm">{t("subject.nodeBreakdown")}</CardTitle></CardHeader>
            <CardContent>
              {!traffic.data || !traffic.data.node_breakdown?.length ? <div className="text-sm text-muted-foreground">{t("subject.noTraffic")}</div> : (
                <table className="w-full text-sm"><thead><tr className="border-b text-xs text-muted-foreground"><th className="text-left py-1">{t("subject.node")}</th><th className="text-right">{t("connections.total")}</th></tr></thead><tbody>{traffic.data.node_breakdown.map((n: any, i: number) => (<tr key={i} className="border-b"><td className="py-1">{n.node_name || `Node#${n.node_id}`}</td><td className="text-right font-mono">{formatTraffic(n.total)}</td></tr>))}</tbody></table>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="connections" className="space-y-4 mt-4">
          <Card>
            <CardHeader><CardTitle className="text-sm">{t("subject.activeConnections")}</CardTitle></CardHeader>
            <CardContent>
              {!ips.data || ips.data.connections.length === 0 ? <div className="text-sm text-muted-foreground">{t("subject.noConnections")}</div> : (
                <table className="w-full text-sm">
                  <thead><tr className="border-b text-xs text-muted-foreground"><th className="text-left py-1">{t("subject.ipAddress")}</th><th className="text-left">{t("subject.node")}</th><th className="text-left">{t("connections.protocol")}</th><th className="text-left">{t("subject.connectionTime")}</th><th className="text-left">{t("subject.lastSeen")}</th></tr></thead>
                  <tbody>{ips.data.connections.map((c: any, i: number) => (<tr key={i} className="border-b"><td className="py-1 font-mono text-xs">{c.source_ip}</td><td className="text-xs">{c.node_name || c.node_id}</td><td className="text-xs">{c.protocol_info || "—"}</td><td className="font-mono text-xs">{formatTimestamp(c.connected_at)}</td><td className="font-mono text-xs">{formatTimestamp(c.last_seen_at)}</td></tr>))}</tbody>
                </table>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="activity" className="space-y-4 mt-4">
          <Card>
            <CardHeader><CardTitle className="text-sm">{t("subject.activity")}</CardTitle></CardHeader>
            <CardContent>
              {!activity.data || (activity.data.events.length === 0 && activity.data.devices.length === 0) ? <div className="text-sm text-muted-foreground">{t("subject.noActivity")}</div> : (
                <div className="space-y-4">
                  {activity.data.events.length > 0 && (
                    <div>
                      <h4 className="text-xs font-semibold mb-2">{t("connections.history")}</h4>
                      <div className="space-y-1">
                        {activity.data.events.slice(0, 20).map((e: any, i: number) => (
                          <div key={i} className="flex gap-2 text-xs border-b py-1">
                            <Badge variant="outline" className="text-xs">{e.event_type}</Badge>
                            <span className="font-mono">{e.source_ip}</span>
                            <span className="text-muted-foreground">{formatTimestamp(e.timestamp)}</span>
                            {e.rejection_reason && <span className="text-destructive">{e.rejection_reason}</span>}
                          </div>
                        ))}
                      </div>
                    </div>
                  )}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="audit" className="space-y-4 mt-4">
          <Card>
            <CardHeader><CardTitle className="text-sm">{t("subject.audit")}</CardTitle></CardHeader>
            <CardContent>
              {!audit.data || audit.data.audit.length === 0 ? <div className="text-sm text-muted-foreground">{t("subject.noAudit")}</div> : (
                <div className="space-y-2">
                  {audit.data.audit.map((a: any, i: number) => (
                    <div key={i} className="border-b py-2 text-xs">
                      <div className="flex gap-2"><Badge variant="outline">{a.action}</Badge><span>{a.actor_type}#{a.actor_id}</span><span className="text-muted-foreground">{formatTimestamp(a.created_at)}</span><span className={a.result === "ok" ? "text-green-600" : "text-destructive"}>{a.result}</span></div>
                      {a.after && <div className="mt-1 font-mono text-xs bg-muted p-1 rounded break-all">{a.after}</div>}
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>

      <ConfirmDialog open={showDelete} onOpenChange={setShowDelete} title={t("subject.confirmDelete")} description={s.name} confirmLabel={t("delete")} pending={deleteMut.isPending} onConfirm={() => deleteMut.mutate()} />
      <ConfirmDialog open={showResetTraffic} onOpenChange={setShowResetTraffic} title={t("subject.confirmResetTraffic")} description={`${s.name}: ${formatTraffic(s.quota_used_bytes)} → 0`} confirmLabel={t("subject.resetTraffic")} pending={resetTrafficMut.isPending} onConfirm={() => resetTrafficMut.mutate()} />
      <ConfirmDialog open={showRevokeSub} onOpenChange={setShowRevokeSub} title={t("subject.confirmRevokeSub")} description={s.name} confirmLabel={t("subject.regenSub")} pending={revokeSub.isPending} onConfirm={() => revokeSub.mutate()} />
    </div>
  );
}
