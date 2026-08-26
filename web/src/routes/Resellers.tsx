import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ApiError, api } from "../lib/api";
import { can, useSession } from "../lib/session";
import { formatNumber, formatTimestamp, t } from "../i18n";

export interface Reseller {
  id: number;
  admin_id: number;
  display_name: string;
  enabled: boolean;
  // null means unlimited. Zero is a real limit meaning "may create nothing",
  // so the two are never collapsed anywhere in this file.
  max_subjects: number | null;
  max_quota_bytes: number | null;
  credit_floor: number;
  created_at: number;
  updated_at: number;
}

export function MutationError({ error }: { error: unknown }) {
  if (!error) return null;
  // The engine's refusals name the ceiling and how much of it is used, which is
  // more use to an operator than a generic failure, so they are shown verbatim
  // rather than mapped onto a local string.
  const message = error instanceof ApiError ? error.message : String(error);
  return (
    <p className="mt-1 text-xs text-destructive" role="alert">
      {message}
    </p>
  );
}

/** Renders a nullable limit, where null is unlimited rather than zero. */
export function Limit({ value }: { value: number | null }) {
  if (value === null) return <span className="text-muted-foreground">{t("reseller.unlimited")}</span>;
  return <span className="font-mono">{formatNumber(value)}</span>;
}

export function Resellers({ onSelect }: { onSelect: (id: number) => void }) {
  const session = useSession();
  const [showCreate, setShowCreate] = useState(false);

  const resellers = useQuery({
    queryKey: ["resellers"],
    queryFn: () => api.get<{ resellers: Reseller[] }>("/api/v1/resellers"),
  });

  if (resellers.isError) {
    return <MutationError error={resellers.error} />;
  }

  return (
    <div>
      <div className="mb-4 flex items-center justify-between">
        <h2 className="text-lg font-semibold">{t("resellers.title")}</h2>
        {can(session.data, "reseller:write") && (
          <button
            type="button"
            onClick={() => setShowCreate(true)}
            className="rounded bg-primary px-3 py-1 text-sm hover:bg-primary/90"
          >
            {t("resellers.create")}
          </button>
        )}
      </div>

      {showCreate && <CreateResellerForm onClose={() => setShowCreate(false)} />}

      {resellers.data?.resellers.length === 0 && (
        <p className="text-sm text-muted-foreground">{t("resellers.empty")}</p>
      )}

      <table className="w-full border-collapse text-sm text-foreground">
        <thead>
          <tr className="border-b border-border text-xs uppercase tracking-wide text-muted-foreground">
            <th className="py-2 pe-3 text-start">{t("reseller.displayName")}</th>
            <th className="pe-3 text-start">{t("reseller.status")}</th>
            <th className="pe-3 text-start">{t("reseller.maxSubjects")}</th>
            <th className="pe-3 text-start">{t("reseller.creditFloor")}</th>
            <th className="text-start">{t("reseller.created")}</th>
          </tr>
        </thead>
        <tbody>
          {resellers.data?.resellers.map((reseller) => (
            <tr
              key={reseller.id}
              onClick={() => onSelect(reseller.id)}
              className="cursor-pointer border-b border-border hover:bg-accent/50"
            >
              <td className="py-1.5 pe-3 font-mono">{reseller.display_name}</td>
              <td className="pe-3">
                {reseller.enabled ? (
                  <span className="text-success">{t("reseller.enabled")}</span>
                ) : (
                  <span className="text-muted-foreground">{t("reseller.disabled")}</span>
                )}
              </td>
              <td className="pe-3">
                <Limit value={reseller.max_subjects} />
              </td>
              <td className="pe-3 font-mono text-xs">{formatNumber(reseller.credit_floor)}</td>
              <td className="font-mono text-xs text-muted-foreground">
                {formatTimestamp(reseller.created_at)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/**
 * A tenant is an ADMIN ACCOUNT plus a billing relationship, not an account of
 * its own: admin_id names an existing account to promote. One admin operates at
 * most one tenant, which is what makes "my tenancy" resolvable from a session
 * alone, so a second attempt comes back 409 and is surfaced as such.
 */
function CreateResellerForm({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [adminID, setAdminID] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [creditFloor, setCreditFloor] = useState("0");

  const create = useMutation({
    mutationFn: () =>
      api.post<{ id: number }>("/api/v1/resellers", {
        admin_id: Number(adminID),
        display_name: displayName,
        credit_floor: Number(creditFloor),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["resellers"] });
      onClose();
    },
  });

  return (
    <div className="mb-4 rounded border border-border bg-card p-4">
      <h3 className="mb-3 text-sm font-semibold">{t("resellers.createNew")}</h3>
      <div className="space-y-3">
        <div>
          <label className="block text-xs text-muted-foreground" htmlFor="reseller-admin-id">
            {t("reseller.adminId")}
          </label>
          <input
            id="reseller-admin-id"
            type="number"
            value={adminID}
            onChange={(e) => setAdminID(e.target.value)}
            className="w-full rounded border border-input bg-background px-2 py-1 text-sm"
          />
          <p className="mt-1 text-xs text-muted-foreground">{t("reseller.adminIdHint")}</p>
        </div>
        <div>
          <label className="block text-xs text-muted-foreground" htmlFor="reseller-display-name">
            {t("reseller.displayName")}
          </label>
          <input
            id="reseller-display-name"
            type="text"
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
            className="w-full rounded border border-input bg-background px-2 py-1 text-sm"
          />
        </div>
        <div>
          <label className="block text-xs text-muted-foreground" htmlFor="reseller-credit-floor">
            {t("reseller.creditFloor")}
          </label>
          <input
            id="reseller-credit-floor"
            type="number"
            value={creditFloor}
            onChange={(e) => setCreditFloor(e.target.value)}
            className="w-full rounded border border-input bg-background px-2 py-1 text-sm"
          />
          <p className="mt-1 text-xs text-muted-foreground">{t("reseller.creditFloorHint")}</p>
        </div>
        <MutationError error={create.error} />
        <div className="flex gap-2">
          <button
            type="button"
            onClick={() => create.mutate()}
            disabled={!adminID || !displayName || create.isPending}
            className="rounded bg-primary px-3 py-1 text-sm hover:bg-primary/90 disabled:opacity-50"
          >
            {t("create")}
          </button>
          <button
            type="button"
            onClick={onClose}
            className="rounded bg-secondary px-3 py-1 text-sm hover:bg-secondary/80"
          >
            {t("cancel")}
          </button>
        </div>
      </div>
    </div>
  );
}
