import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { MutationError } from "../routes/Resellers";
import { ConfirmDialog } from "./ConfirmDialog";
import { Badge } from "./ui/badge";
import { formatTimestamp, t } from "../i18n";

interface Session {
  id: number;
  ip: string;
  user_agent: string;
  created_at: number;
  last_used_at: number;
  expires_at: number;
  current: boolean;
}

/**
 * The operator's own signed-in sessions, and a way to end one.
 *
 * Both endpoints existed and had no client. They are self-scoped rather than
 * permission-gated -- the list selects WHERE admin_id = the caller, and revoke
 * answers 404 for a session the caller does not own unless they are a super
 * admin -- so this is "my sessions", not an administrative view of everyone's.
 *
 * It matters because it is the only way an operator can act on "I signed in
 * from a machine I no longer trust" without changing their password.
 */
export function SessionList() {
  const queryClient = useQueryClient();
  const [pending, setPending] = useState<Session | null>(null);

  const sessions = useQuery({
    queryKey: ["sessions"],
    queryFn: () => api.get<{ sessions: Session[] }>("/api/v1/sessions"),
  });

  const revoke = useMutation({
    mutationFn: (id: number) => api.del(`/api/v1/sessions/${id}`),
    onSuccess: () => {
      setPending(null);
      queryClient.invalidateQueries({ queryKey: ["sessions"] });
    },
  });

  return (
    <section>
      <h3 className="mb-2 text-xs uppercase tracking-wide text-muted-foreground">
        {t("session.title")}
      </h3>
      <MutationError error={sessions.error} />

      <table className="w-full border-collapse text-xs">
        <thead>
          <tr className="border-b border-border text-muted-foreground">
            <th className="py-1 pe-3 text-start">{t("session.ip")}</th>
            <th className="pe-3 text-start">{t("session.device")}</th>
            <th className="pe-3 text-start">{t("session.lastUsed")}</th>
            <th className="text-start">{t("actions")}</th>
          </tr>
        </thead>
        <tbody>
          {(sessions.data?.sessions ?? []).map((s) => (
            <tr key={s.id} className="border-b border-border/50">
              <td className="py-1.5 pe-3 font-mono">
                {s.ip}
                {s.current && (
                  <Badge variant="outline" className="ms-2">
                    {t("session.current")}
                  </Badge>
                )}
              </td>
              {/* A user agent is long and low-value at a glance; the full
                  string is there for anyone comparing two of them. */}
              <td className="max-w-xs truncate pe-3 text-muted-foreground" title={s.user_agent}>
                {s.user_agent}
              </td>
              <td className="pe-3 font-mono text-muted-foreground">
                {formatTimestamp(s.last_used_at)}
              </td>
              <td>
                {/* Revoking the current session is signing out, and there is a
                    button for that which also clears the cached session. Doing
                    it from here would leave the UI holding a dead cookie. */}
                {!s.current && (
                  <button
                    type="button"
                    onClick={() => setPending(s)}
                    className="text-destructive hover:underline"
                  >
                    {t("session.revoke")}
                  </button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <MutationError error={revoke.error} />

      <ConfirmDialog
        open={pending !== null}
        onOpenChange={(open) => !open && setPending(null)}
        title={t("session.confirmRevoke")}
        description={pending ? `${pending.ip} — ${pending.user_agent}` : undefined}
        confirmLabel={t("session.revoke")}
        pending={revoke.isPending}
        onConfirm={() => pending && revoke.mutate(pending.id)}
      />
    </section>
  );
}
