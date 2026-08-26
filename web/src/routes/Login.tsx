import { useState, type FormEvent } from "react";
import { api, ApiError } from "../lib/api";
import { t } from "../i18n";

export function Login({ onSuccess }: { onSuccess: () => void }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [totp, setTotp] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      // totp is always sent: the panel requires a valid code from any admin
      // who has enrolled a secret, so omitting the field would lock those
      // accounts out entirely.
      await api.post("/api/v1/auth/login", { username, password, totp });
      onSuccess();
    } catch (err) {
      const code = err instanceof ApiError ? err.code : "unknown";
      setError(code === "rate_limited" ? t("auth.rateLimited") : t("auth.invalid"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="flex min-h-screen items-center justify-center bg-background text-foreground">
      <form onSubmit={submit} className="w-80 space-y-3 rounded border border-border p-6">
        <h1 className="text-sm font-semibold tracking-wide text-muted-foreground">{t("app.name")}</h1>

        <label className="block text-xs text-muted-foreground">
          {t("auth.username")}
          <input
            className="mt-1 w-full rounded border border-input bg-card px-2 py-1 text-sm text-start"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="username"
            required
          />
        </label>

        <label className="block text-xs text-muted-foreground">
          {t("auth.password")}
          <input
            type="password"
            className="mt-1 w-full rounded border border-input bg-card px-2 py-1 text-sm text-start"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
            required
          />
        </label>

        <label className="block text-xs text-muted-foreground">
          {t("auth.totp")}
          <input
            className="mt-1 w-full rounded border border-input bg-card px-2 py-1 font-mono text-sm text-start"
            value={totp}
            onChange={(e) => setTotp(e.target.value)}
            inputMode="numeric"
            autoComplete="one-time-code"
          />
        </label>

        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}

        <button
          type="submit"
          disabled={busy}
          className="w-full rounded bg-primary px-2 py-1 text-sm font-medium text-primary-foreground disabled:opacity-50"
        >
          {t("auth.signIn")}
        </button>
      </form>
    </main>
  );
}
