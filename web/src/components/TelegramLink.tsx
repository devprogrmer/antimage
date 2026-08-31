import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../lib/api";
import { t } from "../i18n";

interface Link {
  telegram_id: number;
  username: string;
  linked_at: number;
  last_seen_at: number;
}

interface StatusResponse {
  linked: boolean;
  link?: Link;
}

interface CodeResponse {
  code: string;
  expires_in: number;
}

/**
 * TelegramLink manages the caller's own Telegram binding.
 *
 * Every request here is scoped to the session server-side -- there is no admin
 * id to pass and none is accepted -- so this component cannot be pointed at
 * another operator's account by tampering with its props.
 */
export function TelegramLink() {
  const [status, setStatus] = useState<StatusResponse | null>(null);
  const [code, setCode] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // Seconds left on the displayed code. Shown so an operator who walks away
  // knows the code on screen is stale rather than typing a dead one.
  const [remaining, setRemaining] = useState(0);

  const load = useCallback(async () => {
    try {
      setStatus(await api.get<StatusResponse>("/api/v1/me/telegram"));
    } catch {
      setError(t("telegram.loadFailed"));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // Count the displayed code down and drop it at zero, so the UI never offers
  // a code the server will refuse.
  useEffect(() => {
    if (code === null || remaining <= 0) return;
    const timer = window.setTimeout(() => setRemaining((s) => s - 1), 1000);
    return () => window.clearTimeout(timer);
  }, [code, remaining]);

  useEffect(() => {
    if (code !== null && remaining === 0) setCode(null);
  }, [code, remaining]);

  async function issue() {
    setBusy(true);
    setError(null);
    try {
      const res = await api.post<CodeResponse>("/api/v1/me/telegram/link");
      setCode(res.code);
      setRemaining(res.expires_in);
    } catch {
      setError(t("telegram.issueFailed"));
    } finally {
      setBusy(false);
    }
  }

  async function unlink() {
    setBusy(true);
    setError(null);
    try {
      await api.del("/api/v1/me/telegram");
      setCode(null);
      await load();
    } catch (err) {
      // A 404 means it was already unlinked, which is the state the operator
      // asked for; anything else is a real failure.
      if (err instanceof ApiError && err.status === 404) {
        await load();
      } else {
        setError(t("telegram.unlinkFailed"));
      }
    } finally {
      setBusy(false);
    }
  }

  function formatWhen(ts: number): string {
    return new Date(ts * 1000).toLocaleString();
  }

  function formatRemaining(seconds: number): string {
    const m = Math.floor(seconds / 60);
    const s = seconds % 60;
    return `${m}:${String(s).padStart(2, "0")}`;
  }

  return (
    <section className="rounded border border-border bg-card/50 p-4">
      <h2 className="text-sm font-semibold text-foreground">{t("telegram.title")}</h2>
      <p className="mt-1 text-xs text-muted-foreground">{t("telegram.description")}</p>

      {error !== null && (
        <p role="alert" className="mt-3 text-xs text-destructive">
          {error}
        </p>
      )}

      {status === null ? (
        <p className="mt-3 text-xs text-muted-foreground">{t("common.loading")}</p>
      ) : status.linked && status.link !== undefined ? (
        <div className="mt-3 space-y-2">
          <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs">
            <dt className="text-muted-foreground">{t("telegram.account")}</dt>
            <dd className="font-mono text-foreground">
              {status.link.username !== "" ? `@${status.link.username}` : status.link.telegram_id}
            </dd>
            <dt className="text-muted-foreground">{t("telegram.linkedAt")}</dt>
            <dd className="text-foreground">{formatWhen(status.link.linked_at)}</dd>
            <dt className="text-muted-foreground">{t("telegram.lastSeen")}</dt>
            <dd className="text-foreground">{formatWhen(status.link.last_seen_at)}</dd>
          </dl>
          <button
            type="button"
            onClick={unlink}
            disabled={busy}
            className="rounded border border-destructive/40 px-2 py-1 text-xs text-destructive hover:bg-destructive/10 disabled:opacity-50"
          >
            {t("telegram.unlink")}
          </button>
          <p className="text-xs text-muted-foreground">{t("telegram.unlinkHint")}</p>
        </div>
      ) : code !== null ? (
        <div className="mt-3 space-y-2">
          <p className="text-xs text-foreground">{t("telegram.sendToBot")}</p>
          {/*
            select-all so one click grabs the whole command. The code is a
            credential with a ten-minute life, so it is deliberately shown as
            text to be read rather than stored anywhere.
          */}
          <code className="block select-all rounded bg-background px-3 py-2 font-mono text-sm text-success">
            /link {code}
          </code>
          <p className="text-xs text-muted-foreground">
            {t("telegram.expiresIn")} {formatRemaining(remaining)}
          </p>
          <button
            type="button"
            onClick={() => setCode(null)}
            className="text-xs text-muted-foreground hover:text-foreground"
          >
            {t("common.cancel")}
          </button>
        </div>
      ) : (
        <div className="mt-3 space-y-2">
          <button
            type="button"
            onClick={issue}
            disabled={busy}
            className="rounded border border-input px-2 py-1 text-xs text-foreground hover:bg-accent disabled:opacity-50"
          >
            {t("telegram.link")}
          </button>
          <p className="text-xs text-muted-foreground">{t("telegram.linkHint")}</p>
        </div>
      )}
    </section>
  );
}
