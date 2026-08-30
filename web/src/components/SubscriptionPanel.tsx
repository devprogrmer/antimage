import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { MutationError } from "../routes/Resellers";
import { ConfirmDialog } from "./ConfirmDialog";
import { Button } from "./ui/button";
import { SubjectGroupPicker } from "./SubscriptionGroups";
import { formatNumber, formatTimestamp, t } from "../i18n";

/**
 * Everything a subject can connect with, per inbound, in the format that
 * inbound's protocol actually uses.
 *
 * Deliberately not one "subscription" string. A VLESS inbound produces a link,
 * a WireGuard inbound produces a file that has no link form, and an L2TP
 * inbound produces neither -- the user types a server address into their
 * operating system. The panel used to collapse all three into a V2Ray-style
 * subscription, which meant a WireGuard tunnel was handed out as a vless://
 * URI that no client could use.
 */

type Delivery = "uri" | "file" | "manual";

interface ManualConfig {
  server: string;
  port: number;
  username: string;
  password: string;
  psk?: string;
}

interface ClientConfig {
  service_id: number;
  node_id: number;
  node_name: string;
  adapter_kind: string;
  protocol: string;
  delivery: Delivery;
  uri?: string;
  file_name?: string;
  file_body?: string;
  manual?: ManualConfig;
  note?: string;
}

interface SkippedInbound {
  service_id: number;
  node_name: string;
  adapter_kind: string;
  reason: string;
}

interface ConfigsResponse {
  group_filter: string[];
  subject_id: number;
  name: string;
  subscription_url?: string;
  status: string;
  expires_at: number | null;
  quota_bytes: number | null;
  quota_used_bytes: number;
  configs: ClientConfig[];
  skipped: SkippedInbound[];
}

/** Translation keys, typed so t() still checks them against en.json. A plain
 *  Record<string,string> would widen the lookup to string and defeat that. */
type TKey = Parameters<typeof t>[0];

/** Reasons the server names, mapped to something an operator can act on. */
const NOTE_KEY: Record<string, TKey> = {
  notInAggregatedFormats: "sub.noteNotAggregatable",
  noServerPublicKey: "sub.noteNoServerKey",
  openvpnNeedsCA: "sub.noteNeedsCA",
};

const SKIP_KEY: Record<string, TKey> = {
  inboundDisabled: "sub.skipDisabled",
  paramsUnreadable: "sub.skipUnreadable",
  excludedByGroup: "sub.skipExcludedByGroup",
};

export function SubscriptionPanel({ subjectId }: { subjectId: number }) {
  const queryClient = useQueryClient();
  const [revoking, setRevoking] = useState(false);

  const configs = useQuery({
    queryKey: ["subject", subjectId, "configs"],
    queryFn: () => api.get<ConfigsResponse>(`/api/v1/subjects/${subjectId}/configs`),
  });

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ["subject", subjectId, "configs"] });

  // One button for issue and regenerate: from the operator's side both mean
  // "give me a link that works, and stop the previous one".
  const issue = useMutation({
    mutationFn: () => api.post(`/api/v1/subjects/${subjectId}/subscription`, {}),
    onSuccess: invalidate,
  });

  const revoke = useMutation({
    mutationFn: () => api.del(`/api/v1/subjects/${subjectId}/subscription`),
    onSuccess: () => {
      setRevoking(false);
      invalidate();
    },
  });

  const data = configs.data;

  return (
    <div className="rounded border border-border bg-card p-4">
      <h3 className="mb-3 text-sm font-semibold">{t("sub.title")}</h3>
      <MutationError error={configs.error} />
      <MutationError error={issue.error} />
      <MutationError error={revoke.error} />

      {data && (
        <>
          {/* Whether the thing about to be handed over will actually work. A
              link for a frozen or expired subject is a support ticket. */}
          {data.status !== "active" && (
            <p className="mb-3 text-xs text-warning" role="status">
              {t("sub.notActive", { status: data.status })}
            </p>
          )}

          <dl className="mb-4 flex flex-wrap gap-x-6 gap-y-1 text-xs">
            <div className="flex gap-2">
              <dt className="text-muted-foreground">{t("subject.expires")}</dt>
              <dd className="font-mono">
                {data.expires_at === null
                  ? t("reseller.unlimited")
                  : formatTimestamp(data.expires_at)}
              </dd>
            </div>
            <div className="flex gap-2">
              <dt className="text-muted-foreground">{t("reseller.quotaBytes")}</dt>
              <dd className="font-mono">
                {data.quota_bytes === null
                  ? t("reseller.unlimited")
                  : `${formatNumber(data.quota_used_bytes)} / ${formatNumber(data.quota_bytes)}`}
              </dd>
            </div>
          </dl>

          <div className="mb-4 rounded border border-border bg-background p-3">
            {/* The tier the customer is on decides what the link below
                contains, so it belongs beside it rather than on another
                screen. */}
            <SubjectGroupPicker subjectId={subjectId} current={data.group_filter ?? []} />
            <p className="mb-2 text-xs font-medium">{t("sub.aggregated")}</p>
            {data.subscription_url ? (
              <>
                <CopyRow value={absoluteURL(data.subscription_url)} label={t("sub.url")} />
                <QRButton text={absoluteURL(data.subscription_url)} />
              </>
            ) : (
              <p className="text-xs text-muted-foreground">{t("sub.noLinkYet")}</p>
            )}
            <div className="mt-2 flex flex-wrap gap-2">
              <Button size="sm" onClick={() => issue.mutate()} disabled={issue.isPending}>
                {data.subscription_url ? t("sub.regenerate") : t("sub.issue")}
              </Button>
              {data.subscription_url && (
                <Button size="sm" variant="outline" onClick={() => setRevoking(true)}>
                  {t("sub.revoke")}
                </Button>
              )}
            </div>
            {/* Said plainly, because it is the difference between the
                aggregated link and the per-protocol list below. */}
            <p className="mt-2 text-xs text-muted-foreground">{t("sub.aggregatedNote")}</p>
          </div>

          {data.configs.length === 0 && data.skipped.length === 0 && (
            <p className="text-sm text-muted-foreground">{t("sub.noInbounds")}</p>
          )}

          <div className="space-y-3">
            {data.configs.map((c) => (
              <ConfigCard key={c.service_id} config={c} />
            ))}
          </div>

          {data.skipped.length > 0 && (
            <div className="mt-4">
              <p className="mb-1 text-xs font-medium text-warning">{t("sub.skippedTitle")}</p>
              <ul className="space-y-1 text-xs text-muted-foreground">
                {data.skipped.map((s) => (
                  <li key={s.service_id}>
                    <span className="font-mono">{s.adapter_kind}</span>
                    {" · "}
                    <span className="font-mono">{s.node_name}</span>
                    {" — "}
                    {SKIP_KEY[s.reason] ? t(SKIP_KEY[s.reason]) : s.reason}
                  </li>
                ))}
              </ul>
            </div>
          )}
        </>
      )}

      <ConfirmDialog
        open={revoking}
        onOpenChange={setRevoking}
        title={t("sub.confirmRevoke")}
        description={t("sub.revokeWarning")}
        confirmLabel={t("sub.revoke")}
        pending={revoke.isPending}
        onConfirm={() => revoke.mutate()}
      />
    </div>
  );
}

/** A relative /subscribe/... becomes something a customer can actually paste. */
function absoluteURL(path: string): string {
  if (path.startsWith("http")) return path;
  return `${window.location.origin}/api/v1${path}`;
}

function ConfigCard({ config }: { config: ClientConfig }) {
  return (
    <div className="rounded border border-border bg-background p-3">
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <span className="font-mono text-sm">{config.protocol}</span>
        <span className="text-xs text-muted-foreground">{config.node_name}</span>
        <span className="ms-auto rounded bg-secondary px-2 py-0.5 text-xs">
          {t(`sub.delivery.${config.delivery}`)}
        </span>
      </div>

      {config.note && NOTE_KEY[config.note] && (
        <p className="mb-2 text-xs text-warning" role="status">
          {t(NOTE_KEY[config.note])}
        </p>
      )}

      {config.delivery === "uri" && config.uri && (
        <>
          <CopyRow value={config.uri} label={t("sub.link")} />
          <QRButton text={config.uri} />
        </>
      )}

      {config.delivery === "file" && config.file_body && (
        <>
          <pre className="max-h-48 overflow-auto rounded border border-border bg-card p-2 font-mono text-[11px]">
            {config.file_body}
          </pre>
          <div className="mt-2 flex flex-wrap gap-2">
            <CopyButton value={config.file_body} />
            <Button
              size="sm"
              variant="outline"
              onClick={() => download(config.file_name ?? "config.txt", config.file_body!)}
            >
              {t("sub.download")}
            </Button>
          </div>
          {/* No QR: a WireGuard profile or an .ovpn is far past what a QR code
              can hold, and offering one that scans as garbage is worse than
              not offering it. */}
          <p className="mt-1 text-xs text-muted-foreground">{t("sub.noQrForFiles")}</p>
        </>
      )}

      {config.delivery === "manual" && config.manual && (
        <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs">
          <Field label={t("bootstrap.host")} value={config.manual.server} />
          <Field label={t("bootstrap.port")} value={String(config.manual.port)} />
          <Field label={t("bootstrap.username")} value={config.manual.username} />
          <Field label={t("bootstrap.password")} value={config.manual.password} secret />
          {config.manual.psk && (
            <Field label={t("sub.psk")} value={config.manual.psk} secret />
          )}
        </dl>
      )}
    </div>
  );
}

function Field({ label, value, secret }: { label: string; value: string; secret?: boolean }) {
  return (
    <div className="flex gap-2">
      <dt className="text-muted-foreground">{label}</dt>
      {/* select-all so a value can be copied without a button per row; secrets
          are not masked because the operator opened this panel to read them,
          and a mask they must click through is friction without a benefit. */}
      <dd className={secret ? "select-all font-mono" : "font-mono"}>{value}</dd>
    </div>
  );
}

function CopyRow({ value, label }: { value: string; label: string }) {
  return (
    <div className="mb-2">
      <p className="text-xs text-muted-foreground">{label}</p>
      <div className="flex items-center gap-2">
        <code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap rounded border border-border bg-card px-2 py-1 font-mono text-xs">
          {value}
        </code>
        <CopyButton value={value} />
      </div>
    </div>
  );
}

function CopyButton({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      size="sm"
      variant="outline"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          // A denied clipboard permission is not an error worth a dialog: the
          // value is on screen and selectable either way.
          setCopied(false);
        }
      }}
    >
      {copied ? t("sub.copied") : t("sub.copy")}
    </Button>
  );
}

/**
 * Renders the QR through the panel rather than a client-side library.
 *
 * The text is a credential-bearing URI, so the request is authenticated and the
 * response is no-store. Fetched as a blob rather than pointed at with an <img
 * src> because the encoder is a POST -- the URI is long, contains characters a
 * query string mangles, and must not land in an access log.
 */
function QRButton({ text }: { text: string }) {
  const [src, setSrc] = useState<string | null>(null);

  const load = useMutation({
    mutationFn: async () => {
      const res = await fetch("/api/v1/qr", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text }),
        credentials: "same-origin",
      });
      if (!res.ok) {
        const body = (await res.json().catch(() => ({}))) as {
          error?: { message: string };
        };
        throw new Error(body.error?.message ?? "could not generate QR code");
      }
      return URL.createObjectURL(await res.blob());
    },
    onSuccess: setSrc,
  });

  return (
    <div className="mt-1">
      {src === null ? (
        <Button size="sm" variant="outline" onClick={() => load.mutate()} disabled={load.isPending}>
          {t("sub.showQr")}
        </Button>
      ) : (
        <img src={src} alt={t("sub.qrAlt")} className="size-40 rounded border border-border bg-card" />
      )}
      <MutationError error={load.error} />
    </div>
  );
}

function download(name: string, body: string) {
  const url = URL.createObjectURL(new Blob([body], { type: "text/plain" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}
