import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { useSession } from "../lib/session";
import { MutationError } from "./Resellers";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { formatTimestamp, t } from "../i18n";

// Access management: admins and roles.
//
// Read-only listing (the previous version of this page) let an operator see
// who had access; this version lets them shape it. Password reset revokes
// every session the target holds because a password change that leaves live
// sessions is theatre, not a rotation. Deletion is a status flip to
// 'suspended', not a DELETE FROM admins: audit rows reference admins by id.

interface AdminRow {
  id: number;
  username: string;
  role_id: number;
  role_name: string;
  status: "active" | "suspended";
  totp_enabled: boolean;
  created_at: number;
  scopes: number;
}

interface RoleRow {
  id: number;
  name: string;
  is_builtin: boolean;
  permissions: string[];
  assigned: number;
}

export function Access() {
  const session = useSession();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<AdminRow | null>(null);
  const [resetting, setResetting] = useState<AdminRow | null>(null);
  const [deleting, setDeleting] = useState<AdminRow | null>(null);

  const admins = useQuery({
    queryKey: ["admins"],
    queryFn: () => api.get<{ admins: AdminRow[] }>("/api/v1/admins"),
  });
  const roles = useQuery({
    queryKey: ["roles"],
    queryFn: () => api.get<{ roles: RoleRow[] }>("/api/v1/roles"),
  });

  const del = useMutation({
    mutationFn: (id: number) => api.del(`/api/v1/admins/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["admins"] });
      setDeleting(null);
    },
  });

  const meID = session.data?.admin_id;

  return (
    <div className="space-y-6 p-4">
      <header className="flex items-start justify-between">
        <div>
          <h1 className="text-xl font-bold">{t("access.title")}</h1>
          <p className="mt-1 text-sm text-muted-foreground">{t("access.description")}</p>
        </div>
        <Button size="sm" onClick={() => setCreating(true)}>
          {t("access.newAdmin")}
        </Button>
      </header>

      <section>
        <h2 className="text-sm font-semibold mb-3">{t("access.admins")}</h2>
        {admins.isLoading ? (
          <p className="text-xs text-muted-foreground">{t("common.loading")}</p>
        ) : (admins.data?.admins ?? []).length === 0 ? (
          <p className="text-xs text-muted-foreground">{t("access.noAdmins")}</p>
        ) : (
          <table className="w-full text-sm border-collapse">
            <thead>
              <tr className="border-b border-border text-xs text-muted-foreground">
                <th className="py-2 text-start">{t("access.username")}</th>
                <th className="text-start">{t("access.role")}</th>
                <th className="text-start">{t("access.status")}</th>
                <th className="text-start">{t("access.twoFactor")}</th>
                <th className="text-start">{t("access.scopes")}</th>
                <th className="text-start">{t("access.created")}</th>
                <th className="text-end">{t("access.actions")}</th>
              </tr>
            </thead>
            <tbody>
              {(admins.data?.admins ?? []).map((a) => (
                <tr key={a.id} className="border-b border-border">
                  <td className="py-1.5 font-mono text-xs">
                    {a.username}
                    {a.id === meID && (
                      <span className="ms-2 rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                        {t("access.you")}
                      </span>
                    )}
                  </td>
                  <td className="text-xs">{a.role_name}</td>
                  <td className="text-xs">
                    <span
                      className={
                        a.status === "active"
                          ? "rounded bg-success/10 px-2 py-0.5 text-success"
                          : "rounded bg-destructive/10 px-2 py-0.5 text-destructive"
                      }
                    >
                      {t(`access.status.${a.status}`)}
                    </span>
                  </td>
                  <td className="text-xs">
                    {a.totp_enabled ? t("access.totpOn") : t("access.totpOff")}
                  </td>
                  <td className="text-xs">
                    {a.scopes === 0
                      ? t("access.scopesUnrestricted")
                      : t("access.scopesN", { count: String(a.scopes) })}
                  </td>
                  <td className="text-xs text-muted-foreground font-mono">
                    {formatTimestamp(a.created_at)}
                  </td>
                  <td className="text-end text-xs">
                    <div className="flex justify-end gap-2">
                      <button
                        type="button"
                        onClick={() => setEditing(a)}
                        className="text-primary hover:underline"
                      >
                        {t("access.edit")}
                      </button>
                      <button
                        type="button"
                        onClick={() => setResetting(a)}
                        className="text-warning hover:underline"
                      >
                        {t("access.resetPassword")}
                      </button>
                      {a.id !== meID && (
                        <button
                          type="button"
                          onClick={() => setDeleting(a)}
                          className="text-destructive hover:underline"
                        >
                          {t("access.suspend")}
                        </button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      <section>
        <h2 className="text-sm font-semibold mb-3">{t("access.roles")}</h2>
        {roles.isLoading ? (
          <p className="text-xs text-muted-foreground">{t("common.loading")}</p>
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            {(roles.data?.roles ?? []).map((r) => (
              <div key={r.id} className="rounded border border-border bg-card p-3">
                <div className="flex items-center justify-between mb-2">
                  <h3 className="font-mono text-sm">{r.name}</h3>
                  <div className="flex gap-2 text-[10px]">
                    {r.is_builtin && (
                      <span className="rounded bg-muted px-2 py-0.5 text-muted-foreground">
                        {t("access.builtin")}
                      </span>
                    )}
                    <span className="rounded bg-primary/10 px-2 py-0.5 text-primary">
                      {t("access.assignedN", { count: String(r.assigned) })}
                    </span>
                  </div>
                </div>
                <ul className="text-xs text-muted-foreground space-y-0.5">
                  {r.permissions.length === 0 ? (
                    <li>{t("access.noPermissions")}</li>
                  ) : (
                    r.permissions.map((p) => (
                      <li key={p} className="font-mono">
                        {p}
                      </li>
                    ))
                  )}
                </ul>
              </div>
            ))}
          </div>
        )}
      </section>

      {creating && (
        <CreateAdminDialog
          roles={roles.data?.roles ?? []}
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            queryClient.invalidateQueries({ queryKey: ["admins"] });
          }}
        />
      )}

      {editing && (
        <EditAdminDialog
          admin={editing}
          roles={roles.data?.roles ?? []}
          isSelf={editing.id === meID}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            queryClient.invalidateQueries({ queryKey: ["admins"] });
          }}
        />
      )}

      {resetting && (
        <ResetPasswordDialog
          admin={resetting}
          onClose={() => setResetting(null)}
          onDone={() => {
            setResetting(null);
            queryClient.invalidateQueries({ queryKey: ["admins"] });
          }}
        />
      )}

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={t("access.confirmSuspend")}
        description={t("access.confirmSuspendDescription", {
          username: deleting?.username ?? "",
        })}
        confirmLabel={t("access.suspend")}
        pending={del.isPending}
        onConfirm={() => deleting && del.mutate(deleting.id)}
      />
      <MutationError error={del.error} />
    </div>
  );
}

function CreateAdminDialog({
  roles,
  onClose,
  onCreated,
}: {
  roles: RoleRow[];
  onClose: () => void;
  onCreated: () => void;
}) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [roleID, setRoleID] = useState<number>(roles[0]?.id ?? 0);

  const create = useMutation({
    mutationFn: () =>
      api.post<{ id: number }>("/api/v1/admins", {
        username: username.trim(),
        password,
        role_id: roleID,
      }),
    onSuccess: onCreated,
  });

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <div className="w-full max-w-md rounded border border-border bg-background p-4 space-y-3">
        <h3 className="text-sm font-semibold">{t("access.newAdmin")}</h3>
        <div>
          <label htmlFor="admin-username" className="block text-xs text-muted-foreground">
            {t("access.username")}
          </label>
          <Input
            id="admin-username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="off"
          />
        </div>
        <div>
          <label htmlFor="admin-password" className="block text-xs text-muted-foreground">
            {t("access.password")}
          </label>
          <Input
            id="admin-password"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="new-password"
          />
          {/* The backend rejects <8 chars; saying so here beats the operator
              hitting submit and getting a 422 they have to translate. */}
          <p className="mt-1 text-[11px] text-muted-foreground">
            {t("access.passwordHint")}
          </p>
        </div>
        <div>
          <label htmlFor="admin-role" className="block text-xs text-muted-foreground">
            {t("access.role")}
          </label>
          <select
            id="admin-role"
            value={roleID}
            onChange={(e) => setRoleID(Number(e.target.value))}
            className="w-full rounded border border-input bg-background px-3 py-2 text-sm"
          >
            {roles.map((r) => (
              <option key={r.id} value={r.id}>
                {r.name}
              </option>
            ))}
          </select>
        </div>
        <MutationError error={create.error} />
        <div className="flex justify-end gap-2">
          <Button size="sm" variant="outline" onClick={onClose}>
            {t("cancel")}
          </Button>
          <Button
            size="sm"
            disabled={
              create.isPending ||
              username.trim() === "" ||
              password.length < 8 ||
              roleID === 0
            }
            onClick={() => create.mutate()}
          >
            {t("access.create")}
          </Button>
        </div>
      </div>
    </div>
  );
}

function EditAdminDialog({
  admin,
  roles,
  isSelf,
  onClose,
  onSaved,
}: {
  admin: AdminRow;
  roles: RoleRow[];
  isSelf: boolean;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [roleID, setRoleID] = useState(admin.role_id);
  const [status, setStatus] = useState<AdminRow["status"]>(admin.status);

  const save = useMutation({
    mutationFn: () => {
      const body: Record<string, unknown> = {};
      if (roleID !== admin.role_id) body.role_id = roleID;
      if (status !== admin.status) body.status = status;
      return api.put(`/api/v1/admins/${admin.id}`, body);
    },
    onSuccess: onSaved,
  });

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <div className="w-full max-w-md rounded border border-border bg-background p-4 space-y-3">
        <h3 className="text-sm font-semibold">
          {t("access.editAdmin", { username: admin.username })}
        </h3>
        <div>
          <label htmlFor="edit-role" className="block text-xs text-muted-foreground">
            {t("access.role")}
          </label>
          <select
            id="edit-role"
            value={roleID}
            disabled={isSelf}
            onChange={(e) => setRoleID(Number(e.target.value))}
            className="w-full rounded border border-input bg-background px-3 py-2 text-sm disabled:opacity-50"
          >
            {roles.map((r) => (
              <option key={r.id} value={r.id}>
                {r.name}
              </option>
            ))}
          </select>
          {/* Self-lockout guard: the panel refuses this server-side too, but
              stating it here beats an operator hitting save and having the
              browser bounce them. */}
          {isSelf && (
            <p className="mt-1 text-[11px] text-muted-foreground">
              {t("access.cannotEditSelfRole")}
            </p>
          )}
        </div>
        <div>
          <label htmlFor="edit-status" className="block text-xs text-muted-foreground">
            {t("access.status")}
          </label>
          <select
            id="edit-status"
            value={status}
            disabled={isSelf}
            onChange={(e) => setStatus(e.target.value as AdminRow["status"])}
            className="w-full rounded border border-input bg-background px-3 py-2 text-sm disabled:opacity-50"
          >
            <option value="active">{t("access.status.active")}</option>
            <option value="suspended">{t("access.status.suspended")}</option>
          </select>
        </div>
        <MutationError error={save.error} />
        <div className="flex justify-end gap-2">
          <Button size="sm" variant="outline" onClick={onClose}>
            {t("cancel")}
          </Button>
          <Button size="sm" onClick={() => save.mutate()} disabled={save.isPending}>
            {t("access.save")}
          </Button>
        </div>
      </div>
    </div>
  );
}

function ResetPasswordDialog({
  admin,
  onClose,
  onDone,
}: {
  admin: AdminRow;
  onClose: () => void;
  onDone: () => void;
}) {
  const [password, setPassword] = useState("");

  const reset = useMutation({
    mutationFn: () =>
      api.post(`/api/v1/admins/${admin.id}/password`, { new_password: password }),
    onSuccess: onDone,
  });

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <div className="w-full max-w-md rounded border border-border bg-background p-4 space-y-3">
        <h3 className="text-sm font-semibold">
          {t("access.resetFor", { username: admin.username })}
        </h3>
        {/* The reset revokes every session the target holds. Saying so
            before the operator clicks beats a surprise sign-out for the
            person on the other end. */}
        <p className="text-xs text-warning">{t("access.resetWarning")}</p>
        <div>
          <label htmlFor="reset-password" className="block text-xs text-muted-foreground">
            {t("access.newPassword")}
          </label>
          <Input
            id="reset-password"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="new-password"
          />
          <p className="mt-1 text-[11px] text-muted-foreground">{t("access.passwordHint")}</p>
        </div>
        <MutationError error={reset.error} />
        <div className="flex justify-end gap-2">
          <Button size="sm" variant="outline" onClick={onClose}>
            {t("cancel")}
          </Button>
          <Button
            size="sm"
            variant="destructive"
            onClick={() => reset.mutate()}
            disabled={reset.isPending || password.length < 8}
          >
            {t("access.reset")}
          </Button>
        </div>
      </div>
    </div>
  );
}
