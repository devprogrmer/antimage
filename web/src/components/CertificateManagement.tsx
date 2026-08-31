import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { can, useSession } from "../lib/session";
import { MutationError } from "../routes/Resellers";
import { formatTimestamp, t } from "../i18n";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { ConfirmDialog } from "./ConfirmDialog";

interface CACertificate {
  subject: string;
  issuer: string;
  not_before: number;
  not_after: number;
  serial_number: string;
  fingerprint: string;
  pem: string;
}

interface NodeCertificate {
  node_id: number;
  node_name: string;
  subject: string;
  not_after: number;
  serial_number: string;
  fingerprint: string;
  enrolled_at: number;
  // The backend also reports "unknown" for a node whose certificate the panel
  // did not record an expiry for; the client must not render an expiry date
  // for those rows.
  status: "valid" | "expiring_soon" | "expired" | "unknown";
  days_until_expiry: number;
}

interface CertificateStats {
  total: number;
  valid: number;
  expiring_soon: number;
  expired: number;
  // Nodes enrolled before the panel started recording certificate expiry.
  // The certificate works; nobody can say for how much longer.
  unknown: number;
}

/**
 * Certificate management UI: view CA, node certs, expiry warnings, revoke.
 * 
 * PKI oversight with CA certificate details, per-node certificate status,
 * expiry warnings, and revocation controls.
 */
export function CertificateManagement() {
  const session = useSession();
  const queryClient = useQueryClient();
  const [revokingCert, setRevokingCert] = useState<NodeCertificate | null>(null);
  const [showCA, setShowCA] = useState(false);

  const ca = useQuery({
    queryKey: ["ca"],
    queryFn: () => api.get<CACertificate>("/api/v1/ca"),
  });

  const certificates = useQuery({
    queryKey: ["certificates"],
    queryFn: () => api.get<{ certificates: NodeCertificate[]; stats: CertificateStats }>(
      "/api/v1/certificates"
    ),
    refetchInterval: 60000, // Check every minute for expiry changes
  });

  const revoke = useMutation({
    mutationFn: (nodeId: number) =>
      api.post(`/api/v1/nodes/${nodeId}/certificate/revoke`, {}),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["certificates"] });
      setRevokingCert(null);
    },
  });

  const mayWrite = can(session.data, "node:write");
  const stats = certificates.data?.stats;
  const certs = certificates.data?.certificates ?? [];

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">{t("certificates.title")}</h3>
          <p className="text-xs text-muted-foreground">{t("certificates.description")}</p>
        </div>
        {stats && (
          <div className="flex flex-wrap gap-2 text-xs">
            <Badge variant="outline">
              {t("certificates.total")}: {stats.total}
            </Badge>
            <Badge variant="success">
              {t("certificates.valid")}: {stats.valid}
            </Badge>
            <Badge variant="warning">
              {t("certificates.expiringSoon")}: {stats.expiring_soon}
            </Badge>
            {stats.expired > 0 && (
              <Badge variant="destructive">
                {t("certificates.expired")}: {stats.expired}
              </Badge>
            )}
          </div>
        )}
      </header>

      <MutationError error={certificates.error || revoke.error} />

      {/* CA Certificate Section */}
      <section className="rounded-lg border border-border bg-card p-4">
        <div className="flex items-center justify-between">
          <h4 className="text-sm font-semibold">{t("certificates.ca")}</h4>
          <Button
            size="sm"
            variant="outline"
            onClick={() => setShowCA(!showCA)}
          >
            {showCA ? t("common.hide") : t("common.show")}
          </Button>
        </div>

        {ca.data && showCA && (
          <div className="mt-3 space-y-3">
            <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-xs">
              <dt className="text-muted-foreground">{t("certificates.subject")}:</dt>
              <dd className="truncate font-mono" title={ca.data.subject}>
                {ca.data.subject}
              </dd>
              <dt className="text-muted-foreground">{t("certificates.validFrom")}:</dt>
              <dd className="font-mono">{formatTimestamp(ca.data.not_before)}</dd>
              <dt className="text-muted-foreground">{t("certificates.validUntil")}:</dt>
              <dd className="font-mono">{formatTimestamp(ca.data.not_after)}</dd>
              <dt className="text-muted-foreground">{t("certificates.fingerprint")}:</dt>
              <dd className="truncate font-mono text-[10px]" title={ca.data.fingerprint}>
                {ca.data.fingerprint}
              </dd>
            </dl>

            <details className="rounded border border-border bg-background p-2">
              <summary className="cursor-pointer text-xs font-medium">
                {t("certificates.viewPEM")}
              </summary>
              <pre className="mt-2 overflow-x-auto font-mono text-[9px] text-muted-foreground">
                {ca.data.pem}
              </pre>
            </details>
          </div>
        )}
      </section>

      {/* Node Certificates Section */}
      <section>
        <h4 className="mb-2 text-sm font-semibold">{t("certificates.nodeCerts")}</h4>

        {certs.length === 0 && (
          <p className="py-8 text-center text-xs text-muted-foreground">
            {t("certificates.noCerts")}
          </p>
        )}

        <div className="space-y-2">
          {certs.map((cert) => (
            <CertificateCard
              key={cert.node_id}
              certificate={cert}
              mayWrite={mayWrite}
              onRevoke={() => setRevokingCert(cert)}
            />
          ))}
        </div>
      </section>

      {/* Revoke Confirmation Dialog */}
      <ConfirmDialog
        open={revokingCert !== null}
        onOpenChange={(open) => !open && setRevokingCert(null)}
        title={t("certificates.confirmRevoke")}
        description={t("certificates.revokeWarning")}
        confirmLabel={t("certificates.revoke")}
        pending={revoke.isPending}
        onConfirm={() => revokingCert && revoke.mutate(revokingCert.node_id)}
      />
    </div>
  );
}

interface CertificateCardProps {
  certificate: NodeCertificate;
  mayWrite: boolean;
  onRevoke: () => void;
}

function CertificateCard({ certificate, mayWrite, onRevoke }: CertificateCardProps) {
  const [expanded, setExpanded] = useState(false);

  // Status badge variant based on certificate status. "unknown" is not fatal
  // and not fine either -- the certificate works and the panel just cannot
  // say how much longer -- so it reads as outline, not warning or success.
  const statusVariant: "success" | "warning" | "destructive" | "outline" =
    certificate.status === "valid"
      ? "success"
      : certificate.status === "expiring_soon"
      ? "warning"
      : certificate.status === "expired"
      ? "destructive"
      : "outline";

  // Rendered via the i18n table -- an English literal here is invisible to
  // the fa/ru/zh-CN/ar locales the panel ships.
  const expiryText =
    certificate.status === "unknown"
      ? t("certificates.expiryUnknown")
      : certificate.days_until_expiry < 0
      ? t("certificates.expiredDaysAgo", { days: String(Math.abs(certificate.days_until_expiry)) })
      : certificate.days_until_expiry === 0
      ? t("certificates.expiresToday")
      : certificate.days_until_expiry === 1
      ? t("certificates.expiresTomorrow")
      : t("certificates.expiresInDays", { days: String(certificate.days_until_expiry) });

  return (
    <div
      className={`rounded-lg border p-3 ${
        certificate.status === "expired"
          ? "border-destructive/50 bg-destructive/5"
          : certificate.status === "expiring_soon"
          ? "border-warning/50 bg-warning/5"
          : "border-border bg-card"
      }`}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="flex-1 space-y-2">
          <div className="flex items-center gap-2">
            <h5 className="font-mono text-sm font-medium">{certificate.node_name}</h5>
            <Badge variant={statusVariant} className="text-[10px]">
              {certificate.status.replace("_", " ")}
            </Badge>
          </div>

          <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs">
            <dt className="text-muted-foreground">{t("certificates.validUntil")}:</dt>
            {/* Rendering formatTimestamp(0) would print the Unix epoch as a
                real date, which reads as a certificate that expired in 1970. */}
            <dd className="font-mono">
              {certificate.status === "unknown" ? "—" : formatTimestamp(certificate.not_after)}
            </dd>
            <dt className="text-muted-foreground">{t("certificates.expiry")}:</dt>
            <dd
              className={`font-medium ${
                certificate.status === "expired"
                  ? "text-destructive"
                  : certificate.status === "expiring_soon"
                  ? "text-warning"
                  : "text-success"
              }`}
            >
              {expiryText}
            </dd>
          </dl>

          {expanded && (
            <dl className="grid grid-cols-2 gap-x-4 gap-y-1 border-t border-border pt-2 text-xs">
              <dt className="text-muted-foreground">{t("certificates.subject")}:</dt>
              <dd className="truncate font-mono text-[10px]" title={certificate.subject}>
                {certificate.subject}
              </dd>
              <dt className="text-muted-foreground">{t("certificates.serial")}:</dt>
              <dd className="font-mono text-[10px]">{certificate.serial_number}</dd>
              <dt className="text-muted-foreground">{t("certificates.fingerprint")}:</dt>
              <dd className="truncate font-mono text-[10px]" title={certificate.fingerprint}>
                {certificate.fingerprint.slice(0, 24)}...
              </dd>
            </dl>
          )}

          <Button
            size="sm"
            variant="ghost"
            className="h-auto p-0 text-xs"
            onClick={() => setExpanded(!expanded)}
          >
            {expanded ? t("common.showLess") : t("common.showMore")}
          </Button>
        </div>

        {/* Revocation is still offered when the certificate has expired: the
            fingerprint is what mTLS accepts, and it stays in the allow-list
            until this button clears it. An expired certificate whose row is
            never revoked is a stale allow-list entry, not a locked-out node. */}
        {mayWrite && (
          <Button
            size="sm"
            variant="destructive"
            onClick={onRevoke}
          >
            {t("certificates.revoke")}
          </Button>
        )}
      </div>
    </div>
  );
}
