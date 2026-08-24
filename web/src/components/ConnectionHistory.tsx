import { useState, useEffect, useCallback } from "react";
import { api } from "../lib/api";
import { t } from "../i18n";

interface Connection {
  id: number;
  subject_id: number;
  start_time: number;
  end_time?: number;
  duration: number;
  bytes_up: number;
  bytes_down: number;
  ip_address?: string;
  device_id?: string;
  node_id?: number;
  protocol?: string;
}

interface ConnectionHistoryProps {
  subjectId: number;
}

export function ConnectionHistory({ subjectId }: ConnectionHistoryProps) {
  const [connections, setConnections] = useState<Connection[]>([]);
  const [loading, setLoading] = useState(true);
  const [sortBy, setSortBy] = useState<"start_time" | "duration" | "traffic">("start_time");

  const loadConnections = useCallback(async () => {
    setLoading(true);
    try {
      const data = await api.get<any>(`/api/v1/subjects/${subjectId}/connections?limit=100`);
      setConnections(data.connections || []);
    } catch (err) {
      console.error("Failed to load connections:", err);
    } finally {
      setLoading(false);
    }
  }, [subjectId]);

  useEffect(() => {
    loadConnections();
  }, [loadConnections]);

  function formatDuration(seconds: number): string {
    if (seconds < 60) return `${seconds}s`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
    const hours = Math.floor(seconds / 3600);
    const mins = Math.floor((seconds % 3600) / 60);
    return `${hours}h ${mins}m`;
  }

  function formatBytes(bytes: number): string {
    if (bytes === 0) return "0 B";
    const k = 1024;
    const sizes = ["B", "KB", "MB", "GB", "TB"];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return Math.round((bytes / Math.pow(k, i)) * 100) / 100 + " " + sizes[i];
  }

  function formatTimestamp(ts: number): string {
    return new Date(ts * 1000).toLocaleString();
  }

  const sortedConnections = [...connections].sort((a, b) => {
    if (sortBy === "start_time") return b.start_time - a.start_time;
    if (sortBy === "duration") return b.duration - a.duration;
    if (sortBy === "traffic") return (b.bytes_up + b.bytes_down) - (a.bytes_up + a.bytes_down);
    return 0;
  });

  return (
    <div>
      <div className="mb-4 flex items-center gap-4">
        <h3 className="text-sm font-semibold">{t('connections.history')}</h3>
        <select
          value={sortBy}
          onChange={(e) => setSortBy(e.target.value as any)}
          className="px-3 py-1.5 bg-zinc-800 border border-zinc-700 rounded text-xs"
        >
          <option value="start_time">{t('connections.sort_recent')}</option>
          <option value="duration">{t('connections.sort_longest')}</option>
          <option value="traffic">{t('connections.sort_traffic')}</option>
        </select>
      </div>

      {loading ? (
        <div className="text-center py-8 text-sm text-zinc-400">{t('common.loading')}</div>
      ) : connections.length === 0 ? (
        <div className="text-center py-8 text-sm text-zinc-400">{t('connections.no_connections')}</div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-xs border-collapse">
            <thead>
              <tr className="border-b border-zinc-800 text-start">
                <th className="py-2 px-2">{t('connections.start_time')}</th>
                <th className="py-2 px-2">{t('connections.duration')}</th>
                <th className="py-2 px-2">{t('connections.upload')}</th>
                <th className="py-2 px-2">{t('connections.download')}</th>
                <th className="py-2 px-2">{t('connections.total')}</th>
                <th className="py-2 px-2">{t('connections.ip_address')}</th>
                <th className="py-2 px-2">{t('connections.device')}</th>
                <th className="py-2 px-2">{t('connections.protocol')}</th>
                <th className="py-2 px-2">{t('connections.status')}</th>
              </tr>
            </thead>
            <tbody>
              {sortedConnections.map((conn) => (
                <tr key={conn.id} className="border-b border-zinc-800 hover:bg-zinc-900">
                  <td className="py-2 px-2">{formatTimestamp(conn.start_time)}</td>
                  <td className="py-2 px-2">{formatDuration(conn.duration)}</td>
                  <td className="py-2 px-2 text-blue-400">{formatBytes(conn.bytes_up)}</td>
                  <td className="py-2 px-2 text-green-400">{formatBytes(conn.bytes_down)}</td>
                  <td className="py-2 px-2 font-semibold">{formatBytes(conn.bytes_up + conn.bytes_down)}</td>
                  <td className="py-2 px-2 text-zinc-400">{conn.ip_address || "-"}</td>
                  <td className="py-2 px-2 text-zinc-400 font-mono text-xs">{conn.device_id || "-"}</td>
                  <td className="py-2 px-2">
                    {conn.protocol ? (
                      <span className="px-2 py-0.5 bg-zinc-800 rounded">{conn.protocol}</span>
                    ) : (
                      "-"
                    )}
                  </td>
                  <td className="py-2 px-2">
                    {conn.end_time ? (
                      <span className="text-zinc-500">{t('connections.ended')}</span>
                    ) : (
                      <span className="text-green-400">● {t('connections.active')}</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
