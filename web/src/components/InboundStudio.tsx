import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../lib/api";
import { MutationError } from "../routes/Resellers";
import { SchemaForm } from "./SchemaForm";
import { ConfirmDialog } from "./ConfirmDialog";
import type { JSONSchema, Params } from "./SchemaForm";
import { formatTimestamp, t } from "../i18n";

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
 * cannot offer something the host has no adapter for. That is the whole reason
 * this waited on the node work: an editor that offers a protocol whose adapter
 * cannot apply it is a fake feature layer, and the way to prevent it is to take
 * the list from the node rather than from the panel's own build.
 */
export function InboundStudio({ nodeId }: { nodeId: number }) {
  const [adding, setAdding] = useState(false);

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

  return (
    <section className="rounded border border-border bg-card p-4">
      <div className="mb-3 flex items-center justify-between">
        <h3 className="text-sm font-semibold">{t("studio.title")}</h3>
        {offerable.length > 0 && !adding && (
          <button
            type="button"
            onClick={() => setAdding(true)}
            className="rounded bg-primary px-3 py-1 text-sm hover:bg-primary/90"
          >
            {t("studio.add")}
          </button>
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

      {adding && (
        <AddInbound
          nodeId={nodeId}
          adapters={offerable}
          onClose={() => setAdding(false)}
        />
      )}

      <ServiceList nodeId={nodeId} services={services.data?.services ?? []} />
    </section>
  );
}

function ServiceList({ nodeId, services }: { nodeId: number; services: Service[] }) {
  const queryClient = useQueryClient();
  // Which inbound the operator is being asked about. An id rather than a
  // boolean: the dialog names the protocol it is about to remove, and a bare
  // "are you sure" beside a table of six inbounds is a question nobody can
  // answer safely.
  const [pending, setPending] = useState<Service | null>(null);
  const remove = useMutation({
    mutationFn: (id: number) => api.del(`/api/v1/services/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["node", nodeId] });
      setPending(null);
    },
  });

  if (services.length === 0) {
    return <p className="text-sm text-muted-foreground">{t("studio.noInbounds")}</p>;
  }

  return (
    <>
      <table className="w-full border-collapse text-sm text-foreground">
        <thead>
          <tr className="border-b border-border text-xs uppercase tracking-wide text-muted-foreground">
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
                <button
                  type="button"
                  onClick={() => setPending(svc)}
                  className="text-xs text-destructive hover:underline"
                >
                  {t("delete")}
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <MutationError error={remove.error} />
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
 * AddInbound builds a form from one adapter's schema, or lets the operator
 * write the params document directly.
 *
 * JSON mode is not a way around validation. There is deliberately no second
 * validator in the browser: the panel validates every submission against the
 * same schema the form was built from, using the node's copy of it, and a
 * second implementation here could only drift from that one. So JSON mode
 * checks that the text parses and then submits, and whatever the control plane
 * says is shown verbatim. Raw editing takes exactly the same path as the form.
 */
function AddInbound({
  nodeId,
  adapters,
  onClose,
}: {
  nodeId: number;
  adapters: ServiceSchema[];
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [kind, setKind] = useState(adapters[0]?.kind ?? "");
  const [params, setParams] = useState<Params>({});
  const [jsonMode, setJsonMode] = useState(false);
  const [jsonText, setJsonText] = useState("{}");
  const [jsonError, setJsonError] = useState("");

  const adapter = adapters.find((a) => a.kind === kind);

  const create = useMutation({
    mutationFn: (body: { adapter_kind: string; params: Params }) =>
      api.post(`/api/v1/nodes/${nodeId}/services`, body),
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
        // Only a SYNTAX check. Whether the document satisfies the schema is
        // the panel's answer, and it is the one that counts.
        setJsonError(err instanceof Error ? err.message : String(err));
        return;
      }
    }
    setJsonError("");
    create.mutate({ adapter_kind: kind, params: body });
  }

  return (
    <div className="mb-4 rounded border border-border bg-background p-4">
      <div className="mb-3 flex items-center justify-between">
        <h4 className="text-sm font-semibold">{t("studio.addNew")}</h4>
        <button
          type="button"
          onClick={() => (jsonMode ? toForm() : toJSON())}
          className="text-xs text-muted-foreground hover:text-foreground"
        >
          {jsonMode ? t("studio.switchToForm") : t("studio.switchToJson")}
        </button>
      </div>

      <div className="mb-3">
        <label className="block text-xs text-muted-foreground" htmlFor="studio-kind">
          {t("studio.protocol")}
        </label>
        <select
          id="studio-kind"
          value={kind}
          onChange={(e) => {
            setKind(e.target.value);
            // Params belong to the protocol that was selected. Carrying them
            // across would submit one adapter's fields to another, which the
            // schema refuses and which reads as a bug rather than a mistake.
            setParams({});
            setJsonText("{}");
          }}
          className="w-full rounded border border-input bg-background px-2 py-1 text-sm"
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

      <MutationError error={create.error} />

      <div className="mt-3 flex gap-2">
        <button
          type="button"
          onClick={submit}
          disabled={kind === "" || create.isPending}
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
  );
}
