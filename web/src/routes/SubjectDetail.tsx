import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../lib/api";
import { formatTimestamp, t } from "../i18n";

interface Subject {
  id: number;
  name: string;
  enabled: boolean;
  expires_at: number | null;
  expired_at: number | null;
  created_at: number;
  note: string;
}

interface Device {
  id: number;
  fingerprint: string;
  last_seen_at: number | null;
  last_ip: string;
}

export function SubjectDetail({ subjectId }: { subjectId: number }) {
  const queryClient = useQueryClient();
  const [showCredential, setShowCredential] = useState<string | null>(null);
  const [credentialValue, setCredentialValue] = useState<string>("");

  const subject = useQuery({
    queryKey: ["subject", subjectId],
    queryFn: () => api.get<Subject>(`/api/v1/subjects/${subjectId}`),
  });

  const devices = useQuery({
    queryKey: ["devices", subjectId],
    queryFn: () => api.get<{ devices: Device[] }>(`/api/v1/subjects/${subjectId}/devices`),
  });

  const freeze = useMutation({
    mutationFn: () => api.post(`/api/v1/subjects/${subjectId}/freeze`, { reason: "Manual freeze" }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["subject", subjectId] });
    },
  });

  const unfreeze = useMutation({
    mutationFn: () => api.post(`/api/v1/subjects/${subjectId}/unfreeze`, {}),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["subject", subjectId] });
    },
  });

  const disable = useMutation({
    mutationFn: () => api.put(`/api/v1/subjects/${subjectId}`, { enabled: false }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["subject", subjectId] });
    },
  });

  const enable = useMutation({
    mutationFn: () => api.put(`/api/v1/subjects/${subjectId}`, { enabled: true }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["subject", subjectId] });
    },
  });

  const revealCredential = async (kind: string) => {
    const resp = await api.get<{ kind: string; value: string }>(
      `/api/v1/subjects/${subjectId}/credentials/${kind}`
    );
    setCredentialValue(resp.value);
    setShowCredential(kind);
  };

  if (!subject.data) {
    return <div>{t("loading")}</div>;
  }

  const s = subject.data;

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold font-mono">{s.name}</h2>
        <div className="flex gap-2">
          {s.enabled ? (
            <>
              <button
                type="button"
                onClick={() => freeze.mutate()}
                className="rounded bg-amber-600 px-3 py-1 text-sm hover:bg-amber-700"
              >
                {t("subject.freeze")}
              </button>
              <button
                type="button"
                onClick={() => disable.mutate()}
                className="rounded bg-red-600 px-3 py-1 text-sm hover:bg-red-700"
              >
                {t("subject.disable")}
              </button>
            </>
          ) : (
            <>
              <button
                type="button"
                onClick={() => unfreeze.mutate()}
                className="rounded bg-blue-600 px-3 py-1 text-sm hover:bg-blue-700"
              >
                {t("subject.unfreeze")}
              </button>
              <button
                type="button"
                onClick={() => enable.mutate()}
                className="rounded bg-green-600 px-3 py-1 text-sm hover:bg-green-700"
              >
                {t("subject.enable")}
              </button>
            </>
          )}
        </div>
      </div>

      <div className="rounded border border-zinc-800 bg-zinc-900 p-4">
        <h3 className="mb-3 text-sm font-semibold">{t("subject.details")}</h3>
        <dl className="grid grid-cols-2 gap-3 text-sm">
          <div>
            <dt className="text-zinc-400">{t("subject.status")}</dt>
            <dd>{s.enabled ? t("subject.enabled") : t("subject.disabled")}</dd>
          </div>
          <div>
            <dt className="text-zinc-400">{t("subject.created")}</dt>
            <dd className="font-mono text-xs">{formatTimestamp(s.created_at)}</dd>
          </div>
          {s.expires_at && (
            <div>
              <dt className="text-zinc-400">{t("subject.expires")}</dt>
              <dd className="font-mono text-xs">{formatTimestamp(s.expires_at)}</dd>
            </div>
          )}
          {s.note && (
            <div className="col-span-2">
              <dt className="text-zinc-400">{t("subject.note")}</dt>
              <dd>{s.note}</dd>
            </div>
          )}
        </dl>
      </div>

      <div className="rounded border border-zinc-800 bg-zinc-900 p-4">
        <h3 className="mb-3 text-sm font-semibold">{t("subject.credentials")}</h3>
        <div className="flex gap-2">
          <button
            type="button"
            onClick={() => revealCredential("uuid")}
            className="rounded bg-zinc-800 px-3 py-1 text-sm hover:bg-zinc-700"
          >
            {t("subject.revealUUID")}
          </button>
          <button
            type="button"
            onClick={() => revealCredential("password")}
            className="rounded bg-zinc-800 px-3 py-1 text-sm hover:bg-zinc-700"
          >
            {t("subject.revealPassword")}
          </button>
        </div>
        {showCredential && (
          <div className="mt-3 rounded bg-zinc-950 p-3">
            <div className="text-xs text-zinc-400">{showCredential}</div>
            <div className="font-mono text-sm">{credentialValue}</div>
          </div>
        )}
      </div>

      <div className="rounded border border-zinc-800 bg-zinc-900 p-4">
        <h3 className="mb-3 text-sm font-semibold">{t("subject.devices")}</h3>
        {devices.data?.devices.length === 0 ? (
          <div className="text-sm text-zinc-400">{t("subject.noDevices")}</div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-zinc-800 text-xs text-zinc-400">
                <th className="py-2 text-start">{t("device.fingerprint")}</th>
                <th className="text-start">{t("device.lastSeen")}</th>
                <th className="text-start">{t("device.lastIP")}</th>
              </tr>
            </thead>
            <tbody>
              {devices.data?.devices.map((device) => (
                <tr key={device.id} className="border-b border-zinc-900">
                  <td className="py-1.5 font-mono text-xs">{device.fingerprint}</td>
                  <td className="font-mono text-xs text-zinc-500">
                    {formatTimestamp(device.last_seen_at)}
                  </td>
                  <td className="font-mono text-xs text-zinc-500">{device.last_ip}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
