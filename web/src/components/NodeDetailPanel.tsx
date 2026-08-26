import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { can, useSession } from "../lib/session";
import { MutationError } from "../routes/Resellers";
import { formatTimestamp, t } from "../i18n";
import { NodeHealth } from "./NodeHealth";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "./ui/tabs";

interface NodeDetailData {
  id: number;
  name: string;
  address: string;
  status: string;
  online: boolean;
  maintenance_status: string;
  agent_version: string;
  os_info: string;
}

interface Service {
  id: number;
  name: string;
  protocol: string;
  port: number;
  status: string;
}

interface LogEntry {
  timestamp: number;
  level: string;
  message: string;
}

interface NodeDetailPanelProps {
  nodeId: number;
}

/**
 * Node detail panel: metrics charts, service list, recent logs, maintenance mode toggle.
 * 
 * Comprehensive node detail view with health metrics, running services,
 * log stream, and operational controls.
 */
export function NodeDetailPanel({ nodeId }: NodeDetailPanelProps) {
  const session = useSession();
  const queryClient = useQueryClient();
  const [maintenanceConfirm, setMaintenanceConfirm] = useState(false);

  const node = useQuery({
    queryKey: ["node", nodeId],
    queryFn: () => api.get<NodeDetailData>(`/api/v1/nodes/${nodeId}`),
    refetchInterval: 5000,
  });

  const services = useQuery({
    queryKey: ["node", nodeId, "services"],
    queryFn: () => api.get<{ services: Service[] }>(`/api/v1/nodes/${nodeId}/services`),
    refetchInterval: 10000,
  });

  const logs = useQuery({
    queryKey: ["node", nodeId, "logs"],
    queryFn: () =>
      api.get<{ logs: LogEntry[] }>(`/api/v1/nodes/${nodeId}/logs?limit=50`),
    refetchInterval: 5000,
  });

  const toggleMaintenance = useMutation({
    mutationFn: (enabled: boolean) =>
      api.post(`/api/v1/nodes/${nodeId}/maintenance`, {
        enabled,
        reason: enabled ? "Manual maintenance mode" : "Exit maintenance mode",
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["node", nodeId] });
      setMaintenanceConfirm(false);
    },
  });

  const restart = useMutation({
    mutationFn: () => api.post(`/api/v1/nodes/${nodeId}/restart`, {}),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["node", nodeId] });
    },
  });

  const mayWrite = can(session.data, "node:write");
  const nodeData = node.data;
  const inMaintenance = nodeData?.maintenance_status === "enabled";

  if (!nodeData) {
    return (
      <div className="py-8 text-center text-sm text-muted-foreground">
        {t("common.loading")}...
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <h3 className="font-mono text-base font-semibold">{nodeData.name}</h3>
            <Badge variant={nodeData.online ? "success" : "destructive"}>
              {nodeData.online ? t("node.online") : t("node.offline")}
            </Badge>
            {inMaintenance && (
              <Badge variant="warning">{t("node.maintenanceMode")}</Badge>
            )}
          </div>
          <p className="font-mono text-xs text-muted-foreground">{nodeData.address}</p>
        </div>

        {mayWrite && (
          <div className="flex gap-2">
            <Button
              size="sm"
              variant={inMaintenance ? "destructive" : "outline"}
              onClick={() => setMaintenanceConfirm(true)}
              disabled={toggleMaintenance.isPending}
            >
              {inMaintenance ? t("node.exitMaintenance") : t("node.enterMaintenance")}
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => restart.mutate()}
              disabled={restart.isPending || inMaintenance}
            >
              {t("node.restart")}
            </Button>
          </div>
        )}
      </header>

      <MutationError error={toggleMaintenance.error || restart.error} />

      {maintenanceConfirm && (
        <div className="rounded-lg border border-warning bg-warning/10 p-4">
          <p className="text-sm font-medium">
            {inMaintenance
              ? t("node.confirmExitMaintenance")
              : t("node.confirmEnterMaintenance")}
          </p>
          <p className="mt-1 text-xs text-muted-foreground">
            {inMaintenance
              ? t("node.exitMaintenanceDesc")
              : t("node.enterMaintenanceDesc")}
          </p>
          <div className="mt-3 flex gap-2">
            <Button
              size="sm"
              onClick={() => toggleMaintenance.mutate(!inMaintenance)}
              disabled={toggleMaintenance.isPending}
            >
              {t("common.confirm")}
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => setMaintenanceConfirm(false)}
            >
              {t("common.cancel")}
            </Button>
          </div>
        </div>
      )}

      <Tabs defaultValue="metrics">
        <TabsList>
          <TabsTrigger value="metrics">{t("node.metrics")}</TabsTrigger>
          <TabsTrigger value="services">{t("node.services")}</TabsTrigger>
          <TabsTrigger value="logs">{t("node.logs")}</TabsTrigger>
        </TabsList>

        <TabsContent value="metrics" className="space-y-4">
          <NodeHealth nodeId={nodeId} />
        </TabsContent>

        <TabsContent value="services" className="space-y-4">
          <ServicesList services={services.data?.services ?? []} isLoading={services.isLoading} />
        </TabsContent>

        <TabsContent value="logs" className="space-y-4">
          <LogsViewer logs={logs.data?.logs ?? []} isLoading={logs.isLoading} />
        </TabsContent>
      </Tabs>

      <SystemInfo nodeData={nodeData} />
    </div>
  );
}

interface ServicesListProps {
  services: Service[];
  isLoading: boolean;
}

function ServicesList({ services, isLoading }: ServicesListProps) {
  if (isLoading) {
    return (
      <p className="py-4 text-center text-xs text-muted-foreground">
        {t("common.loading")}...
      </p>
    );
  }

  if (services.length === 0) {
    return (
      <p className="py-4 text-center text-xs text-muted-foreground">
        {t("node.noServices")}
      </p>
    );
  }

  return (
    <div className="space-y-2">
      {services.map((service) => (
        <div
          key={service.id}
          className="flex items-center justify-between rounded-lg border border-border bg-card p-3"
        >
          <div className="space-y-1">
            <div className="flex items-center gap-2">
              <span className="font-mono text-sm font-medium">{service.name}</span>
              <ServiceStatusBadge status={service.status} />
            </div>
            <div className="flex gap-3 text-xs text-muted-foreground">
              <span>
                {t("node.protocol")}: <span className="font-mono">{service.protocol}</span>
              </span>
              <span>
                {t("node.port")}: <span className="font-mono">{service.port}</span>
              </span>
            </div>
          </div>
        </div>
      ))}
    </div>
  );
}

function ServiceStatusBadge({ status }: { status: string }) {
  const variant =
    status === "running"
      ? "success"
      : status === "stopped"
      ? "destructive"
      : "outline";
  return (
    <Badge variant={variant} className="text-[10px]">
      {status}
    </Badge>
  );
}

interface LogsViewerProps {
  logs: LogEntry[];
  isLoading: boolean;
}

function LogsViewer({ logs, isLoading }: LogsViewerProps) {
  if (isLoading) {
    return (
      <p className="py-4 text-center text-xs text-muted-foreground">
        {t("common.loading")}...
      </p>
    );
  }

  if (logs.length === 0) {
    return (
      <p className="py-4 text-center text-xs text-muted-foreground">
        {t("node.noLogs")}
      </p>
    );
  }

  return (
    <div className="rounded-lg border border-border bg-background font-mono text-[10px]">
      <div className="max-h-96 overflow-y-auto p-2">
        {logs.map((log, idx) => (
          <div
            key={idx}
            className={`border-b border-border/50 py-1 last:border-0 ${
              log.level === "error"
                ? "text-destructive"
                : log.level === "warn"
                ? "text-warning"
                : "text-foreground"
            }`}
          >
            <span className="text-muted-foreground">
              {formatTimestamp(log.timestamp)}
            </span>{" "}
            <span className="font-semibold uppercase">[{log.level}]</span> {log.message}
          </div>
        ))}
      </div>
    </div>
  );
}

function SystemInfo({ nodeData }: { nodeData: NodeDetailData }) {
  return (
    <section className="rounded-lg border border-border bg-card p-3">
      <h4 className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        {t("node.systemInfo")}
      </h4>
      <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs">
        <dt className="text-muted-foreground">{t("node.agent")}:</dt>
        <dd className="font-mono">{nodeData.agent_version || "—"}</dd>
        <dt className="text-muted-foreground">{t("node.os")}:</dt>
        <dd className="font-mono">{nodeData.os_info || "—"}</dd>
        <dt className="text-muted-foreground">{t("node.status")}:</dt>
        <dd className="font-mono">{nodeData.status}</dd>
      </dl>
    </section>
  );
}
