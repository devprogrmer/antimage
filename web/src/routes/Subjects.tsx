import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useState, useMemo } from "react";
import { api } from "../lib/api";
import { formatNumber, formatTimestamp, t } from "../i18n";
import { Link } from "react-router-dom";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { QuotaBar, formatTraffic, daysLeft } from "../components/QuotaBar";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "../components/ui/sheet";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  DropdownMenuSeparator,
} from "../components/ui/dropdown-menu";
import { Badge } from "../components/ui/badge";
import { Card, CardContent } from "../components/ui/card";

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
  last_online_at: number | null;
  is_online: boolean;
  owner_admin_id: number | null;
  primary_service_id: number | null;
  remaining_bytes: number | null;
  remaining_days: number | null;
  service_ids?: number[];
  node_ids?: number[];
}

interface ServiceInfo {
  id: number;
  node_id: number;
  node_name: string;
  adapter_kind: string;
  params: { protocol?: string; port?: number; network?: string };
  enabled: boolean;
}

interface NodeInfo {
  id: number;
  name: string;
  status: string;
}

interface AdminInfo {
  id: number;
  username: string;
}

type StatusFilter = "" | "active" | "expired" | "disabled" | "limited" | "online" | "offline" | "expiring_soon" | "on_hold" | "frozen";
type SortField = "name" | "created" | "expires" | "traffic" | "quota" | "last_online" | "lifetime" | "id";
type SortOrder = "asc" | "desc";

export function Subjects({ onSelect }: { onSelect: (id: number) => void }) {
  const queryClient = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  const [pendingDelete, setPendingDelete] = useState<Subject | null>(null);
  const [selected, setSelected] = useState<number[]>([]);
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState<StatusFilter>("");
  const [serviceFilter, setServiceFilter] = useState<string>("");
  const [nodeFilter, setNodeFilter] = useState<string>("");
  const [protocolFilter, setProtocolFilter] = useState<string>("");
  const [ownerFilter, setOwnerFilter] = useState<string>("");
  const [sortField, setSortField] = useState<SortField>("created");
  const [sortOrder, setSortOrder] = useState<SortOrder>("desc");
  const [page, setPage] = useState(1);
  const pageSize = 25;
  const [showBulkTraffic, setShowBulkTraffic] = useState(false);
  const [showBulkDays, setShowBulkDays] = useState(false);
  const [bulkTrafficGB, setBulkTrafficGB] = useState(5);
  const [bulkDays, setBulkDays] = useState(30);
  const [copiedId, setCopiedId] = useState<number | null>(null);

  const queryKey = useMemo(() => ["subjects", { search, status, serviceFilter, nodeFilter, protocolFilter, ownerFilter, sortField, sortOrder, page }], [search, status, serviceFilter, nodeFilter, protocolFilter, ownerFilter, sortField, sortOrder, page]);

  const subjectsQuery = useQuery({
    queryKey,
    queryFn: () => {
      const params = new URLSearchParams();
      params.set("page", String(page));
      params.set("page_size", String(pageSize));
      if (search) params.set("search", search);
      if (status) params.set("status", status);
      if (serviceFilter) params.set("service_id", serviceFilter);
      if (nodeFilter) params.set("node_id", nodeFilter);
      if (protocolFilter) params.set("protocol", protocolFilter);
      if (ownerFilter) params.set("owner_id", ownerFilter);
      if (sortField) params.set("sort", sortField);
      if (sortOrder) params.set("order", sortOrder);
      return api.get<{ subjects: Subject[]; total: number; page: number; page_size: number }>(
        "/api/v2/subjects?" + params.toString()
      );
    },
  });

  const servicesQuery = useQuery({
    queryKey: ["services-list"],
    queryFn: () => api.get<{ services: ServiceInfo[] }>("/api/v1/services"),
  });

  const nodesQuery = useQuery({
    queryKey: ["nodes-list"],
    queryFn: () => api.get<{ nodes: NodeInfo[] }>("/api/v1/nodes"),
  });

  const adminsQuery = useQuery({
    queryKey: ["admins-list"],
    queryFn: () => api.get<{ admins: AdminInfo[] }>("/api/v1/admins"),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: number) => api.del(`/api/v1/subjects/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["subjects"] });
      setPendingDelete(null);
      setSelected((s) => s.filter((x) => x !== pendingDelete?.id));
    },
  });

  const bulkAction = async (action: string, extra?: any) => {
    const body = { subject_ids: selected, ...extra };
    if (action === "enable") await api.post("/api/v1/subjects/bulk/enable", body);
    else if (action === "disable") await api.post("/api/v1/subjects/bulk/disable", body);
    else if (action === "delete") await api.post("/api/v1/subjects/bulk/delete", body);
    else if (action === "extend") await api.post("/api/v1/subjects/bulk/extend", { subject_ids: selected, days: bulkDays });
    else if (action === "reset-traffic") await api.post("/api/v1/subjects/bulk/reset-traffic", body);
    else if (action === "set-quota") await api.post("/api/v1/subjects/bulk/set-quota", body);
    else if (action === "add-traffic") await api.post("/api/v1/subjects/bulk/add-traffic", { subject_ids: selected, gb: bulkTrafficGB });
    else if (action === "add-days") await api.post("/api/v1/subjects/bulk/add-days", { subject_ids: selected, days: bulkDays });
    await queryClient.invalidateQueries({ queryKey: ["subjects"] });
    setSelected([]);
  };

  const copySubscription = async (subject: Subject) => {
    try {
      const data = await api.get<{ url: string }>(`/api/v1/subjects/${subject.id}/subscription`);
      await navigator.clipboard.writeText(data.url);
      setCopiedId(subject.id);
      setTimeout(() => setCopiedId(null), 1500);
    } catch {}
  };

  const toggleSelect = (id: number) => {
    setSelected((cur) => cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]);
  };

  const toggleSelectAll = () => {
    const allIds = (subjectsQuery.data?.subjects ?? []).map((s) => s.id);
    if (selected.length === allIds.length) setSelected([]);
    else setSelected(allIds);
  };

  const totalPages = Math.ceil((subjectsQuery.data?.total ?? 0) / pageSize);
  const subjects = subjectsQuery.data?.subjects ?? [];

  const getServiceInfo = (id: number | null | undefined) => {
    if (!id) return null;
    return (servicesQuery.data?.services ?? []).find((s) => s.id === id) ?? null;
  };

  const getNodeInfo = (id: number | null | undefined) => {
    if (!id) return null;
    return (nodesQuery.data?.nodes ?? []).find((n) => n.id === id) ?? null;
  };

  return (
    <div className="space-y-4">
      {/* Header */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-2xl font-bold">{t("subjects.title")}</h2>
          <p className="text-sm text-muted-foreground">
            {subjectsQuery.data?.total ?? 0} {t("subjects.total")} • {t("subjects.active")}: {subjects.filter((s) => s.enabled && !s.expired_at && !s.frozen).length} • {t("subjects.online")}: {subjects.filter((s) => s.is_online).length}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => {
            const params = new URLSearchParams();
            if (search) params.set("search", search);
            window.open("/api/v1/subjects/export?" + params.toString(), "_blank");
          }}>
            {t("subjects.export")}
          </Button>
          <Button size="sm" onClick={() => setShowCreate(true)}>
            {t("subjects.create")}
          </Button>
        </div>
      </div>

      {/* Create Sheet */}
      <Sheet open={showCreate} onOpenChange={setShowCreate}>
        <SheetContent aria-describedby={undefined} className="w-full sm:max-w-xl overflow-y-auto">
          <SheetHeader>
            <SheetTitle>{t("subjects.create")}</SheetTitle>
          </SheetHeader>
          <CreateUserWorkflow onClose={() => { setShowCreate(false); queryClient.invalidateQueries({ queryKey: ["subjects"] }); }} />
        </SheetContent>
      </Sheet>

      {/* Filters */}
      <Card>
        <CardContent className="p-4 space-y-3">
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 xl:grid-cols-6 gap-3">
            <div className="lg:col-span-2">
              <Input
                placeholder={t("subjects.searchPlaceholder")}
                value={search}
                onChange={(e) => { setSearch(e.target.value); setPage(1); }}
              />
            </div>
            <select value={status} onChange={(e) => { setStatus(e.target.value as StatusFilter); setPage(1); }} className="h-9 rounded-md border border-input bg-background px-3 text-sm">
              <option value="">{t("filters.all")} - {t("filters.status")}</option>
              <option value="active">{t("filters.active")}</option>
              <option value="expired">{t("filters.expired")}</option>
              <option value="disabled">{t("filters.disabled")}</option>
              <option value="limited">{t("filters.limited")}</option>
              <option value="online">{t("filters.online")}</option>
              <option value="offline">{t("filters.offline")}</option>
              <option value="expiring_soon">{t("filters.expiringSoon")}</option>
              <option value="on_hold">{t("filters.onHold")}</option>
              <option value="frozen">{t("filters.frozen")}</option>
            </select>
            <select value={serviceFilter} onChange={(e) => { setServiceFilter(e.target.value); setPage(1); }} className="h-9 rounded-md border border-input bg-background px-3 text-sm">
              <option value="">{t("filters.allServices")}</option>
              {(servicesQuery.data?.services ?? []).map((svc) => (
                <option key={svc.id} value={String(svc.id)}>{svc.node_name} · {svc.adapter_kind} {svc.params?.protocol ? `/${svc.params.protocol}` : ""}:{svc.params?.port ?? ""}</option>
              ))}
            </select>
            <select value={nodeFilter} onChange={(e) => { setNodeFilter(e.target.value); setPage(1); }} className="h-9 rounded-md border border-input bg-background px-3 text-sm">
              <option value="">{t("filters.allNodes")}</option>
              {(nodesQuery.data?.nodes ?? []).map((node) => (
                <option key={node.id} value={String(node.id)}>{node.name} ({node.status})</option>
              ))}
            </select>
            <select value={protocolFilter} onChange={(e) => { setProtocolFilter(e.target.value); setPage(1); }} className="h-9 rounded-md border border-input bg-background px-3 text-sm">
              <option value="">{t("filters.allProtocols")}</option>
              <option value="vless">VLESS</option>
              <option value="vmess">VMess</option>
              <option value="trojan">Trojan</option>
              <option value="shadowsocks">Shadowsocks</option>
              <option value="wireguard">WireGuard</option>
              <option value="hysteria2">Hysteria2</option>
            </select>
            <select value={ownerFilter} onChange={(e) => { setOwnerFilter(e.target.value); setPage(1); }} className="h-9 rounded-md border border-input bg-background px-3 text-sm">
              <option value="">{t("filters.allOwners")}</option>
              {(adminsQuery.data?.admins ?? []).map((admin) => (
                <option key={admin.id} value={String(admin.id)}>{admin.username}</option>
              ))}
            </select>
            <select value={`${sortField}:${sortOrder}`} onChange={(e) => { const [f, o] = e.target.value.split(":"); setSortField(f as SortField); setSortOrder(o as SortOrder); }} className="h-9 rounded-md border border-input bg-background px-3 text-sm">
              <option value="created:desc">{t("filters.sort_created")} ↓</option>
              <option value="created:asc">{t("filters.sort_created")} ↑</option>
              <option value="name:asc">{t("filters.sort_name")} ↑</option>
              <option value="name:desc">{t("filters.sort_name")} ↓</option>
              <option value="expires:asc">{t("filters.sort_expiry")} ↑</option>
              <option value="expires:desc">{t("filters.sort_expiry")} ↓</option>
              <option value="traffic:desc">{t("filters.sort_traffic")} ↓</option>
              <option value="quota:desc">{t("filters.sort_quota")} ↓</option>
              <option value="last_online:desc">{t("filters.sort_lastOnline")} ↓</option>
            </select>
            <Button variant="outline" size="sm" onClick={() => { setSearch(""); setStatus(""); setServiceFilter(""); setNodeFilter(""); setProtocolFilter(""); setOwnerFilter(""); setSortField("created"); setSortOrder("desc"); setPage(1); }}>
              {t("filters.clear_filters")}
            </Button>
          </div>
        </CardContent>
      </Card>

      {/* Bulk Actions */}
      {selected.length > 0 && (
        <Card className="border-primary/50 bg-primary/5">
          <CardContent className="p-3 flex flex-wrap items-center gap-2">
            <span className="text-sm font-medium">{selected.length} {t("common.selected")}</span>
            <div className="flex flex-wrap gap-2">
              <Button size="sm" variant="outline" onClick={() => bulkAction("enable")}>{t("subjects.bulkEnable")}</Button>
              <Button size="sm" variant="outline" onClick={() => bulkAction("disable")}>{t("subjects.bulkDisable")}</Button>
              <Button size="sm" variant="outline" onClick={() => setShowBulkTraffic(true)}>{t("subjects.bulkAddTraffic")}</Button>
              <Button size="sm" variant="outline" onClick={() => setShowBulkDays(true)}>{t("subjects.bulkAddDays")}</Button>
              <Button size="sm" variant="outline" onClick={() => bulkAction("reset-traffic")}>{t("subject.resetTraffic")}</Button>
              <Button size="sm" variant="destructive" onClick={() => { if (confirm(`Delete ${selected.length} users?`)) bulkAction("delete"); }}>{t("subjects.bulkDelete")}</Button>
              <Button size="sm" variant="ghost" onClick={() => setSelected([])}>{t("subject.clearSelection")}</Button>
            </div>
          </CardContent>
        </Card>
      )}

      {/* Table */}
      <Card>
        <CardContent className="p-0 overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b bg-muted/50 text-xs">
                <th className="p-2 text-left"><input type="checkbox" checked={subjects.length > 0 && selected.length === subjects.length} onChange={toggleSelectAll} /></th>
                <th className="p-2 text-left">{t("subject.username")}</th>
                <th className="p-2 text-left">{t("subject.status")}</th>
                <th className="p-2 text-left">{t("subject.owner")}</th>
                <th className="p-2 text-left">{t("subject.service")}</th>
                <th className="p-2 text-left">{t("subject.protocol")}</th>
                <th className="p-2 text-left">{t("subject.node")}</th>
                <th className="p-2 text-left">{t("subject.trafficUsed")}</th>
                <th className="p-2 text-left">{t("subject.trafficLimit")}</th>
                <th className="p-2 text-left">{t("subject.remaining")}</th>
                <th className="p-2 text-left">{t("subject.expiry")}</th>
                <th className="p-2 text-left">{t("subject.remainingTime")}</th>
                <th className="p-2 text-left">{t("subject.ipLimit")}</th>
                <th className="p-2 text-left">{t("subject.currentConnections")}</th>
                <th className="p-2 text-left">{t("subject.lastOnline")}</th>
                <th className="p-2 text-left">{t("subject.created")}</th>
                <th className="p-2 text-left">{t("actions")}</th>
              </tr>
            </thead>
            <tbody>
              {subjectsQuery.isLoading ? (
                Array.from({ length: 5 }).map((_, i) => (
                  <tr key={i} className="border-b animate-pulse">
                    <td colSpan={17} className="p-4"><div className="h-4 bg-muted rounded w-full"></div></td>
                  </tr>
                ))
              ) : subjects.length === 0 ? (
                <tr><td colSpan={17} className="p-8 text-center text-muted-foreground">{t("subjects.noResults")}</td></tr>
              ) : subjects.map((subj) => {
                const svc = getServiceInfo(subj.primary_service_id);
                const node = svc ? getNodeInfo(svc.node_id) : (subj.node_ids && subj.node_ids[0] ? getNodeInfo(subj.node_ids[0]) : null);
                const protocol = svc?.params?.protocol ?? svc?.adapter_kind ?? "—";
                return (
                  <tr key={subj.id} className="border-b hover:bg-muted/30">
                    <td className="p-2"><input type="checkbox" checked={selected.includes(subj.id)} onChange={() => toggleSelect(subj.id)} onClick={(e) => e.stopPropagation()} /></td>
                    <td className="p-2"><Link to={`/subjects/${subj.id}`} className="font-mono font-medium hover:underline text-primary">{subj.name}</Link><div className="text-xs text-muted-foreground">ID:{subj.id}</div></td>
                    <td className="p-2"><StatusBadge subject={subj} /></td>
                    <td className="p-2 text-xs">{subj.owner_admin_id ? `Admin#${subj.owner_admin_id}` : "Platform"}</td>
                    <td className="p-2 text-xs">{svc ? `${svc.node_name} #${svc.id}` : (subj.service_ids?.length ? `${subj.service_ids.length} services` : "—")}</td>
                    <td className="p-2"><Badge variant="outline" className="text-xs">{protocol}</Badge></td>
                    <td className="p-2 text-xs">{node ? node.name : (subj.node_ids?.length ? `${subj.node_ids.length} nodes` : "—")}</td>
                    <td className="p-2 font-mono text-xs">{formatTraffic(subj.quota_used_bytes)}</td>
                    <td className="p-2 font-mono text-xs">{subj.quota_bytes ? formatTraffic(subj.quota_bytes) : t("filters.unlimited")}</td>
                    <td className="p-2">
                      <div className="flex flex-col gap-1 min-w-[100px]">
                        <QuotaBar used={subj.quota_used_bytes} total={subj.quota_bytes} />
                        {subj.remaining_bytes !== null && subj.quota_bytes && (
                          <span className="font-mono text-xs text-muted-foreground">{formatTraffic(subj.remaining_bytes)} left</span>
                        )}
                      </div>
                    </td>
                    <td className="p-2 font-mono text-xs">{formatTimestamp(subj.expires_at)}</td>
                    <td className="p-2 text-xs">
                      {(() => {
                        const left = daysLeft(subj.expires_at);
                        if (left === null) return <span className="text-muted-foreground">{t("subject.never")}</span>;
                        return <span className={left <= 3 ? "text-orange-500 font-medium" : left <= 7 ? "text-yellow-600" : ""}>{formatNumber(left)} {t("subject.daysLeft")}</span>;
                      })()}
                    </td>
                    <td className="p-2 text-xs">{subj.max_ips ?? "∞"}</td>
                    <td className="p-2 text-xs">{subj.is_online ? <Badge className="bg-green-500">● {t("subject.online")}</Badge> : <span className="text-muted-foreground">{t("subject.offline")}</span>}</td>
                    <td className="p-2 font-mono text-xs">{formatTimestamp(subj.last_online_at)}</td>
                    <td className="p-2 font-mono text-xs">{formatTimestamp(subj.created_at)}</td>
                    <td className="p-2">
                      <div className="flex items-center gap-1">
                        <Button variant="ghost" size="sm" className="h-7 px-2 text-xs" onClick={(e) => { e.stopPropagation(); copySubscription(subj); }}>
                          {copiedId === subj.id ? t("subject.copied") : t("subject.copySub")}
                        </Button>
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <Button variant="ghost" size="sm" className="h-7 w-7 p-0" onClick={(e) => e.stopPropagation()}>⋯</Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent onClick={(e) => e.stopPropagation()}>
                            <DropdownMenuItem onClick={() => onSelect(subj.id)}>{t("subject.details")}</DropdownMenuItem>
                            <DropdownMenuItem onClick={() => copySubscription(subj)}>{t("subject.copySub")}</DropdownMenuItem>
                            <DropdownMenuItem onClick={() => { api.post(`/api/v1/subjects/${subj.id}/enable`, {}).then(() => queryClient.invalidateQueries({ queryKey: ["subjects"] })); }}>{t("subject.enable")}</DropdownMenuItem>
                            <DropdownMenuItem onClick={() => { api.post(`/api/v1/subjects/${subj.id}/disable`, {}).then(() => queryClient.invalidateQueries({ queryKey: ["subjects"] })); }}>{t("subject.disable")}</DropdownMenuItem>
                            <DropdownMenuItem onClick={() => { api.post(`/api/v1/subjects/${subj.id}/reset-traffic`, {}).then(() => queryClient.invalidateQueries({ queryKey: ["subjects"] })); }}>{t("subject.resetTraffic")}</DropdownMenuItem>
                            <DropdownMenuSeparator />
                            <DropdownMenuItem className="text-destructive" onClick={() => setPendingDelete(subj)}>{t("delete")}</DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </CardContent>
      </Card>

      {/* Pagination */}
      <div className="flex items-center justify-between">
        <span className="text-sm text-muted-foreground">Page {page} of {totalPages || 1} • {subjectsQuery.data?.total ?? 0} users</span>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => setPage((p) => Math.max(1, p - 1))}>Prev</Button>
          <Button variant="outline" size="sm" disabled={page >= totalPages} onClick={() => setPage((p) => p + 1)}>Next</Button>
        </div>
      </div>

      {/* Delete Confirm */}
      <ConfirmDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => !open && setPendingDelete(null)}
        title={t("subject.confirmDelete")}
        description={pendingDelete?.name}
        confirmLabel={t("delete")}
        pending={deleteMutation.isPending}
        onConfirm={() => pendingDelete && deleteMutation.mutate(pendingDelete.id)}
      />

      {/* Bulk Traffic Dialog */}
      {showBulkTraffic && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4">
          <Card className="w-full max-w-md">
            <CardContent className="p-6 space-y-4">
              <h3 className="font-semibold">{t("subject.addTrafficTitle")}</h3>
              <p className="text-sm text-muted-foreground">{selected.length} users</p>
              <Input type="number" value={bulkTrafficGB} onChange={(e) => setBulkTrafficGB(Number(e.target.value))} placeholder="GB" />
              <div className="flex gap-2 justify-end">
                <Button variant="outline" size="sm" onClick={() => setShowBulkTraffic(false)}>{t("cancel")}</Button>
                <Button size="sm" onClick={() => { setShowBulkTraffic(false); bulkAction("add-traffic"); }}>{t("confirm")}</Button>
              </div>
            </CardContent>
          </Card>
        </div>
      )}

      {showBulkDays && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4">
          <Card className="w-full max-w-md">
            <CardContent className="p-6 space-y-4">
              <h3 className="font-semibold">{t("subject.addDaysTitle")}</h3>
              <p className="text-sm text-muted-foreground">{selected.length} users</p>
              <Input type="number" value={bulkDays} onChange={(e) => setBulkDays(Number(e.target.value))} placeholder="Days" />
              <div className="flex gap-2 justify-end">
                <Button variant="outline" size="sm" onClick={() => setShowBulkDays(false)}>{t("cancel")}</Button>
                <Button size="sm" onClick={() => { setShowBulkDays(false); bulkAction("add-days"); }}>{t("confirm")}</Button>
              </div>
            </CardContent>
          </Card>
        </div>
      )}
    </div>
  );
}

function StatusBadge({ subject }: { subject: Subject }) {
  if (!subject.enabled) {
    return <Badge variant="secondary">{t("subject.disabled")}</Badge>;
  }
  if (subject.expired_at || (subject.expires_at && subject.expires_at * 1000 < Date.now())) {
    return <Badge variant="destructive">{t("subject.expired")}</Badge>;
  }
  if (subject.frozen) {
    return <Badge variant="outline" className="border-orange-500 text-orange-500">{t("filters.frozen")}</Badge>;
  }
  if (subject.quota_bytes && subject.quota_used_bytes >= subject.quota_bytes) {
    return <Badge variant="outline" className="border-red-500 text-red-500">{t("subject.limited")}</Badge>;
  }
  if (subject.expires_at) {
    const left = daysLeft(subject.expires_at);
    if (left !== null && left <= 7) {
      return <Badge variant="outline" className="border-yellow-500 text-yellow-600">{t("subject.expiringSoon")}</Badge>;
    }
  }
  if (subject.status === "on_hold") {
    return <Badge variant="outline">{t("subject.onHold")}</Badge>;
  }
  if (subject.is_online) {
    return <Badge className="bg-green-500 hover:bg-green-600"><span className="mr-1">●</span>{t("subject.active")}</Badge>;
  }
  return <Badge className="bg-green-600">{t("subject.active")}</Badge>;
}

function CreateUserWorkflow({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [step, setStep] = useState(1);
  const [name, setName] = useState("");
  const [note, setNote] = useState("");
  const [expireDays, setExpireDays] = useState("30");
  const [quotaGB, setQuotaGB] = useState("50");
  const [ipLimit, setIpLimit] = useState("");
  const [deviceLimit, setDeviceLimit] = useState("");
  const [resetStrategy, setResetStrategy] = useState("no_reset");
  const [serviceIDs, setServiceIDs] = useState<number[]>([]);
  const [telegramId, setTelegramId] = useState("");
  const [autoDelete, setAutoDelete] = useState("");

  const services = useQuery({
    queryKey: ["services-catalog"],
    queryFn: () =>
      api.get<{
        services: Array<{
          id: number;
          node_name: string;
          adapter_kind: string;
          params: { protocol?: string; port?: number };
        }>;
      }>("/api/v1/services"),
  });

  const create = useMutation({
    mutationFn: () =>
      api.post("/api/v1/subjects", {
        name: name || `user_${Date.now()}`,
        note,
        service_ids: serviceIDs,
        expire_days: expireDays ? Number(expireDays) : undefined,
        quota_bytes: quotaGB ? Number(quotaGB) * 1024 * 1024 * 1024 : undefined,
        max_ips: ipLimit ? Number(ipLimit) : undefined,
        max_devices: deviceLimit ? Number(deviceLimit) : undefined,
        data_limit_reset_strategy: resetStrategy,
        telegram_id: telegramId || undefined,
        auto_delete_in_days: autoDelete ? Number(autoDelete) : undefined,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["subjects"] });
      onClose();
    },
  });

  return (
    <div className="space-y-6 mt-4">
      <div className="flex items-center gap-2 text-xs">
        {[1, 2, 3].map((s) => (
          <div key={s} className={`flex-1 h-1 rounded ${step >= s ? "bg-primary" : "bg-muted"}`} />
        ))}
      </div>
      <div className="text-xs text-muted-foreground">Step {step} of 3: {step === 1 ? t("subject.identity") : step === 2 ? t("subject.limits") : t("subject.serviceSelect")}</div>

      {step === 1 && (
        <div className="space-y-4">
          <div>
            <label className="block text-xs font-medium mb-1">{t("subject.username")} *</label>
            <div className="flex gap-2">
              <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="john_doe" className="flex-1" />
              <Button variant="outline" size="sm" onClick={() => setName(`user_${Math.random().toString(36).substring(2, 8)}`)}>Gen</Button>
            </div>
          </div>
          <div>
            <label className="block text-xs font-medium mb-1">{t("subject.note")}</label>
            <Input value={note} onChange={(e) => setNote(e.target.value)} placeholder="Notes..." />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-xs font-medium mb-1">{t("subject.telegramId")}</label>
              <Input value={telegramId} onChange={(e) => setTelegramId(e.target.value)} placeholder="@username or ID" />
            </div>
            <div>
              <label className="block text-xs font-medium mb-1">{t("subject.autoDelete")}</label>
              <Input type="number" value={autoDelete} onChange={(e) => setAutoDelete(e.target.value)} placeholder="Days, empty=never" />
            </div>
          </div>
        </div>
      )}

      {step === 2 && (
        <div className="space-y-4">
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-xs font-medium mb-1">{t("subject.expireDays")}</label>
              <Input type="number" value={expireDays} onChange={(e) => setExpireDays(e.target.value)} placeholder={t("filters.unlimited")} />
            </div>
            <div>
              <label className="block text-xs font-medium mb-1">{t("subject.quotaGB")}</label>
              <Input type="number" value={quotaGB} onChange={(e) => setQuotaGB(e.target.value)} placeholder={t("filters.unlimited")} />
            </div>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-xs font-medium mb-1">{t("subject.ipLimit")}</label>
              <Input type="number" value={ipLimit} onChange={(e) => setIpLimit(e.target.value)} placeholder="Unlimited" />
            </div>
            <div>
              <label className="block text-xs font-medium mb-1">{t("subject.deviceLimit")}</label>
              <Input type="number" value={deviceLimit} onChange={(e) => setDeviceLimit(e.target.value)} placeholder="Unlimited" />
            </div>
          </div>
          <div>
            <label className="block text-xs font-medium mb-1">{t("subject.resetStrategy")}</label>
            <select value={resetStrategy} onChange={(e) => setResetStrategy(e.target.value)} className="w-full h-9 rounded-md border border-input bg-background px-3 text-sm">
              <option value="no_reset">{t("subject.noReset")}</option>
              <option value="daily">{t("subject.daily")}</option>
              <option value="weekly">{t("subject.weekly")}</option>
              <option value="monthly">{t("subject.monthly")}</option>
            </select>
          </div>
        </div>
      )}

      {step === 3 && (
        <div className="space-y-4">
          <label className="block text-xs font-medium mb-1">{t("subject.serviceSelect")} *</label>
          <div className="max-h-64 space-y-2 overflow-y-auto rounded-md border border-border p-3">
            {(services.data?.services ?? []).length === 0 ? (
              <p className="text-xs text-muted-foreground">{t("subject.noInbounds")}</p>
            ) : (
              (services.data?.services ?? []).map((s) => (
                <label key={s.id} className="flex items-center gap-2 text-sm p-2 rounded hover:bg-muted cursor-pointer">
                  <input
                    type="checkbox"
                    checked={serviceIDs.includes(s.id)}
                    onChange={(e) =>
                      setServiceIDs((cur) =>
                        e.target.checked ? [...cur, s.id] : cur.filter((id) => id !== s.id)
                      )
                    }
                  />
                  <div className="flex-1">
                    <div className="font-medium">{s.node_name} · {s.adapter_kind} {s.params?.protocol ? `/${s.params.protocol}` : ""}</div>
                    <div className="text-xs text-muted-foreground">:{s.params?.port} • ID {s.id}</div>
                  </div>
                  <Badge variant="outline" className="text-xs">{s.params?.protocol ?? s.adapter_kind}</Badge>
                </label>
              ))
            )}
          </div>
          <div className="text-xs text-muted-foreground">
            {serviceIDs.length} selected • {t("subject.multiProtocol")} supported via multiple selection
          </div>
        </div>
      )}

      {create.error && (
        <div className="rounded bg-destructive/10 p-3 text-sm text-destructive">
          {(create.error as any).message ?? "Failed to create"}
        </div>
      )}

      <div className="flex gap-2 justify-between pt-4 border-t">
        <Button variant="outline" size="sm" disabled={step === 1} onClick={() => setStep((s) => s - 1)}>
          {t("common.back")}
        </Button>
        <div className="flex gap-2">
          <Button variant="ghost" size="sm" onClick={onClose}>{t("cancel")}</Button>
          {step < 3 ? (
            <Button size="sm" onClick={() => setStep((s) => s + 1)} disabled={step === 1 && !name}>
              {t("common.next")}
            </Button>
          ) : (
            <Button size="sm" onClick={() => create.mutate()} disabled={create.isPending || serviceIDs.length === 0}>
              {create.isPending ? t("loading") : t("create")}
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}
