import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "../lib/api";
import { t } from "../i18n";
import { SchemaForm } from "./SchemaForm";
import type { JSONSchema, Params } from "./SchemaForm";
import { ConfirmDialog } from "./ConfirmDialog";
import { ChevronDown, ChevronRight, Info, AlertCircle } from "lucide-react";

interface AdapterInfo {
  kind: string;
  version: string;
  capabilities: string[];
  service_schema?: JSONSchema;
  requires_pki: boolean;
  hot_user_add: boolean;
}

interface Service {
  id: number;
  node_id: number;
  adapter_kind: string;
  params: Params;
  enabled: boolean;
  created_at: number;
}

/** Renders a server refusal with field-level attribution when available. */
function MutationError({ error }: { error: unknown }) {
  if (!error) return null;
  const message = error instanceof ApiError ? error.message : String(error);
  return (
    <div className="mt-2 rounded border border-destructive/50 bg-destructive/10 px-3 py-2" role="alert">
      <div className="flex items-start gap-2">
        <AlertCircle className="mt-0.5 size-4 shrink-0 text-destructive" />
        <p className="text-xs text-destructive">{message}</p>
      </div>
    </div>
  );
}

/** Protocol-specific help text and examples derived from adapter schemas. */
const ADAPTER_HELP: Record<string, { description: string; examples: string[] }> = {
  xray: {
    description: "Xray-core multiplexing proxy. Supports VLESS, VMess, and Trojan protocols with TLS and transport options (TCP, WebSocket, gRPC).",
    examples: [
      "VLESS + TLS + WebSocket: protocol=vless, port=443, network=ws, security=tls, path=/ws",
      "Trojan + TCP: protocol=trojan, port=8443, network=tcp, security=tls",
      "VMess + gRPC: protocol=vmess, port=443, network=grpc, security=tls, host=example.com"
    ]
  },
  wireguard: {
    description: "WireGuard VPN tunnel. Requires port, subnet (CIDR), and private key. Supports hot peer addition without restarting.",
    examples: [
      "Basic tunnel: port=51820, subnet=10.8.0.1/24, private_key=<base64>",
      "Custom DNS: port=51820, subnet=10.8.0.1/24, private_key=<base64>, dns=[\"1.1.1.1\", \"8.8.8.8\"]",
      "Custom MTU: port=51820, subnet=10.8.0.1/24, private_key=<base64>, mtu=1380"
    ]
  },
  hysteria2: {
    description: "Hysteria2 high-performance proxy protocol. Requires TLS certificates. Supports bandwidth control and masquerading.",
    examples: [
      "Basic config: port=443, password=<strong>, cert_file=/path/to/cert.pem, key_file=/path/to/key.pem",
      "With bandwidth limits: port=443, password=<strong>, cert_file=/path, key_file=/path, up_mbps=100, down_mbps=500",
      "With obfuscation: port=443, password=<strong>, cert_file=/path, key_file=/path, obfs=salamander, obfs_password=<obfs_pass>"
    ]
  },
  l2tp: {
    description: "L2TP/IPsec VPN using strongSwan and xl2tpd. Requires IP range, local IP, and pre-shared key. Supports credential reload without dropping tunnels.",
    examples: [
      "Basic config: ip_range=10.8.0.2-10.8.0.254, local_ip=10.8.0.1, psk=<16+ chars>",
      "With DNS: ip_range=10.8.0.2-10.8.0.254, local_ip=10.8.0.1, psk=<strong>, dns_servers=[\"8.8.8.8\", \"8.8.4.4\"]"
    ]
  },
  singbox: {
    description: "sing-box universal proxy platform. Supports VLESS, VMess, Trojan, and Shadowsocks. Full restart required for config changes.",
    examples: [
      "VLESS + TCP: protocol=vless, port=443, network=tcp, tls=true",
      "Shadowsocks: protocol=shadowsocks, port=8388, method=aes-256-gcm",
      "VMess + WebSocket: protocol=vmess, port=443, network=ws, tls=true, path=/vmess, host=example.com"
    ]
  }
};

/** Collapsible section for grouping related controls. */
function CollapsibleSection({
  title,
  defaultOpen = false,
  children,
}: {
  title: string;
  defaultOpen?: boolean;
  children: React.ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <div className="rounded border border-border bg-card">
      <button
        type="button"
        onClick={() => setOpen(!open)}
        className="flex w-full items-center gap-2 px-3 py-2 text-start text-sm font-medium hover:bg-accent"
      >
        {open ? (
          <ChevronDown className="size-4 text-muted-foreground" />
        ) : (
          <ChevronRight className="size-4 text-muted-foreground" />
        )}
        <span>{title}</span>
      </button>
      {open && <div className="border-t border-border px-3 py-3">{children}</div>}
    </div>
  );
}

/** Protocol-specific help panel with description and examples. */
function ProtocolHelp({ adapterKind }: { adapterKind: string }) {
  const help = ADAPTER_HELP[adapterKind];
  if (!help) return null;

  return (
    <div className="rounded border border-info/50 bg-info/10 px-3 py-2">
      <div className="mb-2 flex items-start gap-2">
        <Info className="mt-0.5 size-4 shrink-0 text-info" />
        <p className="text-xs text-foreground">{help.description}</p>
      </div>
      <div className="ms-6 space-y-1">
        <p className="text-xs font-medium text-muted-foreground">{t("adapter.examples")}:</p>
        {help.examples.map((example, idx) => (
          <p key={idx} className="font-mono text-xs text-muted-foreground">
            • {example}
          </p>
        ))}
      </div>
    </div>
  );
}

/** Main adapter configuration panel - discovers adapters and renders config UI. */
export function AdapterConfigPanel({ nodeId }: { nodeId: number }) {
  const queryClient = useQueryClient();

  // Fetch adapters the node reported at Hello
  const adapters = useQuery({
    queryKey: ["node", nodeId, "adapters"],
    queryFn: () => api.get<{ adapters: AdapterInfo[] }>(`/api/v1/nodes/${nodeId}/adapters`),
  });

  // Fetch existing service configurations
  const services = useQuery({
    queryKey: ["node", nodeId, "services"],
    queryFn: () => api.get<{ services: Service[] }>(`/api/v1/nodes/${nodeId}/services`),
  });

  const [adding, setAdding] = useState(false);
  const [editingService, setEditingService] = useState<Service | null>(null);

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ["node", nodeId] });
  };

  if (adapters.isLoading) {
    return <p className="text-sm text-muted-foreground">{t("loading")}</p>;
  }

  if (adapters.isError) {
    return <MutationError error={adapters.error} />;
  }

  // Node that has never connected reports nothing
  if (adapters.data?.adapters.length === 0) {
    return (
      <section className="rounded border border-border bg-card p-4">
        <h3 className="mb-2 text-sm font-semibold">{t("adapter.configuration")}</h3>
        <p className="text-sm text-muted-foreground">{t("adapter.nodeNotConnected")}</p>
      </section>
    );
  }

  return (
    <section className="space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold">{t("adapter.configuration")}</h3>
        {!adding && (
          <button
            type="button"
            onClick={() => setAdding(true)}
            className="rounded bg-primary px-3 py-1.5 text-sm hover:bg-primary/90"
          >
            {t("adapter.addService")}
          </button>
        )}
      </div>

      {adding && (
        <AddServiceForm
          nodeId={nodeId}
          adapters={adapters.data?.adapters ?? []}
          onClose={() => setAdding(false)}
          onSuccess={() => {
            setAdding(false);
            invalidate();
          }}
        />
      )}

      {editingService && (
        <EditServiceForm
          service={editingService}
          adapters={adapters.data?.adapters ?? []}
          onClose={() => setEditingService(null)}
          onSuccess={() => {
            setEditingService(null);
            invalidate();
          }}
        />
      )}

      <ServiceList
        services={services.data?.services ?? []}
        onEdit={setEditingService}
        onDelete={invalidate}
      />
    </section>
  );
}

/** Form for adding a new service configuration. */
function AddServiceForm({
  nodeId,
  adapters,
  onClose,
  onSuccess,
}: {
  nodeId: number;
  adapters: AdapterInfo[];
  onClose: () => void;
  onSuccess: () => void;
}) {
  const [selectedKind, setSelectedKind] = useState(adapters[0]?.kind ?? "");
  const [params, setParams] = useState<Params>({});
  const [jsonMode, setJsonMode] = useState(false);
  const [jsonText, setJsonText] = useState("{}");
  const [jsonError, setJsonError] = useState("");

  const adapter = adapters.find((a) => a.kind === selectedKind);

  const create = useMutation({
    mutationFn: (body: { adapter_kind: string; params: Params }) =>
      api.post(`/api/v1/nodes/${nodeId}/services`, body),
    onSuccess,
  });

  function toJSON() {
    setJsonText(JSON.stringify(params, null, 2));
    setJsonError("");
    setJsonMode(true);
  }

  function toForm() {
    try {
      const parsed = JSON.parse(jsonText) as Params;
      setParams(parsed);
      setJsonError("");
      setJsonMode(false);
    } catch (err) {
      setJsonError(err instanceof Error ? err.message : String(err));
    }
  }

  function submit() {
    let body = params;
    if (jsonMode) {
      try {
        body = JSON.parse(jsonText) as Params;
      } catch (err) {
        setJsonError(err instanceof Error ? err.message : String(err));
        return;
      }
    }
    setJsonError("");
    create.mutate({ adapter_kind: selectedKind, params: body });
  }

  return (
    <div className="rounded border border-border bg-card p-4">
      <div className="mb-4 flex items-center justify-between">
        <h4 className="text-sm font-semibold">{t("adapter.addService")}</h4>
        <button
          type="button"
          onClick={() => (jsonMode ? toForm() : toJSON())}
          className="text-xs text-muted-foreground hover:text-foreground"
        >
          {jsonMode ? t("studio.switchToForm") : t("studio.switchToJson")}
        </button>
      </div>

      <div className="mb-4 space-y-4">
        <div>
          <label className="block text-xs text-muted-foreground" htmlFor="adapter-kind">
            {t("adapter.protocol")}
          </label>
          <select
            id="adapter-kind"
            value={selectedKind}
            onChange={(e) => {
              setSelectedKind(e.target.value);
              setParams({});
              setJsonText("{}");
            }}
            className="w-full rounded border border-input bg-background px-2 py-1.5 text-sm"
          >
            {adapters.map((a) => (
              <option key={a.kind} value={a.kind}>
                {a.kind} (v{a.version})
              </option>
            ))}
          </select>
        </div>

        {adapter && (
          <div className="space-y-2">
            {adapter.requires_pki && (
              <div className="rounded border border-warning/50 bg-warning/10 px-3 py-2">
                <p className="text-xs text-warning">{t("adapter.requiresPKI")}</p>
              </div>
            )}
            {!adapter.hot_user_add && (
              <div className="rounded border border-info/50 bg-info/10 px-3 py-2">
                <p className="text-xs text-foreground">{t("adapter.noHotAdd")}</p>
              </div>
            )}
          </div>
        )}

        <ProtocolHelp adapterKind={selectedKind} />
      </div>

      {jsonMode ? (
        <div className="mb-4">
          <label className="block text-xs text-muted-foreground" htmlFor="params-json">
            {t("adapter.paramsDocument")}
          </label>
          <textarea
            id="params-json"
            value={jsonText}
            onChange={(e) => setJsonText(e.target.value)}
            rows={16}
            spellCheck={false}
            className="w-full rounded border border-input bg-background px-2 py-1.5 font-mono text-xs"
          />
          {jsonError && (
            <p className="mt-1 text-xs text-destructive" role="alert">
              {jsonError}
            </p>
          )}
        </div>
      ) : (
        adapter?.service_schema && (
          <div className="mb-4">
            <SchemaForm
              schema={adapter.service_schema}
              value={params}
              onChange={setParams}
            />
          </div>
        )
      )}

      <MutationError error={create.error} />

      <div className="flex gap-2">
        <button
          type="button"
          onClick={submit}
          disabled={!selectedKind || create.isPending}
          className="rounded bg-primary px-4 py-1.5 text-sm hover:bg-primary/90 disabled:opacity-50"
        >
          {create.isPending ? t("saving") : t("create")}
        </button>
        <button
          type="button"
          onClick={onClose}
          className="rounded bg-secondary px-4 py-1.5 text-sm hover:bg-secondary/80"
        >
          {t("cancel")}
        </button>
      </div>
    </div>
  );
}

/** Form for editing an existing service configuration. */
function EditServiceForm({
  service,
  adapters,
  onClose,
  onSuccess,
}: {
  service: Service;
  adapters: AdapterInfo[];
  onClose: () => void;
  onSuccess: () => void;
}) {
  const [params, setParams] = useState<Params>(service.params);
  const [jsonMode, setJsonMode] = useState(false);
  const [jsonText, setJsonText] = useState(JSON.stringify(service.params, null, 2));
  const [jsonError, setJsonError] = useState("");

  const adapter = adapters.find((a) => a.kind === service.adapter_kind);

  const update = useMutation({
    mutationFn: (body: { params: Params }) =>
      api.put(`/api/v1/services/${service.id}`, body),
    onSuccess,
  });

  function toJSON() {
    setJsonText(JSON.stringify(params, null, 2));
    setJsonError("");
    setJsonMode(true);
  }

  function toForm() {
    try {
      const parsed = JSON.parse(jsonText) as Params;
      setParams(parsed);
      setJsonError("");
      setJsonMode(false);
    } catch (err) {
      setJsonError(err instanceof Error ? err.message : String(err));
    }
  }

  function submit() {
    let body = params;
    if (jsonMode) {
      try {
        body = JSON.parse(jsonText) as Params;
      } catch (err) {
        setJsonError(err instanceof Error ? err.message : String(err));
        return;
      }
    }
    setJsonError("");
    update.mutate({ params: body });
  }

  return (
    <div className="rounded border border-border bg-card p-4">
      <div className="mb-4 flex items-center justify-between">
        <h4 className="text-sm font-semibold">
          {t("adapter.editService")}: {service.adapter_kind}
        </h4>
        <button
          type="button"
          onClick={() => (jsonMode ? toForm() : toJSON())}
          className="text-xs text-muted-foreground hover:text-foreground"
        >
          {jsonMode ? t("studio.switchToForm") : t("studio.switchToJson")}
        </button>
      </div>

      <div className="mb-4">
        <ProtocolHelp adapterKind={service.adapter_kind} />
      </div>

      {jsonMode ? (
        <div className="mb-4">
          <label className="block text-xs text-muted-foreground" htmlFor="edit-params-json">
            {t("adapter.paramsDocument")}
          </label>
          <textarea
            id="edit-params-json"
            value={jsonText}
            onChange={(e) => setJsonText(e.target.value)}
            rows={16}
            spellCheck={false}
            className="w-full rounded border border-input bg-background px-2 py-1.5 font-mono text-xs"
          />
          {jsonError && (
            <p className="mt-1 text-xs text-destructive" role="alert">
              {jsonError}
            </p>
          )}
        </div>
      ) : (
        adapter?.service_schema && (
          <div className="mb-4">
            <SchemaForm
              schema={adapter.service_schema}
              value={params}
              onChange={setParams}
            />
          </div>
        )
      )}

      <MutationError error={update.error} />

      <div className="flex gap-2">
        <button
          type="button"
          onClick={submit}
          disabled={update.isPending}
          className="rounded bg-primary px-4 py-1.5 text-sm hover:bg-primary/90 disabled:opacity-50"
        >
          {update.isPending ? t("saving") : t("save")}
        </button>
        <button
          type="button"
          onClick={onClose}
          className="rounded bg-secondary px-4 py-1.5 text-sm hover:bg-secondary/80"
        >
          {t("cancel")}
        </button>
      </div>
    </div>
  );
}

/** List of configured services with edit/delete actions. */
function ServiceList({
  services,
  onEdit,
  onDelete,
}: {
  services: Service[];
  onEdit: (service: Service) => void;
  onDelete: () => void;
}) {
  const [pendingDelete, setPendingDelete] = useState<Service | null>(null);

  const deleteMutation = useMutation({
    mutationFn: (id: number) => api.del(`/api/v1/services/${id}`),
    onSuccess: () => {
      setPendingDelete(null);
      onDelete();
    },
  });

  if (services.length === 0) {
    return (
      <div className="rounded border border-border bg-card p-4">
        <p className="text-sm text-muted-foreground">{t("adapter.noServices")}</p>
      </div>
    );
  }

  return (
    <>
      <div className="space-y-2">
        {services.map((svc) => (
          <CollapsibleSection key={svc.id} title={`${svc.adapter_kind} (ID: ${svc.id})`}>
            <div className="space-y-3">
              <div className="flex items-center gap-2">
                <span
                  className={`rounded px-2 py-0.5 text-xs font-medium ${
                    svc.enabled
                      ? "bg-success/20 text-success"
                      : "bg-muted text-muted-foreground"
                  }`}
                >
                  {svc.enabled ? t("adapter.enabled") : t("adapter.disabled")}
                </span>
              </div>
              <div>
                <p className="mb-1 text-xs text-muted-foreground">{t("adapter.configuration")}:</p>
                <pre className="overflow-x-auto rounded border border-border bg-background px-2 py-1.5 font-mono text-xs text-foreground">
                  {JSON.stringify(svc.params, null, 2)}
                </pre>
              </div>
              <div className="flex gap-2">
                <button
                  type="button"
                  onClick={() => onEdit(svc)}
                  className="rounded border border-input px-3 py-1 text-xs hover:bg-accent"
                >
                  {t("edit")}
                </button>
                <button
                  type="button"
                  onClick={() => setPendingDelete(svc)}
                  className="rounded border border-destructive px-3 py-1 text-xs text-destructive hover:bg-destructive/10"
                >
                  {t("delete")}
                </button>
              </div>
            </div>
          </CollapsibleSection>
        ))}
      </div>

      <ConfirmDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => !open && setPendingDelete(null)}
        title={t("adapter.confirmDelete")}
        description={`${pendingDelete?.adapter_kind} (ID: ${pendingDelete?.id})`}
        confirmLabel={t("delete")}
        pending={deleteMutation.isPending}
        onConfirm={() => pendingDelete && deleteMutation.mutate(pendingDelete.id)}
      />
      <MutationError error={deleteMutation.error} />
    </>
  );
}
