import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "@/lib/api";
import { formatTimestamp, t } from "@/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { MutationError } from "@/routes/Resellers";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";

/** Proxy Hub: third-party VPN/proxy provider accounts (Cloudflare WARP,
 *  NordVPN) an operator registers once, then provisions onto any node as a
 *  real WireGuard outbound. Accounts are not node-scoped -- one credential
 *  can back outbounds on several nodes -- so this lives as its own page
 *  rather than folded into a node's detail screen, the way EgressPanel is. */

type Provider = "warp" | "nordvpn";

interface ProxyProviderAccount {
  id: number;
  provider: Provider;
  label: string;
  metadata: Record<string, unknown>;
  created_at: number;
}

interface NodeOption {
  id: number;
  name: string;
}

interface NordVPNCountry {
  id: number;
  name: string;
  code: string;
}

interface NordVPNServer {
  id: number;
  name: string;
  station: string;
  load: number;
  public_key: string;
}

export function ProxyHub() {
  const queryClient = useQueryClient();
  const [registerOpen, setRegisterOpen] = useState(false);
  const [provisionAccount, setProvisionAccount] = useState<ProxyProviderAccount | null>(null);
  const [pendingDelete, setPendingDelete] = useState<ProxyProviderAccount | null>(null);

  const accounts = useQuery({
    queryKey: ["proxy-providers"],
    queryFn: () => api.get<{ accounts: ProxyProviderAccount[] }>("/api/v1/proxy-providers"),
  });

  const remove = useMutation({
    mutationFn: (id: number) => api.del(`/api/v1/proxy-providers/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["proxy-providers"] });
      setPendingDelete(null);
    },
  });

  return (
    <div>
      <div className="mb-4 flex items-center justify-between">
        <div>
          <h2 className="text-lg font-semibold">{t("proxyHub.title")}</h2>
          <p className="text-sm text-muted-foreground">{t("proxyHub.hint")}</p>
        </div>
        <Button size="sm" onClick={() => setRegisterOpen(true)}>
          {t("proxyHub.registerAccount")}
        </Button>
      </div>

      <Sheet open={registerOpen} onOpenChange={setRegisterOpen}>
        <SheetContent aria-describedby={undefined}>
          <SheetHeader>
            <SheetTitle>{t("proxyHub.registerAccount")}</SheetTitle>
          </SheetHeader>
          <RegisterForm onClose={() => setRegisterOpen(false)} />
        </SheetContent>
      </Sheet>

      <Sheet open={provisionAccount !== null} onOpenChange={(open) => !open && setProvisionAccount(null)}>
        <SheetContent aria-describedby={undefined}>
          <SheetHeader>
            <SheetTitle>
              {t("proxyHub.provisionOutbound")}: {provisionAccount?.label}
            </SheetTitle>
          </SheetHeader>
          {provisionAccount && (
            <ProvisionForm account={provisionAccount} onClose={() => setProvisionAccount(null)} />
          )}
        </SheetContent>
      </Sheet>

      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-border text-xs text-muted-foreground">
              <th className="py-2 text-start">{t("proxyHub.label")}</th>
              <th className="text-start">{t("proxyHub.provider")}</th>
              <th className="text-start">{t("proxyHub.createdAt")}</th>
              <th className="text-start">{t("actions")}</th>
            </tr>
          </thead>
          <tbody>
            {(accounts.data?.accounts ?? []).length === 0 ? (
              <tr>
                <td colSpan={4} className="py-6 text-center text-muted-foreground">
                  {t("proxyHub.empty")}
                </td>
              </tr>
            ) : (
              (accounts.data?.accounts ?? []).map((a) => (
                <tr key={a.id} className="border-b border-border/50">
                  <td className="py-2">{a.label}</td>
                  <td className="font-mono text-xs uppercase text-muted-foreground">{a.provider}</td>
                  <td className="font-mono text-xs text-muted-foreground">{formatTimestamp(a.created_at)}</td>
                  <td className="space-x-2 rtl:space-x-reverse">
                    <button
                      type="button"
                      className="text-xs text-primary hover:underline"
                      onClick={() => setProvisionAccount(a)}
                    >
                      {t("proxyHub.provisionOutbound")}
                    </button>
                    <button
                      type="button"
                      className="text-xs text-destructive hover:underline"
                      onClick={() => setPendingDelete(a)}
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
        title={t("proxyHub.confirmDelete")}
        description={pendingDelete?.label}
        confirmLabel={t("delete")}
        pending={remove.isPending}
        onConfirm={() => pendingDelete && remove.mutate(pendingDelete.id)}
      />
      <MutationError error={remove.error} />
    </div>
  );
}

function RegisterForm({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [provider, setProvider] = useState<Provider>("warp");
  const [warpLabel, setWarpLabel] = useState("");
  const [nordLabel, setNordLabel] = useState("");
  const [nordToken, setNordToken] = useState("");

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ["proxy-providers"] });

  const registerWarp = useMutation({
    mutationFn: () => api.post("/api/v1/proxy-providers/warp/register", { label: warpLabel }),
    onSuccess: () => {
      setWarpLabel("");
      invalidate();
      onClose();
    },
  });

  const registerNordVPN = useMutation({
    mutationFn: () =>
      api.post("/api/v1/proxy-providers/nordvpn/register", { label: nordLabel, token: nordToken }),
    onSuccess: () => {
      setNordLabel("");
      setNordToken("");
      invalidate();
      onClose();
    },
  });

  return (
    <Tabs value={provider} onValueChange={(v) => setProvider(v as Provider)}>
      <TabsList>
        <TabsTrigger value="warp">{t("proxyHub.warp")}</TabsTrigger>
        <TabsTrigger value="nordvpn">{t("proxyHub.nordvpn")}</TabsTrigger>
      </TabsList>

      <TabsContent value="warp" className="space-y-3">
        <p className="text-xs text-muted-foreground">{t("proxyHub.warpHint")}</p>
        <Field id="warp-label" label={t("proxyHub.label")} value={warpLabel} onChange={setWarpLabel} />
        <MutationError error={registerWarp.error} />
        <div className="flex gap-2">
          <Button size="sm" disabled={registerWarp.isPending} onClick={() => registerWarp.mutate()}>
            {registerWarp.isPending ? t("proxyHub.registering") : t("proxyHub.registerAccount")}
          </Button>
          <Button variant="outline" size="sm" onClick={onClose}>
            {t("cancel")}
          </Button>
        </div>
      </TabsContent>

      <TabsContent value="nordvpn" className="space-y-3">
        <p className="text-xs text-muted-foreground">{t("proxyHub.nordvpnHint")}</p>
        <Field id="nordvpn-label" label={t("proxyHub.label")} value={nordLabel} onChange={setNordLabel} />
        <Field id="nordvpn-token" label={t("proxyHub.token")} value={nordToken} onChange={setNordToken} />
        <MutationError error={registerNordVPN.error} />
        <div className="flex gap-2">
          <Button
            size="sm"
            disabled={nordToken.trim() === "" || registerNordVPN.isPending}
            onClick={() => registerNordVPN.mutate()}
          >
            {registerNordVPN.isPending ? t("proxyHub.registering") : t("proxyHub.registerAccount")}
          </Button>
          <Button variant="outline" size="sm" onClick={onClose}>
            {t("cancel")}
          </Button>
        </div>
      </TabsContent>
    </Tabs>
  );
}

function ProvisionForm({
  account,
  onClose,
}: {
  account: ProxyProviderAccount;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const nodes = useQuery({
    queryKey: ["nodes"],
    queryFn: () => api.get<{ nodes: NodeOption[] }>("/api/v1/nodes"),
  });
  const [nodeId, setNodeId] = useState("");
  const [tag, setTag] = useState("");

  // NordVPN only: narrowing country -> server picks the station/public_key
  // the outbound needs. WARP needs neither -- its peer comes from Cloudflare
  // fresh at provisioning time, not from anything picked here.
  const [countryId, setCountryId] = useState("");
  const [serverId, setServerId] = useState("");
  const [localAddress, setLocalAddress] = useState("");

  const countries = useQuery({
    queryKey: ["proxy-providers", "nordvpn", "countries"],
    queryFn: () => api.get<{ countries: NordVPNCountry[] }>("/api/v1/proxy-providers/nordvpn/countries"),
    enabled: account.provider === "nordvpn",
  });
  const servers = useQuery({
    queryKey: ["proxy-providers", "nordvpn", "servers", countryId],
    queryFn: () =>
      api.get<{ servers: NordVPNServer[] }>(
        `/api/v1/proxy-providers/nordvpn/servers?country_id=${countryId}`,
      ),
    enabled: account.provider === "nordvpn" && countryId !== "",
  });
  const selectedServer = (servers.data?.servers ?? []).find((s) => String(s.id) === serverId);

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ["egress", Number(nodeId)] });
    queryClient.invalidateQueries({ queryKey: ["node", Number(nodeId)] });
  };

  const provisionWarp = useMutation({
    mutationFn: () =>
      api.post(`/api/v1/proxy-providers/${account.id}/warp/outbound`, {
        node_id: Number(nodeId),
        tag,
      }),
    onSuccess: () => {
      invalidate();
      onClose();
    },
  });

  const provisionNordVPN = useMutation({
    mutationFn: () =>
      api.post(`/api/v1/proxy-providers/${account.id}/nordvpn/outbound`, {
        node_id: Number(nodeId),
        tag,
        station: selectedServer?.station ?? "",
        public_key: selectedServer?.public_key ?? "",
        local_address: localAddress,
      }),
    onSuccess: () => {
      invalidate();
      onClose();
    },
  });

  const mutation = account.provider === "warp" ? provisionWarp : provisionNordVPN;
  const canSubmit =
    nodeId !== "" &&
    tag.trim() !== "" &&
    (account.provider === "warp" || selectedServer !== undefined);

  return (
    <div className="space-y-3">
      <div>
        <label className="block text-xs text-muted-foreground" htmlFor="provision-node">
          {t("proxyHub.node")}
        </label>
        <select
          id="provision-node"
          value={nodeId}
          onChange={(e) => setNodeId(e.target.value)}
          className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
        >
          <option value="">{t("proxyHub.chooseNode")}</option>
          {(nodes.data?.nodes ?? []).map((n) => (
            <option key={n.id} value={n.id}>
              {n.name}
            </option>
          ))}
        </select>
      </div>
      <Field id="provision-tag" label={t("egress.tag")} value={tag} onChange={setTag} />

      {account.provider === "nordvpn" && (
        <>
          <div>
            <label className="block text-xs text-muted-foreground" htmlFor="provision-country">
              {t("proxyHub.country")}
            </label>
            <select
              id="provision-country"
              value={countryId}
              onChange={(e) => {
                setCountryId(e.target.value);
                setServerId("");
              }}
              className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
            >
              <option value="">{t("proxyHub.chooseCountry")}</option>
              {(countries.data?.countries ?? []).map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </div>
          <div>
            <label className="block text-xs text-muted-foreground" htmlFor="provision-server">
              {t("proxyHub.server")}
            </label>
            <select
              id="provision-server"
              value={serverId}
              onChange={(e) => setServerId(e.target.value)}
              disabled={countryId === ""}
              className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm disabled:opacity-50"
            >
              <option value="">{t("proxyHub.chooseServer")}</option>
              {(servers.data?.servers ?? []).map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name} · {t("proxyHub.load")} {s.load}%
                </option>
              ))}
            </select>
          </div>
          <Field
            id="provision-local-address"
            label={t("proxyHub.localAddressOverride")}
            value={localAddress}
            onChange={setLocalAddress}
          />
        </>
      )}

      <MutationError error={mutation.error} />
      <div className="flex gap-2">
        <Button size="sm" disabled={!canSubmit || mutation.isPending} onClick={() => mutation.mutate()}>
          {mutation.isPending ? t("egress.saving") : t("create")}
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
