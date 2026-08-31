import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "@/lib/api";
import { t } from "@/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { MutationError } from "@/routes/Resellers";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";

interface Admin {
  id: number;
  username: string;
  role: string;
  status: string;
  created_at: number;
}

export function Admins() {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState<Admin | null>(null);
  const admins = useQuery({
    queryKey: ["admins"],
    queryFn: () => api.get<{ admins: Admin[] }>("/api/v1/admins"),
  });
  const remove = useMutation({
    mutationFn: (id: number) => api.del(`/api/v1/admins/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["admins"] });
      setPending(null);
    },
  });

  return (
    <div>
      <div className="mb-4 flex items-center justify-between">
        <h2 className="text-lg font-semibold">{t("admins.title")}</h2>
        <Button size="sm" onClick={() => setOpen(true)}>
          {t("admins.create")}
        </Button>
      </div>
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent aria-describedby={undefined}>
          <SheetHeader>
            <SheetTitle>{t("admins.create")}</SheetTitle>
          </SheetHeader>
          <CreateAdmin onClose={() => setOpen(false)} />
        </SheetContent>
      </Sheet>
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-border text-xs text-muted-foreground">
            <th className="py-2 text-start">{t("admins.username")}</th>
            <th className="text-start">{t("admins.role")}</th>
            <th className="text-start">{t("subject.status")}</th>
            <th className="text-start">{t("actions")}</th>
          </tr>
        </thead>
        <tbody>
          {(admins.data?.admins ?? []).length === 0 ? (
            <tr>
              <td colSpan={4} className="py-6 text-center text-muted-foreground">
                {t("admins.empty")}
              </td>
            </tr>
          ) : (
            (admins.data?.admins ?? []).map((a) => (
              <tr key={a.id} className="border-b border-border/50">
                <td className="py-2 font-mono">{a.username}</td>
                <td>{a.role}</td>
                <td>{a.status}</td>
                <td>
                  <button
                    type="button"
                    className="text-xs text-destructive hover:underline"
                    onClick={() => setPending(a)}
                  >
                    {t("delete")}
                  </button>
                </td>
              </tr>
            ))
          )}
        </tbody>
      </table>
      <ConfirmDialog
        open={pending !== null}
        onOpenChange={(o) => !o && setPending(null)}
        title={t("admins.confirmDelete")}
        description={pending?.username}
        confirmLabel={t("delete")}
        pending={remove.isPending}
        onConfirm={() => pending && remove.mutate(pending.id)}
      />
    </div>
  );
}

function CreateAdmin({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState("admin");
  const create = useMutation({
    mutationFn: () => api.post("/api/v1/admins", { username, password, role }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["admins"] });
      onClose();
    },
  });
  return (
    <div className="space-y-3">
      <div>
        <label className="block text-xs text-muted-foreground" htmlFor="admin-username">
          {t("admins.username")}
        </label>
        <Input id="admin-username" value={username} onChange={(e) => setUsername(e.target.value)} />
      </div>
      <div>
        <label className="block text-xs text-muted-foreground" htmlFor="admin-password">
          {t("admins.password")}
        </label>
        <Input
          id="admin-password"
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
      </div>
      <div>
        <label className="block text-xs text-muted-foreground" htmlFor="admin-role">
          {t("admins.role")}
        </label>
        <select
          id="admin-role"
          value={role}
          onChange={(e) => setRole(e.target.value)}
          className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
        >
          <option value="super_admin">super_admin</option>
          <option value="admin">admin</option>
          <option value="reseller">reseller</option>
          <option value="readonly">readonly</option>
        </select>
      </div>
      <MutationError error={create.error} />
      <div className="flex gap-2">
        <Button
          size="sm"
          disabled={!username || password.length < 8 || create.isPending}
          onClick={() => create.mutate()}
        >
          {t("create")}
        </Button>
        <Button variant="outline" size="sm" onClick={onClose}>
          {t("cancel")}
        </Button>
      </div>
    </div>
  );
}
