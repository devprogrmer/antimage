import { useQuery } from "@tanstack/react-query";
import { api } from "../lib/api";
import { formatNumber, formatTimestamp, t } from "../i18n";
import { StatusBadge, type NodeStatus } from "../components/StatusBadge";
import { useNodeStream } from "../lib/useNodeStream";

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
  const nodes = useQuery({
    queryKey: ["nodes"],
    queryFn: () => api.get<{ nodes: NodeRow[] }>("/api/v1/nodes"),
  });
  // The fetched list is the shape of the table; the stream only overrides the
  // three fields that move, so a node that has not been seen live still renders.
  const live = useNodeStream();

  return (
    <table className="w-full border-collapse text-sm text-zinc-200">
      <thead>
        <tr className="border-b border-zinc-800 text-xs uppercase tracking-wide text-zinc-500">
          <th className="py-2 pe-3 text-start">{t("node.name")}</th>
          <th className="pe-3 text-start">{t("node.address")}</th>
          <th className="pe-3 text-start">{t("node.status")}</th>
          <th className="pe-3 text-start">{t("node.revision")}</th>
          <th className="text-start">{t("node.lastSeen")}</th>
        </tr>
      </thead>
      <tbody>
        {nodes.data?.nodes.map((node) => {
          const status = live[node.id]?.status ?? node.status;
          const applied = live[node.id]?.applied_revision ?? node.applied_revision;
          const desired = live[node.id]?.desired_revision ?? node.desired_revision;
          return (
            <tr
              key={node.id}
              onClick={() => onSelect(node.id)}
              className="cursor-pointer border-b border-zinc-900 hover:bg-zinc-900"
            >
              <td className="py-1.5 pe-3 font-mono">{node.name}</td>
              <td className="pe-3 font-mono text-xs text-zinc-500">{node.address}</td>
              <td className="pe-3">
                <StatusBadge status={status as NodeStatus} />
              </td>
              <td className="pe-3 font-mono text-xs">
                {formatNumber(applied)} / {formatNumber(desired)}
                {applied !== desired && (
                  <span className="ms-2 text-amber-400">{t("node.drift")}</span>
                )}
              </td>
              <td className="font-mono text-xs text-zinc-500">
                {formatTimestamp(node.last_seen_at)}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}
