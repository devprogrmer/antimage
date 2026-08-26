import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "../lib/api";
import { formatNumber, t } from "../i18n";
import { ConfirmDialog } from "./ConfirmDialog";

/** Egress: the outbounds a node may send traffic through and the rules that
 *  select between them.
 *
 *  Everything offered here comes from the node's own capabilities. The kinds in
 *  the picker are the adapter's OutboundKinds, and the panel hides itself
 *  entirely on a node whose adapters have no routing engine -- a UI that
 *  offered egress everywhere would let an operator configure a policy the node
 *  silently never applies, which is the exact failure the capability system
 *  exists to prevent. */

interface EgressCapabilities {
  supported: boolean;
  adapter_kind?: string;
  outbound_kinds: string[];
  builtin_tags: string[];
  reason?: string;
}

interface Outbound {
  id: number;
  node_id: number;
  tag: string;
  kind: string;
  params: Record<string, unknown>;
  enabled: boolean;
}

interface RoutingRule {
  id: number;
  node_id: number;
  priority: number;
  domains: string[] | null;
  ip_cidrs: string[] | null;
  geoip: string[] | null;
  geosite: string[] | null;
  ports: string[] | null;
  inbound_tags: string[] | null;
  subject_ids: number[] | null;
  network: string;
  outbound_tag: string;
  enabled: boolean;
}

/** splitList turns a comma-separated field into the array the API expects,
 *  dropping blanks so a trailing comma does not become an empty matcher that
 *  would widen the rule. */
function splitList(raw: string): string[] {
  return raw
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

/** Renders a server refusal.
 *
 *  The message is the backend's own: it explains WHY -- a duplicate tag, a
 *  rule with no matchers, a tag that resolves nowhere -- and restating it here
 *  in the frontend's words would mean maintaining two explanations of one rule
 *  and letting them drift. */
function MutationError({ error }: { error: unknown }) {
  if (!error) return null;
  const message = error instanceof ApiError ? error.message : String(error);
  return (
    <p className="mt-1 text-xs text-red-400" role="alert">
      {message}
    </p>
  );
}

export function EgressPanel({ nodeId }: { nodeId: number }) {
  const queryClient = useQueryClient();

  const caps = useQuery({
    queryKey: ["egress", nodeId, "capabilities"],
    queryFn: () => api.get<EgressCapabilities>(`/api/v1/nodes/${nodeId}/egress/capabilities`),
  });
  const outbounds = useQuery({
    queryKey: ["egress", nodeId, "outbounds"],
    queryFn: () => api.get<{ outbounds: Outbound[] }>(`/api/v1/nodes/${nodeId}/outbounds`),
    enabled: caps.data?.supported === true,
  });
  const rules = useQuery({
    queryKey: ["egress", nodeId, "routing"],
    queryFn: () => api.get<{ rules: RoutingRule[] }>(`/api/v1/nodes/${nodeId}/routing`),
    enabled: caps.data?.supported === true,
  });

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ["egress", nodeId] });
    // Egress changes bump the node's desired revision, so the header showing
    // desired versus applied is now stale too.
    queryClient.invalidateQueries({ queryKey: ["node", nodeId] });
  };

  const [tag, setTag] = useState("");
  const [kind, setKind] = useState("");
  const [params, setParams] = useState("");

  const createOutbound = useMutation({
    mutationFn: () =>
      api.post(`/api/v1/nodes/${nodeId}/outbounds`, {
        tag,
        kind: kind || caps.data?.outbound_kinds[0],
        params: params.trim() === "" ? {} : JSON.parse(params),
      }),
    onSuccess: () => {
      setTag("");
      setParams("");
      invalidate();
    },
  });

  const deleteOutbound = useMutation({
    mutationFn: (id: number) => api.del(`/api/v1/nodes/${nodeId}/outbounds/${id}`),
    onSuccess: () => {
      invalidate();
      setPendingOutbound(null);
    },
  });

  // Which outbound is being removed. Named in the dialog, because a rule that
  // still selects it is the failure this asks about.
  const [pendingOutbound, setPendingOutbound] = useState<Outbound | null>(null);
  const [ruleTarget, setRuleTarget] = useState("");
  const [ruleDomains, setRuleDomains] = useState("");
  const [ruleCIDRs, setRuleCIDRs] = useState("");
  const [rulePorts, setRulePorts] = useState("");
  const [rulePriority, setRulePriority] = useState("0");

  const createRule = useMutation({
    mutationFn: () =>
      api.post(`/api/v1/nodes/${nodeId}/routing`, {
        priority: Number(rulePriority) || 0,
        domains: splitList(ruleDomains),
        ip_cidrs: splitList(ruleCIDRs),
        ports: splitList(rulePorts),
        outbound_tag: ruleTarget,
      }),
    onSuccess: () => {
      setRuleDomains("");
      setRuleCIDRs("");
      setRulePorts("");
      invalidate();
    },
  });

  const deleteRule = useMutation({
    mutationFn: (id: number) => api.del(`/api/v1/nodes/${nodeId}/routing/${id}`),
    onSuccess: invalidate,
  });

  const setDefault = useMutation({
    mutationFn: (outboundTag: string) =>
      api.put(`/api/v1/nodes/${nodeId}/routing/default`, { outbound_tag: outboundTag }),
    onSuccess: invalidate,
  });

  if (caps.isLoading) return <p className="text-xs text-zinc-500">{t("loading")}</p>;

  // Not an error state. A node whose adapters have no routing engine is a
  // normal node, and saying so is more use than an empty panel.
  if (!caps.data?.supported) {
    return (
      <section>
        <h3 className="mb-1 text-xs uppercase tracking-wide text-zinc-500">{t("egress.title")}</h3>
        <p className="text-xs text-zinc-500">{t("egress.unsupported")}</p>
      </section>
    );
  }

  // Every tag a rule may legally name: what the operator configured, plus what
  // the adapter supplies on its own. Mirrors the server's own resolution, so
  // the picker cannot offer a target the API would then refuse.
  const selectableTags = [
    ...(caps.data.builtin_tags ?? []),
    ...(outbounds.data?.outbounds ?? []).filter((o) => o.enabled).map((o) => o.tag),
  ];

  return (
    <section className="space-y-4">
      <header className="flex items-baseline gap-3">
        <h3 className="text-xs uppercase tracking-wide text-zinc-500">{t("egress.title")}</h3>
        <span className="font-mono text-[11px] text-zinc-600">{caps.data.adapter_kind}</span>
      </header>

      {/* Outbounds */}
      <div>
        <h4 className="mb-1 text-xs text-zinc-400">{t("egress.outbounds")}</h4>
        {(outbounds.data?.outbounds ?? []).length === 0 ? (
          <p className="text-xs text-zinc-600">{t("egress.noOutbounds")}</p>
        ) : (
          <table className="w-full border-collapse font-mono text-xs">
            <thead>
              <tr className="border-b border-zinc-800 text-start text-zinc-500">
                <th className="py-1 pe-3 text-start">{t("egress.tag")}</th>
                <th className="pe-3 text-start">{t("egress.kind")}</th>
                <th className="pe-3 text-start">{t("subject.status")}</th>
                <th className="text-start">{t("actions")}</th>
              </tr>
            </thead>
            <tbody>
              {(outbounds.data?.outbounds ?? []).map((o) => (
                <tr key={o.id} className="border-b border-zinc-900">
                  <td className="py-1 pe-3">{o.tag}</td>
                  <td className="pe-3 text-zinc-400">{o.kind}</td>
                  <td className="pe-3 text-zinc-500">
                    {o.enabled ? t("subject.enabled") : t("subject.disabled")}
                  </td>
                  <td>
                    <button
                      type="button"
                      // Removing an outbound a rule still names would make the
                      // node refuse its whole configuration, so this asks first
                      // rather than discovering it at convergence.
                      onClick={() => setPendingOutbound(o)}
                      className="text-destructive hover:underline"
                    >
                      {t("delete")}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <ConfirmDialog
          open={pendingOutbound !== null}
          onOpenChange={(open) => !open && setPendingOutbound(null)}
          title={t("egress.confirmDeleteOutbound")}
          description={pendingOutbound?.tag}
          confirmLabel={t("delete")}
          pending={deleteOutbound.isPending}
          onConfirm={() => pendingOutbound && deleteOutbound.mutate(pendingOutbound.id)}
        />
        <MutationError error={deleteOutbound.error} />

        <div className="mt-2 flex flex-wrap items-end gap-2">
          <label className="flex flex-col gap-1">
            <span className="text-[11px] text-zinc-500">{t("egress.tag")}</span>
            <input
              value={tag}
              onChange={(e) => setTag(e.target.value)}
              className="w-32 border border-zinc-700 bg-zinc-900 px-2 py-1 font-mono text-xs"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[11px] text-zinc-500">{t("egress.kind")}</span>
            {/* The adapter's list, never the frontend's. */}
            <select
              value={kind}
              onChange={(e) => setKind(e.target.value)}
              className="border border-zinc-700 bg-zinc-900 px-2 py-1 font-mono text-xs"
            >
              {caps.data.outbound_kinds.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </select>
          </label>
          <label className="flex flex-1 flex-col gap-1">
            <span className="text-[11px] text-zinc-500">{t("egress.params")}</span>
            <input
              value={params}
              onChange={(e) => setParams(e.target.value)}
              placeholder={t("egress.paramsPlaceholder")}
              className="w-full border border-zinc-700 bg-zinc-900 px-2 py-1 font-mono text-xs"
            />
          </label>
          <button
            type="button"
            disabled={tag.trim() === "" || createOutbound.isPending}
            onClick={() => createOutbound.mutate()}
            className="border border-zinc-700 px-3 py-1 text-xs hover:bg-zinc-800 disabled:opacity-40"
          >
            {createOutbound.isPending ? t("egress.saving") : t("create")}
          </button>
        </div>
        <MutationError error={createOutbound.error} />
      </div>

      {/* Routing rules */}
      <div>
        <h4 className="mb-1 text-xs text-zinc-400">{t("egress.rules")}</h4>
        <p className="mb-1 text-[11px] text-zinc-600">{t("egress.rulesOrderNote")}</p>
        {(rules.data?.rules ?? []).length === 0 ? (
          <p className="text-xs text-zinc-600">{t("egress.noRules")}</p>
        ) : (
          <table className="w-full border-collapse font-mono text-xs">
            <thead>
              <tr className="border-b border-zinc-800 text-start text-zinc-500">
                <th className="py-1 pe-3 text-start">{t("egress.priority")}</th>
                <th className="pe-3 text-start">{t("egress.match")}</th>
                <th className="pe-3 text-start">{t("egress.target")}</th>
                <th className="text-start">{t("actions")}</th>
              </tr>
            </thead>
            <tbody>
              {(rules.data?.rules ?? []).map((r) => (
                <tr key={r.id} className="border-b border-zinc-900">
                  <td className="py-1 pe-3 text-zinc-400">{formatNumber(r.priority)}</td>
                  <td className="pe-3 text-zinc-300">
                    {[
                      ...(r.domains ?? []),
                      ...(r.geosite ?? []),
                      ...(r.ip_cidrs ?? []),
                      ...(r.geoip ?? []),
                      ...(r.ports ?? []),
                    ].join(", ")}
                  </td>
                  <td className="pe-3">{r.outbound_tag}</td>
                  <td>
                    <button
                      type="button"
                      onClick={() => deleteRule.mutate(r.id)}
                      className="text-red-400 hover:text-red-300"
                    >
                      {t("delete")}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <MutationError error={deleteRule.error} />

        <div className="mt-2 flex flex-wrap items-end gap-2">
          <label className="flex flex-col gap-1">
            <span className="text-[11px] text-zinc-500">{t("egress.priority")}</span>
            <input
              value={rulePriority}
              onChange={(e) => setRulePriority(e.target.value)}
              inputMode="numeric"
              className="w-16 border border-zinc-700 bg-zinc-900 px-2 py-1 font-mono text-xs"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[11px] text-zinc-500">{t("egress.domains")}</span>
            <input
              value={ruleDomains}
              onChange={(e) => setRuleDomains(e.target.value)}
              placeholder={t("egress.commaSeparated")}
              className="w-44 border border-zinc-700 bg-zinc-900 px-2 py-1 font-mono text-xs"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[11px] text-zinc-500">{t("egress.ipCidrs")}</span>
            <input
              value={ruleCIDRs}
              onChange={(e) => setRuleCIDRs(e.target.value)}
              placeholder={t("egress.commaSeparated")}
              className="w-36 border border-zinc-700 bg-zinc-900 px-2 py-1 font-mono text-xs"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[11px] text-zinc-500">{t("egress.ports")}</span>
            <input
              value={rulePorts}
              onChange={(e) => setRulePorts(e.target.value)}
              placeholder={t("egress.portsPlaceholder")}
              className="w-24 border border-zinc-700 bg-zinc-900 px-2 py-1 font-mono text-xs"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[11px] text-zinc-500">{t("egress.target")}</span>
            <select
              value={ruleTarget}
              onChange={(e) => setRuleTarget(e.target.value)}
              className="border border-zinc-700 bg-zinc-900 px-2 py-1 font-mono text-xs"
            >
              <option value="">{t("egress.chooseTarget")}</option>
              {selectableTags.map((tagName) => (
                <option key={tagName} value={tagName}>
                  {tagName}
                </option>
              ))}
            </select>
          </label>
          <button
            type="button"
            disabled={ruleTarget === "" || createRule.isPending}
            onClick={() => createRule.mutate()}
            className="border border-zinc-700 px-3 py-1 text-xs hover:bg-zinc-800 disabled:opacity-40"
          >
            {createRule.isPending ? t("egress.saving") : t("create")}
          </button>
        </div>
        <MutationError error={createRule.error} />
      </div>

      {/* Default outbound */}
      <div>
        <h4 className="mb-1 text-xs text-zinc-400">{t("egress.default")}</h4>
        <p className="mb-1 text-[11px] text-zinc-600">{t("egress.defaultNote")}</p>
        <select
          value={""}
          onChange={(e) => setDefault.mutate(e.target.value)}
          className="border border-zinc-700 bg-zinc-900 px-2 py-1 font-mono text-xs"
        >
          <option value="">{t("egress.defaultNone")}</option>
          {selectableTags.map((tagName) => (
            <option key={tagName} value={tagName}>
              {tagName}
            </option>
          ))}
        </select>
        <MutationError error={setDefault.error} />
      </div>
    </section>
  );
}
