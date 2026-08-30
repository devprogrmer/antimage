import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../lib/api";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { EnforcementStatus } from "../components/EnforcementStatus";
import { SubscriptionPanel } from "../components/SubscriptionPanel";
import { ActivityTimeline } from "../components/ActivityTimeline";
import { ActiveConnections } from "../components/ActiveConnections";
import { MutationError } from "./Resellers";
import { formatNumber, formatTimestamp, t } from "../i18n";

interface Subject {
  id: number;
  name: string;
  enabled: boolean;
  expires_at: number | null;
  expired_at: number | null;
  frozen_at: number | null;
  frozen_reason?: string;
  status: "active" | "on_hold" | "expired" | "disabled" | "frozen";
  on_hold_seconds: number | null;
  status_changed_at: number | null;
  created_at: number;
  note: string;
}

const STATUS_LABEL = {
  active: "subject.active",
  on_hold: "subject.onHold",
  expired: "subject.expired",
  disabled: "subject.disabled",
  frozen: "subject.frozen",
} as const;

// Matches the backend's DeviceResponse in devices_api.go. The list route
// returns a BARE array, not `{ devices: [...] }` -- the previous typing was
// wrong and left `data.devices` undefined at runtime, so the whole devices
// section crashed after the first fetch.
interface Device {
  id: number;
  subject_id: number;
  hwid: string;
  name: string;
  first_seen_at: number;
  last_seen_at: number;
  last_ip: string;
  user_agent: string;
  is_active: boolean;
  revoked_at?: number | null;
  revoked_reason?: string;
}

export function SubjectDetail({ subjectId }: { subjectId: number }) {
  const queryClient = useQueryClient();
  const [showCredential, setShowCredential] = useState<string | null>(null);
  const [credentialValue, setCredentialValue] = useState<string>("");
  const [pendingRotate, setPendingRotate] = useState<string | null>(null);

  const subject = useQuery({
    queryKey: ["subject", subjectId],
    queryFn: () => api.get<Subject>(`/api/v1/subjects/${subjectId}`),
  });

  const devices = useQuery({
    queryKey: ["devices", subjectId],
    queryFn: () => api.get<Device[]>(`/api/v1/subjects/${subjectId}/devices`),
  });

  const revokeDevice = useMutation({
    mutationFn: (deviceId: number) => api.post(`/api/v1/devices/${deviceId}/revoke`, {
      reason: "revoked from panel",
    }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["devices", subjectId] });
    },
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

  // Rotation replaces the credential and republishes every node serving this
  // subject, so every client configured with the old value stops working. That
  // is the point of it -- and the reason it asks first.
  const rotate = useMutation({
    mutationFn: (kind: string) =>
      api.post<{ kind: string; value: string }>(
        `/api/v1/subjects/${subjectId}/credentials/${kind}/rotate`,
        {},
      ),
    onSuccess: (resp) => {
      // Shown immediately: the operator rotated it in order to hand the new
      // value to somebody, and making them click Reveal afterwards is a second
      // disclosure of the same secret for no benefit.
      setCredentialValue(resp.value);
      setShowCredential(resp.kind);
      setPendingRotate(null);
      queryClient.invalidateQueries({ queryKey: ["subject", subjectId] });
    },
  });

  if (!subject.data) {
    return <div>{t("loading")}</div>;
  }

  const s = subject.data;

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold font-mono">{s.name}</h2>
        {/* Frozen and enabled are ORTHOGONAL, and pairing them behind one
            ternary is what stranded the operator. Freezing leaves `enabled`
            true, so the old `s.enabled ?` branch kept showing Freeze after a
            subject was already frozen and never offered Unfreeze -- a
            revocation that could not be undone from the screen that made it.
            Each pair now toggles on the field it actually governs. */}
        <div className="flex gap-2">
          {s.frozen_at == null ? (
            <button
              type="button"
              onClick={() => freeze.mutate()}
              className="rounded bg-warning px-3 py-1 text-sm text-background hover:bg-warning/90"
            >
              {t("subject.freeze")}
            </button>
          ) : (
            <button
              type="button"
              onClick={() => unfreeze.mutate()}
              className="rounded bg-primary px-3 py-1 text-sm hover:bg-primary/90"
            >
              {t("subject.unfreeze")}
            </button>
          )}
          {s.enabled ? (
            <button
              type="button"
              onClick={() => disable.mutate()}
              className="rounded bg-destructive px-3 py-1 text-sm hover:bg-destructive/90"
            >
              {t("subject.disable")}
            </button>
          ) : (
            <button
              type="button"
              onClick={() => enable.mutate()}
              className="rounded bg-success px-3 py-1 text-sm text-background hover:bg-success/90"
            >
              {t("subject.enable")}
            </button>
          )}
        </div>
      </div>

      <div className="rounded border border-border bg-card p-4">
        <h3 className="mb-3 text-sm font-semibold">{t("subject.details")}</h3>
        <dl className="grid grid-cols-2 gap-3 text-sm">
          <div>
            <dt className="text-muted-foreground">{t("subject.status")}</dt>
            {/* One word, derived on the server. This screen and the list badge
                each used to work it out from the raw columns and disagreed
                about which of frozen and disabled took precedence. */}
            {/* Falls back to the raw value rather than blank, for the same
                reason t() returns the key when a translation is missing: a
                visible oddity beats an empty cell nobody can diagnose. */}
            <dd>{STATUS_LABEL[s.status] ? t(STATUS_LABEL[s.status]) : s.status}</dd>
          </div>
          {s.status_changed_at !== null && (
            <div>
              <dt className="text-muted-foreground">{t("subject.statusChanged")}</dt>
              <dd className="font-mono text-xs">{formatTimestamp(s.status_changed_at)}</dd>
            </div>
          )}
          {/* The plan is sold but not started: say how long it will run, since
              there is no expiry date to show yet. */}
          {s.on_hold_seconds !== null && (
            <div>
              <dt className="text-muted-foreground">{t("subject.validityDays")}</dt>
              <dd className="font-mono text-xs">
                {t("subject.daysFromFirstUse", {
                  days: formatNumber(Math.round(s.on_hold_seconds / 86400)),
                })}
              </dd>
            </div>
          )}
          {s.frozen_at != null && (
            <div>
              <dt className="text-muted-foreground">{t("subject.frozenReason")}</dt>
              <dd className="font-mono text-xs">
                {s.frozen_reason || t("subject.frozenNoReason")}
              </dd>
            </div>
          )}
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
        </dl>
      </div>

      {/* Above credentials and devices: when a customer reports that service
          stopped, this is the card that answers it. */}
      <EnforcementStatus subjectId={subjectId} />

      {/* The operator hands this to the customer, so it sits with the rest of
          what defines their access. */}
      <SubscriptionPanel subjectId={subjectId} />

      <div className="rounded border border-border bg-card p-4">
        <h3 className="mb-3 text-sm font-semibold">{t("subject.credentials")}</h3>
        <div className="flex flex-wrap gap-2">
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
          {/* Rotation had a route and no button, so the only way to replace a
              leaked credential was to delete the customer and recreate them. */}
          <button
            type="button"
            onClick={() => setPendingRotate("uuid")}
            className="rounded bg-warning px-3 py-1 text-sm text-background hover:bg-warning/90"
          >
            {t("subject.rotateUUID")}
          </button>
          <button
            type="button"
            onClick={() => setPendingRotate("password")}
            className="rounded bg-warning px-3 py-1 text-sm text-background hover:bg-warning/90"
          >
            {t("subject.rotatePassword")}
          </button>
        </div>
        <MutationError error={rotate.error} />
        {showCredential && (
          <div className="mt-3 rounded bg-background p-3">
            <div className="text-xs text-muted-foreground">{showCredential}</div>
            <div className="select-all font-mono text-sm">{credentialValue}</div>
          </div>
        )}

        <ConfirmDialog
          open={pendingRotate !== null}
          onOpenChange={(open) => !open && setPendingRotate(null)}
          title={t("subject.confirmRotate")}
          description={t("subject.rotateWarning")}
          confirmLabel={t("subject.rotate")}
          pending={rotate.isPending}
          onConfirm={() => pendingRotate && rotate.mutate(pendingRotate)}
        />
      </div>

      <div className="rounded border border-border bg-card p-4">
        <h3 className="mb-3 text-sm font-semibold">{t("subject.devices")}</h3>
        {(devices.data ?? []).length === 0 ? (
          <div className="text-sm text-muted-foreground">{t("subject.noDevices")}</div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-border text-xs text-muted-foreground">
                <th className="py-2 text-start">{t("device.fingerprint")}</th>
                <th className="text-start">{t("device.lastSeen")}</th>
                <th className="text-start">{t("device.lastIP")}</th>
                <th className="text-end">{t("device.actions")}</th>
              </tr>
            </thead>
            <tbody>
              {(devices.data ?? []).map((device) => (
                <tr key={device.id} className="border-b border-border">
                  <td className="py-1.5 font-mono text-xs">
                    {device.hwid}
                    {!device.is_active && (
                      <span className="ms-2 rounded bg-destructive/10 px-1.5 py-0.5 text-[10px] text-destructive">
                        {t("device.revoked")}
                      </span>
                    )}
                  </td>
                  <td className="font-mono text-xs text-muted-foreground">
                    {formatTimestamp(device.last_seen_at)}
                  </td>
                  <td className="font-mono text-xs text-muted-foreground">{device.last_ip}</td>
                  <td className="text-end">
                    {/* Revoke is offered only for still-active devices. The
                        backend records revoked_at and refuses re-revoke; a
                        button that reads "revoke" against a revoked device
                        just repeats an operator's own conclusion back to
                        them. */}
                    {device.is_active && (
                      <button
                        type="button"
                        disabled={revokeDevice.isPending}
                        onClick={() => revokeDevice.mutate(device.id)}
                        className="rounded bg-destructive/10 px-2 py-1 text-xs text-destructive hover:bg-destructive/20"
                      >
                        {t("device.revoke")}
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <MutationError error={revokeDevice.error} />
      </div>

      {/* Live view of open transports. Complements the timeline: this
          answers "is anybody connected right now?" and the timeline
          answers "what happened yesterday?". Both routes existed and
          neither was reachable. */}
      <ActiveConnections subjectId={subjectId} />

      {/* Subject timeline: audit rows (create/disable/quota/etc) merged with
          the connection audit log's connect/disconnect/reject/kick trace. */}
      <ActivityTimeline subjectId={subjectId} />
    </div>
  );
}
