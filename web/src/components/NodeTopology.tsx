import { useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";

import { api } from "../lib/api";
import { can, useSession } from "../lib/session";
import { formatNumber, t } from "../i18n";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";

interface NodeSummary {
  id: number;
  name: string;
  address: string;
  status: string;
  desired_revision: number;
  applied_revision: number;
  online: boolean;
  last_seen_at: number | null;
  config_drift: number;
  maintenance_status: string;
  agent_version: string;
  os_info: string;
}

interface TopologyStats {
  total: number;
  online: number;
  drifted: number;
  maintenance: number;
  healthy: number;
}

/**
 * Node topology visualization: visual map with status indicators.
 * 
 * Grid layout showing all nodes with color-coded status badges, drift warnings,
 * and quick-action buttons. Designed for fleet-wide operational awareness.
 */
export function NodeTopology() {
  const session = useSession();
  const [filter, setFilter] = useState<"all" | "online" | "drift" | "maintenance">("all");
  const [selectedNode, setSelectedNode] = useState<number | null>(null);

  const nodes = useQuery({
    queryKey: ["nodes"],
    queryFn: () => api.get<{ nodes: NodeSummary[] }>("/api/v1/nodes"),
    refetchInterval: 5000, // Live updates
  });

  const stats = useMemo<TopologyStats>(() => {
    const all = nodes.data?.nodes ?? [];
    return {
      total: all.length,
      online: all.filter((n) => n.online).length,
      drifted: all.filter((n) => n.config_drift > 0).length,
      maintenance: all.filter((n) => n.maintenance_status === "enabled").length,
      healthy: all.filter(
        (n) =>
          n.online &&
          n.status === "converged" &&
          n.config_drift === 0 &&
          n.maintenance_status !== "enabled"
      ).length,
    };
  }, [nodes.data]);

  const filtered = useMemo(() => {
    const all = nodes.data?.nodes ?? [];
    switch (filter) {
      case "online":
        return all.filter((n) => n.online);
      case "drift":
        return all.filter((n) => n.config_drift > 0);
      case "maintenance":
        return all.filter((n) => n.maintenance_status === "enabled");
      default:
        return all;
    }
  }, [nodes.data, filter]);

  const mayWrite = can(session.data, "node:write");

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-center gap-3">
        <h2 className="text-base font-semibold">{t("topology.title")}</h2>
        <div className="flex flex-wrap gap-2 text-xs">
          <Badge variant="outline">
            {t("topology.total")}: {formatNumber(stats.total)}
          </Badge>
          <Badge variant="success">
            {t("topology.online")}: {formatNumber(stats.online)}
          </Badge>
          <Badge variant="warning">
            {t("topology.drift")}: {formatNumber(stats.drifted)}
          </Badge>
          <Badge variant="secondary">
            {t("topology.maintenance")}: {formatNumber(stats.maintenance)}
          </Badge>
        </div>
      </header>

      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          variant={filter === "all" ? "default" : "outline"}
          onClick={() => setFilter("all")}
        >
          {t("topology.filterAll")}
        </Button>
        <Button
          size="sm"
          variant={filter === "online" ? "default" : "outline"}
          onClick={() => setFilter("online")}
        >
          {t("topology.filterOnline")}
        </Button>
        <Button
          size="sm"
          variant={filter === "drift" ? "default" : "outline"}
          onClick={() => setFilter("drift")}
        >
          {t("topology.filterDrift")}
        </Button>
        <Button
          size="sm"
          variant={filter === "maintenance" ? "default" : "outline"}
          onClick={() => setFilter("maintenance")}
        >
          {t("topology.filterMaintenance")}
        </Button>
      </div>

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
        {filtered.map((node) => (
          <NodeCard
            key={node.id}
            node={node}
            selected={selectedNode === node.id}
            onSelect={() => setSelectedNode(node.id === selectedNode ? null : node.id)}
            mayWrite={mayWrite}
          />
        ))}
      </div>

      {filtered.length === 0 && (
        <p className="py-8 text-center text-sm text-muted-foreground">
          {t("topology.noNodes")}
        </p>
      )}
    </div>
  );
}

interface NodeCardProps {
  node: NodeSummary;
  selected: boolean;
  onSelect: () => void;
  mayWrite: boolean;
}

function NodeCard({ node, selected, onSelect, mayWrite }: NodeCardProps) {
  const drift = node.desired_revision !== node.applied_revision;
  const maintenance = node.maintenance_status === "enabled";

  // Status indicator color
  const statusColor = node.online
    ? node.status === "converged"
      ? "bg-success"
      : node.status === "applying"
      ? "bg-warning"
      : "bg-destructive"
    : "bg-muted-foreground";

  return (
    <div
      className={`relative cursor-pointer rounded-lg border p-3 transition-all ${
        selected
          ? "border-primary bg-accent ring-2 ring-primary ring-offset-2"
          : "border-border bg-card hover:border-primary/50"
      }`}
      onClick={onSelect}
    >
      {/* Status indicator dot */}
      <div
        className={`absolute right-2 top-2 h-3 w-3 rounded-full ${statusColor} ${
          node.online ? "animate-pulse" : ""
        }`}
        title={node.online ? t("node.online") : t("node.offline")}
      />

      <div className="space-y-2">
        <div>
          <h3 className="truncate font-mono text-sm font-semibold">{node.name}</h3>
          <p className="truncate font-mono text-xs text-muted-foreground">{node.address}</p>
        </div>

        <div className="flex flex-wrap gap-1">
          <StatusBadge status={node.status} size="sm" />
          {drift && (
            <Badge variant="warning" className="text-[10px]">
              {t("node.drift")}
            </Badge>
          )}
          {maintenance && (
            <Badge variant="secondary" className="text-[10px]">
              {t("node.maintenance")}
            </Badge>
          )}
        </div>

        <dl className="grid grid-cols-2 gap-x-2 gap-y-1 text-[10px]">
          <dt className="text-muted-foreground">{t("node.revision")}:</dt>
          <dd className="font-mono">
            {formatNumber(node.applied_revision)}/{formatNumber(node.desired_revision)}
          </dd>
          {node.agent_version && (
            <>
              <dt className="text-muted-foreground">{t("node.agent")}:</dt>
              <dd className="truncate font-mono" title={node.agent_version}>
                {node.agent_version}
              </dd>
            </>
          )}
        </dl>

        {selected && mayWrite && (
          <div className="flex gap-1 pt-1">
            <a
              href={`#/nodes/${node.id}`}
              className="flex-1 rounded bg-primary px-2 py-1 text-center text-[10px] text-primary-foreground hover:bg-primary/90"
              onClick={(e) => e.stopPropagation()}
            >
              {t("common.view")}
            </a>
            {drift && (
              <button
                type="button"
                className="flex-1 rounded bg-success/15 px-2 py-1 text-[10px] text-success hover:bg-success/25"
                onClick={(e) => {
                  e.stopPropagation();
                  // Trigger sync action
                }}
              >
                {t("node.sync")}
              </button>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

function StatusBadge({ status, size }: { status: string; size?: "sm" }) {
  const className = size === "sm" ? "text-[10px] px-1.5 py-0" : "";
  if (status === "converged") return <Badge variant="success" className={className}>{status}</Badge>;
  if (status === "applying") return <Badge variant="warning" className={className}>{status}</Badge>;
  if (status === "failed") return <Badge variant="destructive" className={className}>{status}</Badge>;
  return <Badge variant="outline" className={className}>{status}</Badge>;
}
