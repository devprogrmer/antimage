import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { useSession } from "../lib/session";
import { MutationError } from "../routes/Resellers";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { t } from "../i18n";

/**
 * TOTP enrolment and disable, for the signed-in admin only.
 *
 * The three routes existed and none was reachable from the browser: an admin
 * who logged in had no way to enable two-factor from the panel, and one who
 * had TOTP enrolled had no way to turn it off without shelling into the
 * database. The panel itself accepts recovery codes at the login screen, so a
 * locked-out admin could recover -- but only if they had ever been given the
 * codes, which required this UI.
 *
 * The secret and its provisioning URI are shown ONCE. They live in
 * totp_pending_enc, sealed under the master key; the panel does not send them
 * again after enrolment. The recovery codes are shown ONCE for the same
 * reason: their hashes live in admin_recovery_codes, and the plaintext is
 * only produced during handleTOTPConfirm's success path. An admin who closes
 * this card without capturing them starts over with a fresh /enrol.
 */
export function TOTPSection() {
  const session = useSession();
  const enabled = session.data?.totp_enabled === true;

  if (enabled) return <DisableForm />;
  return <EnrolForm />;
}

interface EnrolResponse {
  secret: string;
  provisioning_uri: string;
}

interface ConfirmResponse {
  recovery_codes: string[];
}

function EnrolForm() {
  const queryClient = useQueryClient();
  const [phase, setPhase] = useState<"idle" | "enrolled" | "confirmed">("idle");
  const [pending, setPending] = useState<EnrolResponse | null>(null);
  const [code, setCode] = useState("");
  const [codes, setCodes] = useState<string[]>([]);

  const enrol = useMutation({
    mutationFn: () => api.post<EnrolResponse>("/api/v1/auth/totp/enrol", {}),
    onSuccess: (data) => {
      setPending(data);
      setPhase("enrolled");
    },
  });

  const confirm = useMutation({
    mutationFn: () => api.post<ConfirmResponse>("/api/v1/auth/totp/confirm", { totp: code }),
    onSuccess: (data) => {
      setCodes(data.recovery_codes);
      setCode("");
      setPhase("confirmed");
      // Session cache carried totp_enabled=false; refetching flips the
      // Profile between EnrolForm and DisableForm without a full reload.
      queryClient.invalidateQueries({ queryKey: ["session"] });
    },
  });

  return (
    <section className="rounded border border-border bg-card p-4 space-y-3">
      <h3 className="text-sm font-semibold">{t("totp.title")}</h3>
      <p className="text-xs text-muted-foreground">{t("totp.description")}</p>

      {phase === "idle" && (
        <>
          <Button size="sm" onClick={() => enrol.mutate()} disabled={enrol.isPending}>
            {enrol.isPending ? t("common.loading") : t("totp.enrol")}
          </Button>
          <MutationError error={enrol.error} />
        </>
      )}

      {phase === "enrolled" && pending && (
        <>
          {/* Shown once. The panel does not resend it. An operator who
              closes the card without scanning it has to /enrol again. */}
          <p className="text-xs text-warning">{t("totp.scanOnce")}</p>
          <div>
            <p className="text-xs text-muted-foreground">{t("totp.provisioningURI")}</p>
            <code className="block break-all rounded border border-border bg-background p-2 font-mono text-xs">
              {pending.provisioning_uri}
            </code>
          </div>
          <div>
            <p className="text-xs text-muted-foreground">{t("totp.secret")}</p>
            <code className="block break-all rounded border border-border bg-background p-2 font-mono text-xs">
              {pending.secret}
            </code>
          </div>
          <div>
            <label className="block text-xs text-muted-foreground" htmlFor="totp-code">
              {t("totp.enterCode")}
            </label>
            <Input
              id="totp-code"
              value={code}
              onChange={(e) => setCode(e.target.value)}
              inputMode="numeric"
              autoComplete="one-time-code"
              placeholder="123456"
            />
          </div>
          <Button
            size="sm"
            onClick={() => confirm.mutate()}
            disabled={confirm.isPending || code.length !== 6}
          >
            {confirm.isPending ? t("common.loading") : t("totp.confirm")}
          </Button>
          <MutationError error={confirm.error} />
        </>
      )}

      {phase === "confirmed" && (
        <>
          {/* Shown ONCE. Only the hashes are stored; the plaintext codes
              exist outside this response nowhere on the server. */}
          <p className="text-xs text-warning">{t("totp.recoveryCodesOnce")}</p>
          <pre className="rounded border border-border bg-background p-2 font-mono text-xs">
            {codes.join("\n")}
          </pre>
          <p className="text-xs text-muted-foreground">{t("totp.recoveryCodesHint")}</p>
        </>
      )}
    </section>
  );
}

function DisableForm() {
  const queryClient = useQueryClient();
  const [code, setCode] = useState("");

  const disable = useMutation({
    mutationFn: () => api.post("/api/v1/auth/totp/disable", { totp: code }),
    onSuccess: () => {
      setCode("");
      queryClient.invalidateQueries({ queryKey: ["session"] });
    },
  });

  return (
    <section className="rounded border border-border bg-card p-4 space-y-3">
      <h3 className="text-sm font-semibold">{t("totp.enabledTitle")}</h3>
      <p className="text-xs text-muted-foreground">{t("totp.disableDescription")}</p>
      <div>
        <label className="block text-xs text-muted-foreground" htmlFor="totp-disable-code">
          {t("totp.enterCodeOrRecovery")}
        </label>
        <Input
          id="totp-disable-code"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          inputMode="numeric"
          autoComplete="one-time-code"
        />
      </div>
      <Button
        size="sm"
        variant="destructive"
        onClick={() => disable.mutate()}
        disabled={disable.isPending || code.trim() === ""}
      >
        {disable.isPending ? t("common.loading") : t("totp.disable")}
      </Button>
      <MutationError error={disable.error} />
    </section>
  );
}
