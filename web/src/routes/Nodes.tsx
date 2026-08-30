import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";

import { api } from "../lib/api";
import { formatNumber, formatTimestamp, t } from "../i18n";
import { StatusBadge, type NodeStatus } from "../components/StatusBadge";
import { DataTable } from "../components/DataTable";
import type { Column } from "../components/DataTable";
import { useNodeStream } from "../lib/useNodeStream";
import { AddNode } from "../components/AddNode";
import { BulkNodeActions } from "../components/NodeActions";
import { Button } from "../components/ui/button";

interface NodeRow {
  id: number;
  name: string;
  address: string;
  status: NodeStatus;
  desired_revision: number;
  applied_revision: number;
  last_seen_at: number | null;
  online: boolean;
}

export function Nodes({ onSelect }: { onSelect: (id: number) => void }) {
  const [adding, setAdding] = useState(false);
  const [selected, setSelected] = useState<Set<string | number>>(new Set());

  const nodes = useQuery({
    queryKey: ["nodes"],
    queryFn: () => api.get<{ nodes: NodeRow[] }>("/api/v1/nodes"),
  });
  // The fetched list is the shape of the table; the stream only overrides the
  // three fields that move, so a node that has not been seen live still renders.
  const live = useNodeStream();

  // Live values folded in BEFORE the table sees them, so sorting by status or
  // revision sorts by what is on screen. Sorting the fetched values while
  // rendering the streamed ones would produce an order that contradicts the
  // column it claims to be sorted by.
  const rows = useMemo(
    () =>
      (nodes.data?.nodes ?? []).map((node) => ({
        ...node,
        status: (live[node.id]?.status ?? node.status) as NodeStatus,
        applied_revision: live[node.id]?.applied_revision ?? node.applied_revision,
        desired_revision: live[node.id]?.desired_revision ?? node.desired_revision,
      })),
    [nodes.data, live],
  );

  // Keep the selection to rows that still exist. A node deleted or filtered
  // away must not stay selected and then be restarted by a fleet action the
  // operator thought applied to what was on screen.
  useEffect(() => {
    if (nodes.data === undefined) return;
    setSelected((prev) => {
      if (prev.size === 0) return prev;
      const visible = new Set<string | number>(nodes.data.nodes.map((n) => n.id));
      const next = new Set([...prev].filter((id) => visible.has(id)));
      return next.size === prev.size ? prev : next;
    });
  }, [nodes.data]);

  const selectedNodes = rows.filter((n) => selected.has(n.id));

  const columns: Column<NodeRow>[] = [
    {
      id: "name",
      header: t("node.name"),
      sortValue: (n) => n.name,
      // Pinned: a row with no name is unidentifiable, and the link in it is
      // the keyboard path to the detail screen.
      hideable: false,
      cell: (n) => (
        <Link
          to={`/nodes/${n.id}`}
          // The row is clickable for the mouse; this is what a keyboard and a
          // screen reader use, and it is what makes middle-click open a node
          // in a new tab the way an operator expects.
          onClick={(e) => e.stopPropagation()}
          className="font-mono hover:underline"
        >
          {n.name}
        </Link>
      ),
    },
    {
      id: "address",
      header: t("node.address"),
      sortValue: (n) => n.address,
      className: "font-mono text-xs text-muted-foreground",
      cell: (n) => n.address,
    },
    {
      id: "status",
      header: t("node.status"),
      sortValue: (n) => n.status,
      cell: (n) => <StatusBadge status={n.status} />,
    },
    {
      id: "revision",
      header: t("node.revision"),
      // Sorted by the SIZE of the drift, not the revision number: "which nodes
      // are furthest behind" is the question an operator is asking when they
      // sort this column.
      sortValue: (n) => n.desired_revision - n.applied_revision,
      className: "font-mono text-xs",
      cell: (n) => (
        <>
          {formatNumber(n.applied_revision)} / {formatNumber(n.desired_revision)}
          {n.applied_revision !== n.desired_revision && (
            <span className="ms-2 text-warning">{t("node.drift")}</span>
          )}
        </>
      ),
    },
    {
      id: "lastSeen",
      header: t("node.lastSeen"),
      sortValue: (n) => n.last_seen_at,
      className: "font-mono text-xs text-muted-foreground",
      cell: (n) => formatTimestamp(n.last_seen_at),
    },
  ];

  return (
    <div>
      <div className="mb-4 flex items-center justify-between">
        <h2 className="text-lg font-semibold">{t("nav.nodes")}</h2>
        <Button size="sm" onClick={() => setAdding(true)}>
          {t("node.add")}
        </Button>
      </div>

      <AddNode open={adding} onOpenChange={setAdding} />

      <BulkNodeActions
        nodes={selectedNodes.map((n) => ({ id: n.id, name: n.name, status: n.status }))}
        onDone={() => setSelected(new Set())}
      />

      <DataTable
        rows={rows}
        columns={columns}
        selected={selected}
        onSelectedChange={setSelected}
        selectionLabel={(n) => n.name}
        rowKey={(n) => n.id}
        onRowActivate={(n) => onSelect(n.id)}
        storageKey="nodes"
        empty={t("node.none")}
        caption={t("nav.nodes")}
      />
    </div>
  );
}
