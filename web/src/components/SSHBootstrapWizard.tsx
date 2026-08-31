import { useMutation } from "@tanstack/react-query";
import { useState } from "react";

import { api, ApiError } from "../lib/api";
import { can, useSession } from "../lib/session";
import { MutationError } from "../routes/Resellers";
import { t } from "../i18n";
import { Button } from "./ui/button";

/**
 * SSH bootstrap wizard: guided node onboarding, matching the real backend.
 *
 * The earlier version of this component polled `/bootstrap-ssh/status/{jobId}`
 * for a job that never existed -- the backend has no job store, no async
 * bootstrap, and no per-step progress. The real flow is two synchronous POSTs
 * against the same route:
 *
 *   1. POST with no host_key_fingerprint. Panel opens SSH, reads the host's
 *      key, hands the fingerprint back with `confirm_required: true`. Nothing
 *      is executed. An admin who does not recognise the fingerprint stops.
 *
 *   2. POST again with the confirmed fingerprint. Panel opens SSH, mints a
 *      one-time enrolment token, runs `curl | sudo bash` on the host, and
 *      returns the install script's combined output on success (200) or the
 *      output plus stderr on failure (502).
 *
 * The wizard is written to that shape. Progress is what the install script
 * itself printed, streamed only at the granularity the script prints at,
 * because that is what is actually available.
 */

interface SSHConfig {
  host: string;
  port: number;
  user: string;
  private_key_pem: string;
  passphrase?: string;
}

interface PromptResponse {
  host_key_fingerprint: string;
  confirm_required: true;
}

interface BootstrapSuccess {
  output: string;
}

interface SSHBootstrapWizardProps {
  nodeId: number;
  onComplete?: () => void;
}

export function SSHBootstrapWizard({ nodeId, onComplete }: SSHBootstrapWizardProps) {
  const session = useSession();
  const [config, setConfig] = useState<SSHConfig>({
    host: "",
    port: 22,
    user: "root",
    private_key_pem: "",
    passphrase: "",
  });
  const [fingerprint, setFingerprint] = useState<string | null>(null);
  const [output, setOutput] = useState<string | null>(null);
  const [failureOutput, setFailureOutput] = useState<string | null>(null);

  const mayWrite = can(session.data, "node:write");

  // Phase one: read the host key. Never runs anything -- the response says
  // `confirm_required` and the admin decides. The private key is sent
  // because SSH cannot read the host key without at least trying to open
  // the transport, and the panel does not keep it.
  const prompt = useMutation({
    mutationFn: () =>
      api.post<PromptResponse>(`/api/v1/nodes/${nodeId}/bootstrap-ssh`, {
        ...config,
        passphrase: config.passphrase || undefined,
      }),
    onSuccess: (data) => {
      setFingerprint(data.host_key_fingerprint);
      setFailureOutput(null);
    },
  });

  // Phase two: run with the confirmed fingerprint. Returns the install
  // script's combined output on success, or 502 with output+stderr.
  const run = useMutation({
    mutationFn: () =>
      api.post<BootstrapSuccess>(`/api/v1/nodes/${nodeId}/bootstrap-ssh`, {
        ...config,
        passphrase: config.passphrase || undefined,
        host_key_fingerprint: fingerprint,
      }),
    onSuccess: (data) => {
      setOutput(data.output);
      setFailureOutput(null);
    },
    onError: (err: Error) => {
      // The 502 body carries the install script's output alongside the
      // error envelope. An operator fixing this needs to read what the script
      // said before it failed, not just the error line.
      if (err instanceof ApiError && err.body && typeof err.body === "object") {
        const output = (err.body as { output?: unknown }).output;
        if (typeof output === "string") setFailureOutput(output);
      }
    },
  });

  if (!mayWrite) {
    return (
      <div className="rounded-lg border border-border bg-card p-4 text-center text-sm text-muted-foreground">
        {t("deploy.noPermission")}
      </div>
    );
  }

  if (output !== null) {
    return (
      <div className="space-y-3 rounded-lg border border-success bg-success/10 p-4">
        <h3 className="text-sm font-semibold">{t("bootstrap.complete")}</h3>
        <p className="text-xs text-muted-foreground">{t("bootstrap.completeDesc")}</p>
        <pre className="max-h-64 overflow-auto rounded border border-border bg-background p-2 font-mono text-[10px]">
          {output}
        </pre>
        <Button size="sm" onClick={onComplete}>
          {t("common.close")}
        </Button>
      </div>
    );
  }

  return (
    <div className="space-y-4 rounded-lg border border-border bg-card p-4">
      <header>
        <h3 className="text-sm font-semibold">{t("bootstrap.title")}</h3>
        <p className="mt-1 text-xs text-muted-foreground">{t("bootstrap.description")}</p>
      </header>

      {fingerprint === null ? (
        <ConfigStep
          config={config}
          onChange={setConfig}
          onSubmit={() => prompt.mutate()}
          pending={prompt.isPending}
          error={prompt.error}
        />
      ) : (
        <ConfirmStep
          config={config}
          fingerprint={fingerprint}
          onBack={() => {
            setFingerprint(null);
            prompt.reset();
          }}
          onConfirm={() => run.mutate()}
          pending={run.isPending}
          error={run.error}
          failureOutput={failureOutput}
        />
      )}
    </div>
  );
}

interface ConfigStepProps {
  config: SSHConfig;
  onChange: (c: SSHConfig) => void;
  onSubmit: () => void;
  pending: boolean;
  error: Error | null;
}

function ConfigStep({ config, onChange, onSubmit, pending, error }: ConfigStepProps) {
  const update = <K extends keyof SSHConfig>(field: K, value: SSHConfig[K]) => {
    onChange({ ...config, [field]: value });
  };

  return (
    <div className="space-y-3">
      <div>
        <label className="block text-xs font-medium text-muted-foreground mb-1" htmlFor="ssh-host">
          {t("bootstrap.host")}
        </label>
        <input
          id="ssh-host"
          type="text"
          value={config.host}
          onChange={(e) => update("host", e.target.value)}
          placeholder="192.168.1.100"
          className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
        />
      </div>

      <div className="grid grid-cols-2 gap-3">
        <div>
          <label className="block text-xs font-medium text-muted-foreground mb-1" htmlFor="ssh-port">
            {t("bootstrap.port")}
          </label>
          <input
            id="ssh-port"
            type="number"
            value={config.port}
            onChange={(e) => update("port", parseInt(e.target.value, 10) || 22)}
            className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
          />
        </div>
        <div>
          <label className="block text-xs font-medium text-muted-foreground mb-1" htmlFor="ssh-user">
            {t("bootstrap.username")}
          </label>
          <input
            id="ssh-user"
            type="text"
            value={config.user}
            onChange={(e) => update("user", e.target.value)}
            className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
          />
        </div>
      </div>

      <div>
        <label className="block text-xs font-medium text-muted-foreground mb-1" htmlFor="ssh-key">
          {t("bootstrap.privateKey")}
        </label>
        {/* PEM paste rather than a path on disk. The panel does not read the
            filesystem the admin's browser sees; the key has to travel here to
            be usable. It is held in memory only for the request; see
            bootstrap.go and its defer creds.Zero(). */}
        <textarea
          id="ssh-key"
          value={config.private_key_pem}
          onChange={(e) => update("private_key_pem", e.target.value)}
          placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
          rows={6}
          className="w-full rounded-md border border-input bg-background px-3 py-2 font-mono text-[11px]"
        />
        <p className="mt-1 text-xs text-muted-foreground">{t("bootstrap.privateKeyHint")}</p>
      </div>

      <div>
        <label className="block text-xs font-medium text-muted-foreground mb-1" htmlFor="ssh-pass">
          {t("bootstrap.passphrase")}
        </label>
        <input
          id="ssh-pass"
          type="password"
          value={config.passphrase ?? ""}
          onChange={(e) => update("passphrase", e.target.value)}
          className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
        />
      </div>

      <MutationError error={error} />

      <Button size="sm" onClick={onSubmit} disabled={!config.host || !config.private_key_pem || pending}>
        {pending ? t("common.loading") : t("bootstrap.readHostKey")}
      </Button>
    </div>
  );
}

interface ConfirmStepProps {
  config: SSHConfig;
  fingerprint: string;
  onBack: () => void;
  onConfirm: () => void;
  pending: boolean;
  error: Error | null;
  failureOutput: string | null;
}

function ConfirmStep({
  config, fingerprint, onBack, onConfirm, pending, error, failureOutput,
}: ConfirmStepProps) {
  return (
    <div className="space-y-3">
      {/* The fingerprint is what the admin verifies against the host they
          expect. Wrong fingerprint = someone else's server. The panel refuses
          to run anything until this value comes back on the next request. */}
      <div className="rounded-lg border border-warning bg-warning/10 p-3">
        <p className="text-xs font-semibold text-warning">
          {t("bootstrap.confirmHostKey")}
        </p>
        <dl className="mt-2 space-y-1 text-xs">
          <div className="flex gap-2">
            <dt className="text-muted-foreground">{t("bootstrap.host")}:</dt>
            <dd className="font-mono">{config.host}:{config.port}</dd>
          </div>
          <div className="flex gap-2">
            <dt className="text-muted-foreground">{t("bootstrap.fingerprint")}:</dt>
            <dd className="break-all font-mono">{fingerprint}</dd>
          </div>
        </dl>
        <p className="mt-2 text-xs text-muted-foreground">
          {t("bootstrap.fingerprintHint")}
        </p>
      </div>

      <MutationError error={error} />

      {/* On a 502 the install script's stderr is what an operator needs. It
          shows the exact command the panel ran, redacted for the audit log
          but not here, and whatever apt or systemctl said before it gave up. */}
      {failureOutput !== null && (
        <details open className="rounded-lg border border-destructive bg-destructive/5 p-3">
          <summary className="cursor-pointer text-xs font-semibold text-destructive">
            {t("bootstrap.failureOutput")}
          </summary>
          <pre className="mt-2 max-h-64 overflow-auto rounded border border-border bg-background p-2 font-mono text-[10px]">
            {failureOutput}
          </pre>
        </details>
      )}

      <div className="flex gap-2">
        <Button size="sm" onClick={onConfirm} disabled={pending}>
          {pending ? t("bootstrap.running") : t("bootstrap.confirmAndRun")}
        </Button>
        <Button size="sm" variant="outline" onClick={onBack} disabled={pending}>
          {t("common.back")}
        </Button>
      </div>
    </div>
  );
}
