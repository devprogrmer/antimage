import { useState, useEffect, useCallback } from "react";
import { api } from "../lib/api";
import { formatRelativeTime, t } from "../i18n";

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
        <h3 className="text-sm font-semibold">{t('activity.timeline')}</h3>
        <select
          value={eventTypeFilter}
          onChange={(e) => {
            setEventTypeFilter(e.target.value);
            setOffset(0);
          }}
          className="px-3 py-1.5 bg-secondary border border-input rounded text-xs"
        >
          <option value="">{t('activity.all_events')}</option>
          <option value="connection_start">{t('activity.connection_start')}</option>
          <option value="connection_end">{t('activity.connection_end')}</option>
          <option value="traffic_update">{t('activity.traffic_update')}</option>
          <option value="quota_exceeded">{t('activity.quota_exceeded')}</option>
          <option value="enabled">{t('activity.enabled')}</option>
          <option value="disabled">{t('activity.disabled')}</option>
        </select>
      </div>

      {loading && offset === 0 ? (
        <div className="text-center py-8 text-sm text-muted-foreground">{t('common.loading')}</div>
      ) : activities.length === 0 ? (
        <div className="text-center py-8 text-sm text-muted-foreground">{t('activity.no_activity')}</div>
      ) : (
        <div className="space-y-3">
          {activities.map((activity) => (
            <div key={activity.id} className="bg-card border border-border rounded p-3">
              <div className="flex items-start gap-3">
                <span className="text-2xl">{getEventIcon(activity.event_type)}</span>
                <div className="flex-1">
                  <div className="flex items-center gap-2 mb-1">
                    <span className="text-sm font-medium">
                      {activity.event_type.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase())}
                    </span>
                    <span className="text-xs text-muted-foreground">
                      {formatRelativeTime(activity.timestamp)}
                    </span>
                  </div>

                  <div className="text-xs text-muted-foreground space-y-1">
                    {activity.ip_address && (
                      <div>{t('activity.ip')}: {activity.ip_address}</div>
                    )}
                    {activity.device_id && (
                      <div>{t('activity.device')}: {activity.device_id}</div>
                    )}
                    {(activity.bytes_up > 0 || activity.bytes_down > 0) && (
                      <div>
                        {t('activity.traffic')}: ↑ {formatBytes(activity.bytes_up)} / ↓ {formatBytes(activity.bytes_down)}
                      </div>
                    )}
                    {activity.details && (
                      <div className="mt-2 p-2 bg-background rounded font-mono text-xs">
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
              className="w-full py-2 text-xs text-muted-foreground hover:text-foreground disabled:opacity-50"
            >
              {loading ? t('common.loading') : t('common.load_more')}
            </button>
          )}
        </div>
      )}
    </div>
  );
}
