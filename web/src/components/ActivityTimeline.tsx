import { useState, useEffect, useCallback } from "react";
import { api } from "../lib/api";

interface Activity {
  id: number;
  subject_id: number;
  event_type: string;
  timestamp: number;
  details?: string;
  ip_address?: string;
  device_id?: string;
  node_id?: number;
  bytes_up: number;
  bytes_down: number;
}

interface ActivityTimelineProps {
  subjectId: number;
}

export function ActivityTimeline({ subjectId }: ActivityTimelineProps) {
  const [activities, setActivities] = useState<Activity[]>([]);
  const [loading, setLoading] = useState(true);
  const [hasMore, setHasMore] = useState(false);
  const [offset, setOffset] = useState(0);
  const [eventTypeFilter, setEventTypeFilter] = useState("");

  const loadActivities = useCallback(async () => {
    setLoading(true);
    try {
      const params = new URLSearchParams();
      params.set("limit", "50");
      params.set("offset", offset.toString());
      if (eventTypeFilter) {
        params.set("event_type", eventTypeFilter);
      }

      const data = await api.get<any>(`/api/v1/subjects/${subjectId}/activity?${params.toString()}`);
      if (offset === 0) {
        setActivities(data.activities || []);
      } else {
        setActivities(prev => [...prev, ...(data.activities || [])]);
      }
      setHasMore(data.has_more);
    } catch (err) {
      console.error("Failed to load activities:", err);
    } finally {
      setLoading(false);
    }
  }, [subjectId, offset, eventTypeFilter]);

  useEffect(() => {
    loadActivities();
  }, [loadActivities]);

  function loadMore() {
    setOffset(prev => prev + 50);
    loadActivities();
  }

  function getEventIcon(eventType: string): string {
    const icons: Record<string, string> = {
      connection_start: "🟢",
      connection_end: "🔴",
      traffic_update: "📊",
      quota_exceeded: "⚠️",
      disabled: "🚫",
      enabled: "✅",
      created: "➕",
      deleted: "🗑️",
    };
    return icons[eventType] || "📌";
  }

  function formatTimestamp(ts: number): string {
    const date = new Date(ts * 1000);
    const now = new Date();
    const diffMs = now.getTime() - date.getTime();
    const diffMins = Math.floor(diffMs / 60000);
    const diffHours = Math.floor(diffMs / 3600000);
    const diffDays = Math.floor(diffMs / 86400000);

    if (diffMins < 1) return "Just now";
    if (diffMins < 60) return `${diffMins}m ago`;
    if (diffHours < 24) return `${diffHours}h ago`;
    if (diffDays < 7) return `${diffDays}d ago`;
    return date.toLocaleDateString() + " " + date.toLocaleTimeString();
  }

  function formatBytes(bytes: number): string {
    if (bytes === 0) return "0 B";
    const k = 1024;
    const sizes = ["B", "KB", "MB", "GB", "TB"];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return Math.round((bytes / Math.pow(k, i)) * 100) / 100 + " " + sizes[i];
  }

  return (
    <div>
      <div className="mb-4 flex items-center gap-4">
        <h3 className="text-sm font-semibold">Activity Timeline</h3>
        <select
          value={eventTypeFilter}
          onChange={(e) => {
            setEventTypeFilter(e.target.value);
            setOffset(0);
          }}
          className="px-3 py-1.5 bg-zinc-800 border border-zinc-700 rounded text-xs"
        >
          <option value="">All Events</option>
          <option value="connection_start">Connections Start</option>
          <option value="connection_end">Connections End</option>
          <option value="traffic_update">Traffic Updates</option>
          <option value="quota_exceeded">Quota Exceeded</option>
          <option value="enabled">Enabled</option>
          <option value="disabled">Disabled</option>
        </select>
      </div>

      {loading && offset === 0 ? (
        <div className="text-center py-8 text-sm text-zinc-400">Loading...</div>
      ) : activities.length === 0 ? (
        <div className="text-center py-8 text-sm text-zinc-400">No activity recorded yet</div>
      ) : (
        <div className="space-y-3">
          {activities.map((activity) => (
            <div key={activity.id} className="bg-zinc-900 border border-zinc-800 rounded p-3">
              <div className="flex items-start gap-3">
                <span className="text-2xl">{getEventIcon(activity.event_type)}</span>
                <div className="flex-1">
                  <div className="flex items-center gap-2 mb-1">
                    <span className="text-sm font-medium">
                      {activity.event_type.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase())}
                    </span>
                    <span className="text-xs text-zinc-500">
                      {formatTimestamp(activity.timestamp)}
                    </span>
                  </div>

                  <div className="text-xs text-zinc-400 space-y-1">
                    {activity.ip_address && (
                      <div>IP: {activity.ip_address}</div>
                    )}
                    {activity.device_id && (
                      <div>Device: {activity.device_id}</div>
                    )}
                    {(activity.bytes_up > 0 || activity.bytes_down > 0) && (
                      <div>
                        Traffic: ↑ {formatBytes(activity.bytes_up)} / ↓ {formatBytes(activity.bytes_down)}
                      </div>
                    )}
                    {activity.details && (
                      <div className="mt-2 p-2 bg-zinc-950 rounded font-mono text-xs">
                        {activity.details}
                      </div>
                    )}
                  </div>
                </div>
              </div>
            </div>
          ))}

          {hasMore && (
            <button
              type="button"
              onClick={loadMore}
              disabled={loading}
              className="w-full py-2 text-xs text-zinc-400 hover:text-zinc-100 disabled:opacity-50"
            >
              {loading ? "Loading..." : "Load More"}
            </button>
          )}
        </div>
      )}
    </div>
  );
}
