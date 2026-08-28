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

interface Host {
  id: number;
  service_id: number;
  node_id: number;
  node_name: string;
  remark: string;
  address: string;
  port: number | null;
  sni: string;
  host: string;
  path: string;
  security: string;
  public_key: string;
  short_id: string;
  flow: string;
  enabled: boolean;
  priority: number;
}

interface CatalogService {
  id: number;
  node_id: number;
  node_name: string;
  adapter_kind: string;
  params: { protocol?: string; port?: number };
  enabled: boolean;
}

export function Hosts() {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [pendingDelete, setPendingDelete] = useState<Host | null>(null);

  const hosts = useQuery({
    queryKey: ["hosts"],
    queryFn: () => api.get<{ hosts: Host[] }>("/api/v1/hosts"),
  });

  const remove = useMutation({
    mutationFn: (id: number) => api.del(`/api/v1/hosts/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["hosts"] });
      setPendingDelete(null);
    },
  });

  return (
    <div>
      <div className="mb-4 flex items-center justify-between">
        <div>
          <h2 className="text-lg font-semibold">{t("hosts.title")}</h2>
          <p className="text-sm text-muted-foreground">{t("hosts.hint")}</p>
        </div>
        <Button size="sm" onClick={() => setOpen(true)}>
          {t("hosts.create")}
        </Button>
      </div>

      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent aria-describedby={undefined}>
          <SheetHeader>
            <SheetTitle>{t("hosts.create")}</SheetTitle>
          </SheetHeader>
          <HostForm onClose={() => setOpen(false)} />
        </SheetContent>
      </Sheet>

      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-border text-xs text-muted-foreground">
              <th className="py-2 text-start">{t("hosts.remark")}</th>
              <th className="text-start">{t("hosts.address")}</th>
              <th className="text-start">{t("hosts.inbound")}</th>
              <th className="text-start">{t("hosts.security")}</th>
              <th className="text-start">{t("actions")}</th>
            </tr>
          </thead>
          <tbody>
            {(hosts.data?.hosts ?? []).length === 0 ? (
              <tr>
                <td colSpan={5} className="py-6 text-center text-muted-foreground">
                  {t("hosts.empty")}
                </td>
              </tr>
            ) : (
              (hosts.data?.hosts ?? []).map((h) => (
                <tr key={h.id} className="border-b border-border/50">
                  <td className="py-2">{h.remark || "—"}</td>
                  <td className="font-mono text-xs">
                    {h.address || t("hosts.inherit")}
                    {h.port ? `:${h.port}` : ""}
                  </td>
                  <td>
                    {h.node_name} #{h.service_id}
                  </td>
                  <td className="font-mono text-xs">{h.security || t("hosts.inherit")}</td>
                  <td>
                    <button
                      type="button"
                      className="text-xs text-destructive hover:underline"
                      onClick={() => setPendingDelete(h)}
                    >
                      {t("delete")}
                    </button>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>

      <ConfirmDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => !open && setPendingDelete(null)}
        title={t("hosts.confirmDelete")}
        description={pendingDelete?.remark || pendingDelete?.address}
        confirmLabel={t("delete")}
        pending={remove.isPending}
        onConfirm={() => pendingDelete && remove.mutate(pendingDelete.id)}
      />
    </div>
  );
}

function HostForm({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const services = useQuery({
    queryKey: ["services-catalog"],
    queryFn: () => api.get<{ services: CatalogService[] }>("/api/v1/services"),
  });
  const [serviceId, setServiceId] = useState("");
  const [remark, setRemark] = useState("");
  const [address, setAddress] = useState("");
  const [sni, setSni] = useState("");
  const [security, setSecurity] = useState("");
  const [publicKey, setPublicKey] = useState("");
  const [shortId, setShortId] = useState("");

  const create = useMutation({
    mutationFn: () =>
      api.post("/api/v1/hosts", {
        service_id: Number(serviceId),
        remark,
        address,
        sni,
        security,
        public_key: publicKey,
        short_id: shortId,
        enabled: true,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["hosts"] });
      onClose();
    },
  });

  return (
    <div className="space-y-3">
      <div>
        <label className="block text-xs text-muted-foreground" htmlFor="host-service">
          {t("hosts.inbound")}
        </label>
        <select
          id="host-service"
          value={serviceId}
          onChange={(e) => setServiceId(e.target.value)}
          className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
        >
          <option value="">{t("hosts.chooseInbound")}</option>
          {(services.data?.services ?? []).map((s) => (
            <option key={s.id} value={s.id}>
              {s.node_name} · {s.adapter_kind}
              {s.params?.protocol ? `/${s.params.protocol}` : ""}
              {s.params?.port ? `:${s.params.port}` : ""}
            </option>
          ))}
        </select>
      </div>
      <Field id="host-remark" label={t("hosts.remark")} value={remark} onChange={setRemark} />
      <Field id="host-address" label={t("hosts.address")} value={address} onChange={setAddress} />
      <Field id="host-sni" label={t("hosts.sni")} value={sni} onChange={setSni} />
      <div>
        <label className="block text-xs text-muted-foreground" htmlFor="host-security">
          {t("hosts.security")}
        </label>
        <select
          id="host-security"
          value={security}
          onChange={(e) => setSecurity(e.target.value)}
          className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
        >
          <option value="">{t("hosts.inherit")}</option>
          <option value="none">none</option>
          <option value="tls">tls</option>
          <option value="reality">reality</option>
        </select>
      </div>
      {security === "reality" && (
        <>
          <Field id="host-pbk" label={t("hosts.publicKey")} value={publicKey} onChange={setPublicKey} />
          <Field id="host-sid" label={t("hosts.shortId")} value={shortId} onChange={setShortId} />
        </>
      )}
      <MutationError error={create.error} />
      <div className="flex gap-2">
        <Button size="sm" disabled={!serviceId || create.isPending} onClick={() => create.mutate()}>
          {t("create")}
        </Button>
        <Button variant="outline" size="sm" onClick={onClose}>
          {t("cancel")}
        </Button>
      </div>
    </div>
  );
}

function Field({
  id,
  label,
  value,
  onChange,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div>
      <label className="block text-xs text-muted-foreground" htmlFor={id}>
        {label}
      </label>
      <Input id={id} value={value} onChange={(e) => onChange(e.target.value)} />
    </div>
  );
}
