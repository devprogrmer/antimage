import { useState, useEffect, useCallback } from "react";
import { useTranslation } from "react-i18n";
import { api } from "../lib/api";

interface Device {
  device_id: string;
  first_seen: number;
  last_seen: number;
  connection_count: number;
  total_bytes_up: number;
  total_bytes_down: number;
  last_ip_address?: string;
}

interface DeviceListProps {
  subjectId: number;
}

export function DeviceList({ subjectId }: DeviceListProps) {
  const { t } = useTranslation();
  const [devices, setDevices] = useState<Device[]>([]);
  const [loading, setLoading] = useState(true);

  const loadDevices = useCallback(async () => {
    setLoading(true);
    try {
      const data = await api.get<any>(`/api/v1/subjects/${subjectId}/devices`);
      setDevices(data.devices || []);
    } catch (err) {
      console.error("Failed to load devices:", err);
    } finally {
      setLoading(false);
    }
  }, [subjectId]);

  useEffect(() => {
    loadDevices();
  }, [loadDevices]);

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

  function getDeviceStatus(lastSeen: number): { label: string; color: string } {
    const now = Date.now() / 1000;
    const diffSeconds = now - lastSeen;

    if (diffSeconds < 300) {
      return { label: t('devices.online'), color: "text-green-400" };
    } else if (diffSeconds < 3600) {
      return { label: t('devices.recently_active'), color: "text-yellow-400" };
    } else {
      return { label: t('devices.offline'), color: "text-zinc-500" };
    }
  }

  return (
    <div>
      <div className="mb-4">
        <h3 className="text-sm font-semibold">{t('devices.title')}</h3>
        <p className="text-xs text-zinc-400 mt-1">
          {t('devices.count', { count: devices.length })}
        </p>
      </div>

      {loading ? (
        <div className="text-center py-8 text-sm text-zinc-400">{t('common.loading')}</div>
      ) : devices.length === 0 ? (
        <div className="text-center py-8 text-sm text-zinc-400">{t('devices.no_devices')}</div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          {devices.map((device) => {
            const status = getDeviceStatus(device.last_seen);
            return (
              <div key={device.device_id} className="bg-zinc-900 border border-zinc-800 rounded p-4">
                <div className="flex items-start justify-between mb-3">
                  <div className="flex-1">
                    <div className="font-mono text-xs text-zinc-300 mb-1">
                      {device.device_id}
                    </div>
                    <div className={`text-xs font-semibold ${status.color}`}>
                      {status.label}
                    </div>
                  </div>
                </div>

                <div className="space-y-2 text-xs text-zinc-400">
                  <div className="flex justify-between">
                    <span>{t('devices.first_seen')}:</span>
                    <span className="text-zinc-300">{formatTimestamp(device.first_seen)}</span>
                  </div>
                  <div className="flex justify-between">
                    <span>{t('devices.last_seen')}:</span>
                    <span className="text-zinc-300">{formatTimestamp(device.last_seen)}</span>
                  </div>
                  <div className="flex justify-between">
                    <span>{t('devices.connections')}:</span>
                    <span className="text-zinc-300">{device.connection_count}</span>
                  </div>
                  <div className="flex justify-between">
                    <span>{t('devices.upload')}:</span>
                    <span className="text-blue-400">{formatBytes(device.total_bytes_up)}</span>
                  </div>
                  <div className="flex justify-between">
                    <span>{t('devices.download')}:</span>
                    <span className="text-green-400">{formatBytes(device.total_bytes_down)}</span>
                  </div>
                  <div className="flex justify-between">
                    <span>{t('devices.total_traffic')}:</span>
                    <span className="text-zinc-100 font-semibold">
                      {formatBytes(device.total_bytes_up + device.total_bytes_down)}
                    </span>
                  </div>
                  {device.last_ip_address && (
                    <div className="flex justify-between pt-2 border-t border-zinc-800">
                      <span>{t('devices.last_ip')}:</span>
                      <span className="text-zinc-300 font-mono">{device.last_ip_address}</span>
                    </div>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
