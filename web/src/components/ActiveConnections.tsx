import { useQuery } from "@tanstack/react-query";

import { api } from "../lib/api";
import { formatTimestamp, t } from "../i18n";

/**
 * Active connections for a subject, right now.
 *
 * `/subjects/{id}/connections` is the live view of the active_connections
 * table (SP17) -- one row per open transport per node. It complements the
 * ActivityTimeline, which shows the historical trace: this shows what is
 * open at this moment. During an incident, this is the answer to "is anybody
 * connected?".
 */

interface ActiveConnection {
  subject_id: number;
  device_id?: number;
  node_id: number;
  connection_id: string;
  source_ip: string;
  connected_at: number;
  last_seen_at: number;
  protocol_info: string;
}

interface ActiveConnectionsProps {
  subjectId: number;
}

export function ActiveConnections({ subjectId }: ActiveConnectionsProps) {
  const connections = useQuery({
    queryKey: ["subject", subjectId, "connections"],
    queryFn: () =>
      api.get<ActiveConnection[]>(`/api/v1/subjects/${subjectId}/connections?limit=100`),
    // The list moves as devices connect and drop off, so a stale five-second
    // window would show an operator a state that no longer exists.
    refetchInterval: 5000,
  });

  const rows = connections.data ?? [];

  return (
    <div className="rounded border border-border bg-card p-4">
      <div className="mb-3 flex items-center justify-between">
        <h3 className="text-sm font-semibold">{t("connections.title")}</h3>
        <span className="text-xs text-muted-foreground">
          {t("connections.count", { count: String(rows.length) })}
        </span>
      </div>

      {rows.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("connections.none")}</p>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-border text-xs text-muted-foreground">
              <th className="py-2 text-start">{t("connections.node")}</th>
              <th className="text-start">{t("connections.sourceIP")}</th>
              <th className="text-start">{t("connections.protocol")}</th>
              <th className="text-start">{t("connections.since")}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((c) => (
              <tr key={c.connection_id} className="border-b border-border">
                <td className="py-1.5 font-mono text-xs">{c.node_id}</td>
                <td className="font-mono text-xs">{c.source_ip}</td>
                <td className="font-mono text-xs text-muted-foreground">
                  {c.protocol_info || "—"}
                </td>
                <td className="font-mono text-xs text-muted-foreground">
                  {formatTimestamp(c.connected_at)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
