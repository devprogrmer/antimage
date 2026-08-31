import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../lib/api";
import { MutationError } from "../routes/Resellers";
import { SchemaForm } from "./SchemaForm";
import { ConfirmDialog } from "./ConfirmDialog";
import { Button } from "./ui/button";
import type { JSONSchema, Params } from "./SchemaForm";
import { formatNumber, formatTimestamp, t } from "../i18n";

interface ServiceSchema {
  kind: string;
  version: string;
  schema: JSONSchema;
  offerable: boolean;
  reason?: string;
  hot_user_add: boolean;
  requires_pki: boolean;
}

interface Service {
  id: number;
  node_id: number;
  adapter_kind: string;
  params: Params;
  enabled: boolean;
  created_at: number;
}

/**
 * InboundStudio edits a node's inbounds from the schemas that node publishes.
 *
 * Every protocol offered here is one the node reported at Hello, so the editor
 * cannot offer something the host has no adapter for. That is why the protocol
 * list is not a hardcoded dropdown: an editor that offers a protocol whose
 * adapter cannot apply it is a fake feature layer, and taking the list from the
 * node is what prevents it. Adding an adapter to a node adds its protocol here,
 * with its own fields, without a line of panel code.
 */
export function InboundStudio({ nodeId }: { nodeId: number }) {
  // The form doubles as create, edit and clone. `editing` carries the service
  // being changed; `draft` carries starting params, which is how clone works --
  // a clone is a create whose fields arrive filled in.
  const [form, setForm] = useState<
    { mode: "create" | "edit" | "clone"; service?: Service } | null
  >(null);
  const [selected, setSelected] = useState<Set<number>>(new Set());

  const schemas = useQuery({
    queryKey: ["node", nodeId, "service-schemas"],
    queryFn: () =>
      api.get<{ adapters: ServiceSchema[] }>(`/api/v1/nodes/${nodeId}/service-schemas`),
  });
  const services = useQuery({
    queryKey: ["node", nodeId, "services"],
    queryFn: () => api.get<{ services: Service[] }>(`/api/v1/nodes/${nodeId}/services`),
  });

  const offerable = (schemas.data?.adapters ?? []).filter((a) => a.offerable);
  const unofferable = (schemas.data?.adapters ?? []).filter((a) => !a.offerable);
  const list = services.data?.services ?? [];

  return (
    <section className="rounded border border-border bg-card p-4">
      <div className="mb-3 flex items-center justify-between">
        <h3 className="text-sm font-semibold">{t("studio.title")}</h3>
        {offerable.length > 0 && form === null && (
          <Button size="sm" onClick={() => setForm({ mode: "create" })}>
            {t("studio.add")}
          </Button>
        )}
      </div>

      {schemas.isError && <MutationError error={schemas.error} />}
      {services.isError && <MutationError error={services.error} />}

      {/* A node that has never connected reports nothing. Saying so beats an
          empty form the operator cannot explain. */}
      {schemas.data !== undefined && schemas.data.adapters.length === 0 && (
        <p className="text-sm text-muted-foreground">{t("studio.nodeReportedNothing")}</p>
      )}

      {/* A protocol the node runs but cannot describe is named, not hidden:
          otherwise an operator sees WireGuard running and no way to add one,
          with nothing to explain the gap. */}
      {unofferable.map((a) => (
        <p key={a.kind} className="mb-2 text-xs text-warning" role="status">
          {a.reason ?? t("studio.notOfferable")}
        </p>
      ))}

      {form !== null && (
        <InboundForm
          nodeId={nodeId}
          adapters={offerable}
          mode={form.mode}
          source={form.service}
          onClose={() => setForm(null)}
        />
      )}

      <BulkBar
        nodeId={nodeId}
        services={list.filter((s) => selected.has(s.id))}
        onDone={() => setSelected(new Set())}
      />

      <ServiceList
        nodeId={nodeId}
        services={list}
        selected={selected}
        onSelectedChange={setSelected}
        onEdit={(svc) => setForm({ mode: "edit", service: svc })}
        onClone={(svc) => setForm({ mode: "clone", service: svc })}
      />
    </section>
  );
}

/** The body PUT and POST both take. */
function serviceBody(svc: { adapter_kind: string; params: Params; enabled: boolean }) {
  return { adapter_kind: svc.adapter_kind, params: svc.params, enabled: svc.enabled };
}

function ServiceList({
  nodeId,
  services,
  selected,
  onSelectedChange,
  onEdit,
  onClone,
}: {
  nodeId: number;
  services: Service[];
  selected: Set<number>;
  onSelectedChange: (next: Set<number>) => void;
  onEdit: (svc: Service) => void;
  onClone: (svc: Service) => void;
}) {
  const queryClient = useQueryClient();
  // Which inbound the operator is being asked about. The dialog names the
  // protocol it is about to remove: a bare "are you sure" beside a table of six
  // inbounds is a question nobody can answer safely.
  const [pending, setPending] = useState<Service | null>(null);

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ["node", nodeId] });

  const remove = useMutation({
    mutationFn: (id: number) => api.del(`/api/v1/services/${id}`),
    onSuccess: () => {
      invalidate();
      setPending(null);
    },
  });

  // Enable and disable are one PUT with the flag flipped. It goes through
  // CommitNodeChange like any other edit, so disabling an inbound republishes
  // the node rather than only changing a row nobody acts on.
  const toggle = useMutation({
    mutationFn: (svc: Service) =>
      api.put(`/api/v1/services/${svc.id}`, serviceBody({ ...svc, enabled: !svc.enabled })),
    onSuccess: invalidate,
  });

  if (services.length === 0) {
    return <p className="text-sm text-muted-foreground">{t("studio.noInbounds")}</p>;
  }

  const allSelected = services.every((s) => selected.has(s.id));

  return (
    <>
      <table className="w-full border-collapse text-sm text-foreground">
        <caption className="sr-only">{t("studio.title")}</caption>
        <thead>
          <tr className="border-b border-border text-xs uppercase tracking-wide text-muted-foreground">
            <th className="w-8 py-2 pe-3 text-start">
              <input
                type="checkbox"
                checked={allSelected}
                ref={(el) => {
                  if (el) {
                    el.indeterminate =
                      !allSelected && services.some((s) => selected.has(s.id));
                  }
                }}
                onChange={() =>
                  onSelectedChange(
                    allSelected ? new Set() : new Set(services.map((s) => s.id)),
                  )
                }
                aria-label={t("table.selectAll")}
                className="size-4 cursor-pointer accent-primary"
              />
            </th>
            <th className="py-2 pe-3 text-start">{t("studio.protocol")}</th>
            <th className="pe-3 text-start">{t("studio.status")}</th>
            <th className="pe-3 text-start">{t("studio.params")}</th>
            <th className="pe-3 text-start">{t("reseller.created")}</th>
            <th className="text-start">{t("actions")}</th>
          </tr>
        </thead>
        <tbody>
          {services.map((svc) => (
            <tr key={svc.id} className="border-b border-border align-top">
              <td className="py-1.5 pe-3">
                <input
                  type="checkbox"
                  checked={selected.has(svc.id)}
                  onChange={() => {
                    const next = new Set(selected);
                    if (next.has(svc.id)) next.delete(svc.id);
                    else next.add(svc.id);
                    onSelectedChange(next);
                  }}
                  aria-label={`${svc.adapter_kind} ${svc.id}`}
                  className="size-4 cursor-pointer accent-primary"
                />
              </td>
              <td className="py-1.5 pe-3 font-mono">{svc.adapter_kind}</td>
              <td className="pe-3">
                {svc.enabled ? (
                  <span className="text-success">{t("reseller.enabled")}</span>
                ) : (
                  <span className="text-muted-foreground">{t("reseller.disabled")}</span>
                )}
              </td>
              <td className="pe-3">
                <code className="block max-w-md overflow-x-auto whitespace-pre text-xs text-muted-foreground">
                  {JSON.stringify(svc.params, null, 2)}
                </code>
              </td>
              <td className="pe-3 font-mono text-xs text-muted-foreground">
                {formatTimestamp(svc.created_at)}
              </td>
              <td>
                <div className="flex flex-wrap gap-2 text-xs">
                  <button
                    type="button"
                    onClick={() => onEdit(svc)}
                    className="text-primary hover:underline"
                  >
                    {t("edit")}
                  </button>
                  {/* Clone opens the form PRE-FILLED rather than posting a copy
                      straight away. Two inbounds of the same protocol almost
                      always collide on the listen port, so a blind clone would
                      be a request that is refused nearly every time -- the
                      operator needs to change something first. */}
                  <button
                    type="button"
                    onClick={() => onClone(svc)}
                    className="text-primary hover:underline"
                  >
                    {t("studio.clone")}
                  </button>
                  <button
                    type="button"
                    onClick={() => toggle.mutate(svc)}
                    disabled={toggle.isPending}
                    className="text-primary hover:underline disabled:opacity-50"
                  >
                    {svc.enabled ? t("studio.disable") : t("studio.enable")}
                  </button>
                  <button
                    type="button"
                    onClick={() => setPending(svc)}
                    className="text-destructive hover:underline"
                  >
                    {t("delete")}
                  </button>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <MutationError error={remove.error} />
      <MutationError error={toggle.error} />
      <ConfirmDialog
        open={pending !== null}
        onOpenChange={(open) => !open && setPending(null)}
        title={t("studio.confirmDelete")}
        // The title already states the disruption ('Everyone connected through
        // it is disconnected'), so the description names WHICH inbound --
        // the question the yes/no box could not answer beside a table of six.
        description={pending?.adapter_kind}
        confirmLabel={t("delete")}
        pending={remove.isPending}
        onConfirm={() => pending && remove.mutate(pending.id)}
      />
    </>
  );
}

/**
 * Bulk enable, disable and delete over the selected inbounds.
 *
 * Applied one request at a time rather than through a bulk endpoint, because
 * there is no bulk service endpoint and inventing one would put a second write
 * path beside CommitNodeChange. Each request republishes the node, which is
 * correct and is also why the count is reported: a batch where three of five
 * succeeded is the normal outcome when one inbound has a stale port, and
 * "done" would hide it.
 */
function BulkBar({
  nodeId,
  services,
  onDone,
}: {
  nodeId: number;
  services: Service[];
  onDone: () => void;
}) {
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const [result, setResult] = useState<{ ok: number; failed: number; first?: string } | null>(
    null,
  );

  const run = useMutation({
    mutationFn: async (action: "enable" | "disable" | "delete") => {
      let ok = 0;
      let failed = 0;
      let first: string | undefined;
      for (const svc of services) {
        try {
          if (action === "delete") {
            await api.del(`/api/v1/services/${svc.id}`);
          } else {
            await api.put(
              `/api/v1/services/${svc.id}`,
              serviceBody({ ...svc, enabled: action === "enable" }),
            );
          }
          ok++;
        } catch (err) {
          failed++;
          if (first === undefined) first = err instanceof Error ? err.message : String(err);
        }
      }
      return { ok, failed, first };
    },
    onSuccess: (r) => {
      setResult(r);
      setConfirming(false);
      queryClient.invalidateQueries({ queryKey: ["node", nodeId] });
      onDone();
    },
  });

  const count = services.length;
  if (count === 0 && result === null) return null;

  return (
    <div className="mb-2 flex flex-wrap items-center gap-2 rounded border border-border bg-background p-2">
      {count > 0 && (
        <>
          <span className="text-xs">
            {t("bulk.selected", { count: formatNumber(count) })}
          </span>
          <Button size="sm" variant="outline" disabled={run.isPending}
            onClick={() => run.mutate("enable")}>
            {t("studio.enable")}
          </Button>
          <Button size="sm" variant="outline" disabled={run.isPending}
            onClick={() => run.mutate("disable")}>
            {t("studio.disable")}
          </Button>
          <Button size="sm" variant="outline" disabled={run.isPending}
            onClick={() => setConfirming(true)}>
            {t("delete")}
          </Button>
          <Button size="sm" variant="ghost" className="ms-auto"
            onClick={() => { setResult(null); onDone(); }}>
            {t("bulk.clearSelection")}
          </Button>
        </>
      )}
      {result && (
        <p
          role="status"
          className={result.failed > 0 ? "w-full text-xs text-warning" : "w-full text-xs text-success"}
        >
          {t("bulk.result", {
            changed: formatNumber(result.ok),
            failed: formatNumber(result.failed),
          })}
          {result.first && <span className="ms-2 text-muted-foreground">{result.first}</span>}
        </p>
      )}
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title={t("studio.confirmDelete")}
        description={t("studio.bulkAffects", { count: formatNumber(count) })}
        confirmLabel={t("delete")}
        pending={run.isPending}
        onConfirm={() => run.mutate("delete")}
      />
    </div>
  );
}

/**
 * InboundForm creates, edits or clones one inbound from an adapter's schema.
 *
 * One component for all three because they differ only in where the starting
 * params come from and which request is sent. Three near-identical forms would
 * be three places for the JSON/form toggle to drift.
 *
 * JSON mode is not a way around validation. There is deliberately no second
 * validator in the browser: the panel validates every submission against the
 * same schema the form was built from, using the node's copy of it, and a
 * second implementation here could only drift from that one. So JSON mode
 * checks that the text parses and then submits, and whatever the control plane
 * says is shown verbatim. Raw editing takes exactly the same path as the form.
 */
function InboundForm({
  nodeId,
  adapters,
  mode,
  source,
  onClose,
}: {
  nodeId: number;
  adapters: ServiceSchema[];
  mode: "create" | "edit" | "clone";
  source?: Service;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [kind, setKind] = useState(source?.adapter_kind ?? adapters[0]?.kind ?? "");
  const [params, setParams] = useState<Params>(source?.params ?? {});
  const [jsonMode, setJsonMode] = useState(false);
  const [jsonText, setJsonText] = useState(
    source ? JSON.stringify(source.params, null, 2) : "{}",
  );
  const [jsonError, setJsonError] = useState("");

  const adapter = adapters.find((a) => a.kind === kind);

  const save = useMutation({
    mutationFn: (body: { adapter_kind: string; params: Params; enabled?: boolean }) =>
      mode === "edit" && source
        ? api.put(`/api/v1/services/${source.id}`, body)
        : api.post(`/api/v1/nodes/${nodeId}/services`, body),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["node", nodeId] });
      onClose();
    },
  });

  /** Moving between modes carries the document across, so nothing is retyped. */
  function toJSON() {
    setJsonText(JSON.stringify(params, null, 2));
    setJsonError("");
    setJsonMode(true);
  }
  function toForm() {
    try {
      setParams(JSON.parse(jsonText) as Params);
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
        // Only a SYNTAX check. Whether the document satisfies the schema is
        // the panel's answer, and it is the one that counts.
        setJsonError(err instanceof Error ? err.message : String(err));
        return;
      }
    }
    setJsonError("");
    save.mutate({
      adapter_kind: kind,
      params: body,
      // An edit must carry the enabled flag through: the update handler
      // rewrites the whole row, and omitting it would silently re-enable an
      // inbound the operator had turned off.
      ...(mode === "edit" && source ? { enabled: source.enabled } : {}),
    });
  }

  const heading =
    mode === "edit" ? t("studio.editInbound")
      : mode === "clone" ? t("studio.cloneInbound")
        : t("studio.addNew");

  return (
    <div className="mb-4 rounded border border-border bg-background p-4">
      <div className="mb-3 flex items-center justify-between">
        <h4 className="text-sm font-semibold">{heading}</h4>
        <button
          type="button"
          onClick={() => (jsonMode ? toForm() : toJSON())}
          className="text-xs text-muted-foreground hover:text-foreground"
        >
          {jsonMode ? t("studio.switchToForm") : t("studio.switchToJson")}
        </button>
      </div>

      {mode === "clone" && (
        <p className="mb-2 text-xs text-muted-foreground">{t("studio.cloneHint")}</p>
      )}

      <div className="mb-3">
        <label className="block text-xs text-muted-foreground" htmlFor="studio-kind">
          {t("studio.protocol")}
        </label>
        <select
          id="studio-kind"
          value={kind}
          // The protocol of an existing inbound is fixed. Changing it would
          // keep the id and swap the adapter under it, which is a different
          // inbound wearing the old one's identity -- delete and create, or
          // clone, are the honest ways to do that.
          disabled={mode === "edit"}
          onChange={(e) => {
            setKind(e.target.value);
            // Params belong to the protocol that was selected. Carrying them
            // across would submit one adapter's fields to another, which the
            // schema refuses and which reads as a bug rather than a mistake.
            setParams({});
            setJsonText("{}");
          }}
          className="w-full rounded border border-input bg-background px-2 py-1 text-sm disabled:opacity-60"
        >
          {adapters.map((a) => (
            <option key={a.kind} value={a.kind}>
              {a.kind}
            </option>
          ))}
        </select>
        {adapter?.requires_pki && (
          <p className="mt-1 text-xs text-warning">{t("studio.requiresPki")}</p>
        )}
        {adapter && !adapter.hot_user_add && (
          <p className="mt-1 text-xs text-muted-foreground">{t("studio.noHotAdd")}</p>
        )}
      </div>

      {jsonMode ? (
        <div>
          <label className="block text-xs text-muted-foreground" htmlFor="studio-json">
            {t("studio.paramsDocument")}
          </label>
          <textarea
            id="studio-json"
            value={jsonText}
            onChange={(e) => setJsonText(e.target.value)}
            rows={12}
            spellCheck={false}
            className="w-full rounded border border-input bg-background px-2 py-1 font-mono text-xs"
          />
          <p className="mt-1 text-xs text-muted-foreground">{t("studio.jsonModeNote")}</p>
          {jsonError !== "" && (
            <p className="mt-1 text-xs text-destructive" role="alert">
              {jsonError}
            </p>
          )}
        </div>
      ) : (
        adapter && <SchemaForm schema={adapter.schema} value={params} onChange={setParams} />
      )}

      <MutationError error={save.error} />

      <div className="mt-3 flex gap-2">
        <Button size="sm" onClick={submit} disabled={kind === "" || save.isPending}>
          {mode === "edit" ? t("save") : t("create")}
        </Button>
        <Button size="sm" variant="outline" onClick={onClose}>
          {t("cancel")}
        </Button>
      </div>
    </div>
  );
}
