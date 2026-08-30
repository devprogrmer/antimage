import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { MutationError } from "../routes/Resellers";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { t } from "../i18n";

/**
 * Rotate the signed-in admin's own password.
 *
 * On success the panel revokes every session for this admin, including the
 * one this tab is holding, so the client is expected to log in again with
 * the new password. That is the correct behaviour for a rotation: anything
 * that still held the previous credential has to prove the new one.
 */
export function ChangePasswordSection() {
  const queryClient = useQueryClient();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirmed, setConfirmed] = useState("");
  const [done, setDone] = useState(false);

  const change = useMutation({
    mutationFn: () =>
      api.post("/api/v1/me/password", {
        current_password: current,
        new_password: next,
      }),
    onSuccess: () => {
      setCurrent("");
      setNext("");
      setConfirmed("");
      setDone(true);
      // The session cache is stale the moment the write returns; the next
      // request will 401 and the login screen will take over.
      queryClient.clear();
    },
  });

  const mismatch = next !== "" && confirmed !== "" && next !== confirmed;
  const canSubmit =
    current !== "" && next.length >= 8 && next === confirmed && !change.isPending;

  return (
    <section className="rounded border border-border bg-card p-4 space-y-3">
      <h3 className="text-sm font-semibold">{t("password.title")}</h3>
      <p className="text-xs text-muted-foreground">{t("password.description")}</p>

      {done ? (
        <p className="rounded bg-success/10 p-3 text-xs text-success">
          {t("password.doneSignInAgain")}
        </p>
      ) : (
        <>
          <div>
            <label className="block text-xs text-muted-foreground" htmlFor="cur-pwd">
              {t("password.current")}
            </label>
            <Input
              id="cur-pwd"
              type="password"
              autoComplete="current-password"
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
            />
          </div>
          <div>
            <label className="block text-xs text-muted-foreground" htmlFor="new-pwd">
              {t("password.new")}
            </label>
            <Input
              id="new-pwd"
              type="password"
              autoComplete="new-password"
              value={next}
              onChange={(e) => setNext(e.target.value)}
            />
            <p className="mt-1 text-[11px] text-muted-foreground">{t("access.passwordHint")}</p>
          </div>
          <div>
            <label className="block text-xs text-muted-foreground" htmlFor="confirm-pwd">
              {t("password.confirm")}
            </label>
            <Input
              id="confirm-pwd"
              type="password"
              autoComplete="new-password"
              value={confirmed}
              onChange={(e) => setConfirmed(e.target.value)}
            />
            {mismatch && (
              <p className="mt-1 text-[11px] text-destructive">{t("password.mismatch")}</p>
            )}
          </div>
          <MutationError error={change.error} />
          <Button
            size="sm"
            disabled={!canSubmit}
            onClick={() => change.mutate()}
          >
            {change.isPending ? t("common.loading") : t("password.change")}
          </Button>
        </>
      )}
    </section>
  );
}
