import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../lib/api";
import { formatNumber, formatTimestamp, t } from "../i18n";
import { QuotaBar, formatTraffic, daysLeft } from "../components/QuotaBar";

interface Subject {
  id: number;
  name: string;
  enabled: boolean;
  expires_at: number | null;
  expired_at: number | null;
  created_at: number;
  note: string;
  quota_bytes: number | null;
  quota_used_bytes: number;
  frozen: boolean;
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

  const subscription = useQuery({
    queryKey: ["subject-sub", subjectId],
    queryFn: () =>
      api.get<{ url: string; clash_url: string; singbox_url: string; qr_url: string }>(
        `/api/v1/subjects/${subjectId}/subscription`,
      ),
  });

  const revokeSub = useMutation({
    mutationFn: () => api.post(`/api/v1/subjects/${subjectId}/subscription/revoke`, {}),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["subject-sub", subjectId] }),
  });

  // Which copy button just copied, so only that one flashes its label.
  const [copied, setCopied] = useState<string | null>(null);

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
                className="rounded bg-warning px-3 py-1 text-sm text-background hover:bg-warning/90"
              >
                {t("subject.freeze")}
              </button>
              <button
                type="button"
                onClick={() => disable.mutate()}
                className="rounded bg-destructive px-3 py-1 text-sm hover:bg-destructive/90"
              >
                {t("subject.disable")}
              </button>
            </>
          ) : (
            <>
              <button
                type="button"
                onClick={() => unfreeze.mutate()}
                className="rounded bg-primary px-3 py-1 text-sm hover:bg-primary/90"
              >
                {t("subject.unfreeze")}
              </button>
              <button
                type="button"
                onClick={() => enable.mutate()}
                className="rounded bg-success px-3 py-1 text-sm text-background hover:bg-success/90"
              >
                {t("subject.enable")}
              </button>
            </>
          )}
        </div>
      </div>

      <div className="rounded border border-border bg-card p-4">
        <h3 className="mb-3 text-sm font-semibold">{t("subject.details")}</h3>
        <dl className="grid grid-cols-2 gap-3 text-sm">
          <div>
            <dt className="text-muted-foreground">{t("subject.status")}</dt>
            <dd>{s.enabled ? t("subject.enabled") : t("subject.disabled")}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">{t("subject.created")}</dt>
            <dd className="font-mono text-xs">{formatTimestamp(s.created_at)}</dd>
          </div>
          {s.expires_at && (
            <div>
              <dt className="text-muted-foreground">{t("subject.expires")}</dt>
              <dd className="font-mono text-xs">{formatTimestamp(s.expires_at)}</dd>
            </div>
          )}
          {s.note && (
            <div className="col-span-2">
              <dt className="text-muted-foreground">{t("subject.note")}</dt>
              <dd>{s.note}</dd>
            </div>
          )}
          <div className="col-span-2">
            <dt className="text-muted-foreground">{t("subject.quota")}</dt>
            <dd>
              {s.quota_bytes ? (
                <div className="mt-1 flex flex-col gap-1">
                  <QuotaBar used={s.quota_used_bytes} total={s.quota_bytes} />
                  <span className="font-mono text-xs text-muted-foreground">
                    {formatTraffic(s.quota_used_bytes)} / {formatTraffic(s.quota_bytes)}
                  </span>
                </div>
              ) : (
                <span className="text-xs">{t("filters.unlimited")}</span>
              )}
            </dd>
          </div>
          {s.expires_at && (
            <div>
              <dt className="text-muted-foreground">{t("subject.remaining")}</dt>
              <dd className={`text-xs ${daysLeft(s.expires_at)! <= 3 ? "text-warning" : ""}`}>
                {formatNumber(daysLeft(s.expires_at)!)} {t("subject.daysLeft")}
              </dd>
            </div>
          )}
        </dl>
      </div>

      <div className="rounded border border-border bg-card p-4">
        <h3 className="mb-3 text-sm font-semibold">{t("subject.subscription")}</h3>
        {subscription.data?.url ? (
          <div className="flex flex-col gap-3 md:flex-row md:items-start">
            <div className="min-w-0 flex-1 space-y-2">
              <p className="break-all select-all font-mono text-xs">{subscription.data.url}</p>
              <div className="flex flex-wrap gap-2">
                <button
                  type="button"
                  className="rounded bg-secondary px-3 py-1 text-sm hover:bg-secondary/80"
                  onClick={async () => {
                    await navigator.clipboard.writeText(subscription.data!.url);
                    setCopied("main");
                    setTimeout(() => setCopied(null), 1500);
                  }}
                >
                  {copied === "main" ? t("common.saved") : t("common.copy")}
                </button>
                <button
                  type="button"
                  className="rounded bg-secondary px-3 py-1 text-sm hover:bg-secondary/80"
                  onClick={async () => {
                    await navigator.clipboard.writeText(subscription.data!.clash_url);
                    setCopied("clash");
                    setTimeout(() => setCopied(null), 1500);
                  }}
                >
                  {copied === "clash" ? t("common.saved") : t("subject.copyClash")}
                </button>
                <button
                  type="button"
                  className="rounded bg-secondary px-3 py-1 text-sm hover:bg-secondary/80"
                  onClick={async () => {
                    await navigator.clipboard.writeText(subscription.data!.singbox_url);
                    setCopied("singbox");
                    setTimeout(() => setCopied(null), 1500);
                  }}
                >
                  {copied === "singbox" ? t("common.saved") : t("subject.copySingbox")}
                </button>
                <a
                  href={subscription.data.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="rounded bg-primary px-3 py-1 text-sm text-primary-foreground hover:bg-primary/90"
                >
                  {t("subject.openSubPage")}
                </a>
                <button
                  type="button"
                  className="rounded bg-destructive px-3 py-1 text-sm hover:bg-destructive/90"
                  onClick={() => revokeSub.mutate()}
                >
                  {t("subject.revokeSub")}
                </button>
              </div>
            </div>
            {/* The QR inline rather than a link away: the operator holds a
                phone up to the screen, which a navigation to a PNG breaks. */}
            <img
              src={subscription.data.qr_url}
              alt={t("subject.qr")}
              width={132}
              height={132}
              className="h-[132px] w-[132px] rounded border border-border bg-background p-1"
            />
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">{t("loading")}</p>
        )}
      </div>

      <div className="rounded border border-border bg-card p-4">
        <h3 className="mb-3 text-sm font-semibold">{t("subject.credentials")}</h3>
        <div className="flex gap-2">
          <button
            type="button"
            onClick={() => revealCredential("uuid")}
            className="rounded bg-secondary px-3 py-1 text-sm hover:bg-secondary/80"
          >
            {t("subject.revealUUID")}
          </button>
          <button
            type="button"
            onClick={() => revealCredential("password")}
            className="rounded bg-secondary px-3 py-1 text-sm hover:bg-secondary/80"
          >
            {t("subject.revealPassword")}
          </button>
        </div>
        {showCredential && (
          <div className="mt-3 rounded bg-background p-3">
            <div className="text-xs text-muted-foreground">{showCredential}</div>
            <div className="font-mono text-sm">{credentialValue}</div>
          </div>
        )}
      </div>

      <div className="rounded border border-border bg-card p-4">
        <h3 className="mb-3 text-sm font-semibold">{t("subject.devices")}</h3>
        {devices.data?.devices.length === 0 ? (
          <div className="text-sm text-muted-foreground">{t("subject.noDevices")}</div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-border text-xs text-muted-foreground">
                <th className="py-2 text-start">{t("device.fingerprint")}</th>
                <th className="text-start">{t("device.lastSeen")}</th>
                <th className="text-start">{t("device.lastIP")}</th>
              </tr>
            </thead>
            <tbody>
              {devices.data?.devices.map((device) => (
                <tr key={device.id} className="border-b border-border">
                  <td className="py-1.5 font-mono text-xs">{device.fingerprint}</td>
                  <td className="font-mono text-xs text-muted-foreground">
                    {formatTimestamp(device.last_seen_at)}
                  </td>
                  <td className="font-mono text-xs text-muted-foreground">{device.last_ip}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
