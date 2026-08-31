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
  supports_balancer: boolean;
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
  // Exactly one of the two is ever non-empty: outbound_tag selects a fixed
  // outbound, balancer_tag selects a named pool the adapter picks among.
  outbound_tag: string;
  balancer_tag: string;
  enabled: boolean;
}

interface Balancer {
  id: number;
  node_id: number;
  tag: string;
  selector: string[];
  strategy: string;
  enabled: boolean;
}

// Every target picker's option value is prefixed with which kind of thing
// it names, since an outbound tag and a balancer tag share one namespace of
// strings a rule can select between -- targetToRequest reverses this on
// submit.
const TARGET_PREFIX_OUTBOUND = "outbound:";
const TARGET_PREFIX_BALANCER = "balancer:";

function targetToRequest(raw: string): { outbound_tag: string; balancer_tag: string } {
  if (raw.startsWith(TARGET_PREFIX_BALANCER)) {
    return { outbound_tag: "", balancer_tag: raw.slice(TARGET_PREFIX_BALANCER.length) };
  }
  return { outbound_tag: raw.slice(TARGET_PREFIX_OUTBOUND.length), balancer_tag: "" };
}

function targetOf(r: { outbound_tag: string; balancer_tag: string }): string {
  return r.balancer_tag ? TARGET_PREFIX_BALANCER + r.balancer_tag : TARGET_PREFIX_OUTBOUND + r.outbound_tag;
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
    <p className="mt-1 text-xs text-destructive" role="alert">
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
  // Balancers are a distinct capability from routing: a node can route
  // without being able to balance among outbounds, so this checks
  // supports_balancer specifically rather than reusing caps.data.supported.
  const balancers = useQuery({
    queryKey: ["egress", nodeId, "balancers"],
    queryFn: () => api.get<{ balancers: Balancer[] }>(`/api/v1/nodes/${nodeId}/balancers`),
    enabled: caps.data?.supported === true && caps.data?.supports_balancer === true,
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

  // Editing an existing outbound. The backend already supported this
  // (PUT /outbounds/{id}); this panel just never called it, so the only way
  // to change a wrong port or a rotated key was delete-and-recreate under a
  // new id -- which also orphans any routing rule that named the old tag.
  const [editingOutbound, setEditingOutbound] = useState<Outbound | null>(null);
  const [editTag, setEditTag] = useState("");
  const [editKind, setEditKind] = useState("");
  const [editParams, setEditParams] = useState("");
  const [editEnabled, setEditEnabled] = useState(true);

  function startEditOutbound(o: Outbound) {
    setEditingOutbound(o);
    setEditTag(o.tag);
    setEditKind(o.kind);
    // Re-serialized from what the server already redacted; a credential
    // field here is the "__redacted__" sentinel the backend restores on
    // submit as long as this field is left untouched.
    setEditParams(JSON.stringify(o.params, null, 2));
    setEditEnabled(o.enabled);
  }

  const updateOutbound = useMutation({
    mutationFn: () =>
      api.put(`/api/v1/nodes/${nodeId}/outbounds/${editingOutbound!.id}`, {
        tag: editTag,
        kind: editKind,
        params: editParams.trim() === "" ? {} : JSON.parse(editParams),
        enabled: editEnabled,
      }),
    onSuccess: () => {
      setEditingOutbound(null);
      invalidate();
    },
  });

  // Which outbound is being removed. Named in the dialog, because a rule that
  // still selects it is the failure this asks about.
  const [pendingOutbound, setPendingOutbound] = useState<Outbound | null>(null);

  const [balancerTag, setBalancerTag] = useState("");
  const [balancerSelector, setBalancerSelector] = useState("");
  const [balancerStrategy, setBalancerStrategy] = useState("random");

  const createBalancer = useMutation({
    mutationFn: () =>
      api.post(`/api/v1/nodes/${nodeId}/balancers`, {
        tag: balancerTag,
        selector: splitList(balancerSelector),
        strategy: balancerStrategy,
      }),
    onSuccess: () => {
      setBalancerTag("");
      setBalancerSelector("");
      setBalancerStrategy("random");
      invalidate();
    },
  });

  const deleteBalancer = useMutation({
    mutationFn: (id: number) => api.del(`/api/v1/nodes/${nodeId}/balancers/${id}`),
    onSuccess: invalidate,
  });

  const [editingBalancer, setEditingBalancer] = useState<Balancer | null>(null);
  const [editBalancerTag, setEditBalancerTag] = useState("");
  const [editBalancerSelector, setEditBalancerSelector] = useState("");
  const [editBalancerStrategy, setEditBalancerStrategy] = useState("random");

  function startEditBalancer(b: Balancer) {
    setEditingBalancer(b);
    setEditBalancerTag(b.tag);
    setEditBalancerSelector(b.selector.join(", "));
    setEditBalancerStrategy(b.strategy);
  }

  const updateBalancer = useMutation({
    mutationFn: () =>
      api.put(`/api/v1/nodes/${nodeId}/balancers/${editingBalancer!.id}`, {
        tag: editBalancerTag,
        selector: splitList(editBalancerSelector),
        strategy: editBalancerStrategy,
      }),
    onSuccess: () => {
      setEditingBalancer(null);
      invalidate();
    },
  });

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
        ...targetToRequest(ruleTarget),
      }),
    onSuccess: () => {
      setRuleDomains("");
      setRuleCIDRs("");
      setRulePorts("");
      setRuleTarget("");
      invalidate();
    },
  });

  const deleteRule = useMutation({
    mutationFn: (id: number) => api.del(`/api/v1/nodes/${nodeId}/routing/${id}`),
    onSuccess: invalidate,
  });

  // Editing a rule: same backend gap as outbounds -- PUT existed, nothing
  // called it, so re-prioritizing or widening a match meant delete-and-lose
  // its position in the ordering.
  const [editingRule, setEditingRule] = useState<RoutingRule | null>(null);
  const [editRuleTarget, setEditRuleTarget] = useState("");
  const [editRuleDomains, setEditRuleDomains] = useState("");
  const [editRuleCIDRs, setEditRuleCIDRs] = useState("");
  const [editRulePorts, setEditRulePorts] = useState("");
  const [editRulePriority, setEditRulePriority] = useState("0");

  function startEditRule(r: RoutingRule) {
    setEditingRule(r);
    setEditRuleTarget(targetOf(r));
    setEditRuleDomains((r.domains ?? []).join(", "));
    setEditRuleCIDRs((r.ip_cidrs ?? []).join(", "));
    setEditRulePorts((r.ports ?? []).join(", "));
    setEditRulePriority(String(r.priority));
  }

  const updateRule = useMutation({
    mutationFn: () =>
      api.put(`/api/v1/nodes/${nodeId}/routing/${editingRule!.id}`, {
        priority: Number(editRulePriority) || 0,
        domains: splitList(editRuleDomains),
        ip_cidrs: splitList(editRuleCIDRs),
        ports: splitList(editRulePorts),
        ...targetToRequest(editRuleTarget),
      }),
    onSuccess: () => {
      setEditingRule(null);
      invalidate();
    },
  });

  const setDefault = useMutation({
    mutationFn: (outboundTag: string) =>
      api.put(`/api/v1/nodes/${nodeId}/routing/default`, { outbound_tag: outboundTag }),
    onSuccess: invalidate,
  });

  if (caps.isLoading) return <p className="text-xs text-muted-foreground">{t("loading")}</p>;

  // Not an error state. A node whose adapters have no routing engine is a
  // normal node, and saying so is more use than an empty panel.
  if (!caps.data?.supported) {
    return (
      <section>
        <h3 className="mb-1 text-xs uppercase tracking-wide text-muted-foreground">{t("egress.title")}</h3>
        <p className="text-xs text-muted-foreground">{t("egress.unsupported")}</p>
      </section>
    );
  }

  // Every outbound tag a rule may legally name: what the operator
  // configured, plus what the adapter supplies on its own. Mirrors the
  // server's own resolution, so the picker cannot offer a target the API
  // would then refuse.
  const selectableOutboundTags = [
    ...(caps.data.builtin_tags ?? []),
    ...(outbounds.data?.outbounds ?? []).filter((o) => o.enabled).map((o) => o.tag),
  ];
  const selectableBalancerTags = (balancers.data?.balancers ?? [])
    .filter((b) => b.enabled)
    .map((b) => b.tag);

  // The rule target picker's option list: outbounds first (the common
  // case), balancers grouped separately and only when this node can even
  // apply one -- rendering an empty optgroup would be worse than omitting
  // it, not better.
  const targetOptions = (
    <>
      <option value="">{t("egress.chooseTarget")}</option>
      <optgroup label={t("egress.outbounds")}>
        {selectableOutboundTags.map((tagName) => (
          <option key={TARGET_PREFIX_OUTBOUND + tagName} value={TARGET_PREFIX_OUTBOUND + tagName}>
            {tagName}
          </option>
        ))}
      </optgroup>
      {caps.data.supports_balancer && selectableBalancerTags.length > 0 && (
        <optgroup label={t("balancer.title")}>
          {selectableBalancerTags.map((tagName) => (
            <option key={TARGET_PREFIX_BALANCER + tagName} value={TARGET_PREFIX_BALANCER + tagName}>
              {tagName}
            </option>
          ))}
        </optgroup>
      )}
    </>
  );

  return (
    <section className="space-y-4">
      <header className="flex items-baseline gap-3">
        <h3 className="text-xs uppercase tracking-wide text-muted-foreground">{t("egress.title")}</h3>
        <span className="font-mono text-[11px] text-muted-foreground">{caps.data.adapter_kind}</span>
      </header>

      {/* Outbounds */}
      <div>
        <h4 className="mb-1 text-xs text-muted-foreground">{t("egress.outbounds")}</h4>
        {(outbounds.data?.outbounds ?? []).length === 0 ? (
          <p className="text-xs text-muted-foreground">{t("egress.noOutbounds")}</p>
        ) : (
          <table className="w-full border-collapse font-mono text-xs">
            <thead>
              <tr className="border-b border-border text-start text-muted-foreground">
                <th className="py-1 pe-3 text-start">{t("egress.tag")}</th>
                <th className="pe-3 text-start">{t("egress.kind")}</th>
                <th className="pe-3 text-start">{t("subject.status")}</th>
                <th className="text-start">{t("actions")}</th>
              </tr>
            </thead>
            <tbody>
              {(outbounds.data?.outbounds ?? []).map((o) => (
                <tr key={o.id} className="border-b border-border">
                  <td className="py-1 pe-3">{o.tag}</td>
                  <td className="pe-3 text-muted-foreground">{o.kind}</td>
                  <td className="pe-3 text-muted-foreground">
                    {o.enabled ? t("subject.enabled") : t("subject.disabled")}
                  </td>
                  <td className="space-x-2 rtl:space-x-reverse">
                    <button
                      type="button"
                      onClick={() => startEditOutbound(o)}
                      className="text-primary hover:underline"
                    >
                      {t("egress.edit")}
                    </button>
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
            <span className="text-[11px] text-muted-foreground">{t("egress.tag")}</span>
            <input
              value={tag}
              onChange={(e) => setTag(e.target.value)}
              className="w-32 border border-input bg-card px-2 py-1 font-mono text-xs"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[11px] text-muted-foreground">{t("egress.kind")}</span>
            {/* The adapter's list, never the frontend's. */}
            <select
              value={kind}
              onChange={(e) => setKind(e.target.value)}
              className="border border-input bg-card px-2 py-1 font-mono text-xs"
            >
              {caps.data.outbound_kinds.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </select>
          </label>
          <label className="flex flex-1 flex-col gap-1">
            <span className="text-[11px] text-muted-foreground">{t("egress.params")}</span>
            <input
              value={params}
              onChange={(e) => setParams(e.target.value)}
              placeholder={t("egress.paramsPlaceholder")}
              className="w-full border border-input bg-card px-2 py-1 font-mono text-xs"
            />
          </label>
          <button
            type="button"
            disabled={tag.trim() === "" || createOutbound.isPending}
            onClick={() => createOutbound.mutate()}
            className="border border-input px-3 py-1 text-xs hover:bg-accent disabled:opacity-40"
          >
            {createOutbound.isPending ? t("egress.saving") : t("create")}
          </button>
        </div>
        <MutationError error={createOutbound.error} />

        {editingOutbound && (
          <div className="mt-3 border border-primary/40 bg-primary/5 p-3">
            <p className="mb-2 text-[11px] text-muted-foreground">
              {t("egress.editing")}: <span className="font-mono">{editingOutbound.tag}</span>
            </p>
            <div className="flex flex-wrap items-end gap-2">
              <label className="flex flex-col gap-1">
                <span className="text-[11px] text-muted-foreground">{t("egress.tag")}</span>
                <input
                  value={editTag}
                  onChange={(e) => setEditTag(e.target.value)}
                  className="w-32 border border-input bg-card px-2 py-1 font-mono text-xs"
                />
              </label>
              <label className="flex flex-col gap-1">
                <span className="text-[11px] text-muted-foreground">{t("egress.kind")}</span>
                {/* Kind is fixed on edit: an outbound whose kind changes needs
                    a different params shape entirely, and the adapter treats
                    that as a new outbound rather than a mutation. */}
                <input
                  value={editKind}
                  disabled
                  className="w-28 border border-input bg-muted px-2 py-1 font-mono text-xs text-muted-foreground"
                />
              </label>
              <label className="flex flex-1 flex-col gap-1">
                <span className="text-[11px] text-muted-foreground">{t("egress.params")}</span>
                <textarea
                  value={editParams}
                  onChange={(e) => setEditParams(e.target.value)}
                  rows={4}
                  className="w-full border border-input bg-card px-2 py-1 font-mono text-xs"
                />
              </label>
              <label className="flex items-center gap-1 text-[11px] text-muted-foreground">
                <input
                  type="checkbox"
                  checked={editEnabled}
                  onChange={(e) => setEditEnabled(e.target.checked)}
                />
                {t("subject.enabled")}
              </label>
              <button
                type="button"
                disabled={editTag.trim() === "" || updateOutbound.isPending}
                onClick={() => updateOutbound.mutate()}
                className="border border-input px-3 py-1 text-xs hover:bg-accent disabled:opacity-40"
              >
                {updateOutbound.isPending ? t("egress.saving") : t("subject.update")}
              </button>
              <button
                type="button"
                onClick={() => setEditingOutbound(null)}
                className="border border-input px-3 py-1 text-xs hover:bg-accent"
              >
                {t("cancel")}
              </button>
            </div>
            <MutationError error={updateOutbound.error} />
          </div>
        )}
      </div>

      {/* Routing rules */}
      <div>
        <h4 className="mb-1 text-xs text-muted-foreground">{t("egress.rules")}</h4>
        <p className="mb-1 text-[11px] text-muted-foreground">{t("egress.rulesOrderNote")}</p>
        {(rules.data?.rules ?? []).length === 0 ? (
          <p className="text-xs text-muted-foreground">{t("egress.noRules")}</p>
        ) : (
          <table className="w-full border-collapse font-mono text-xs">
            <thead>
              <tr className="border-b border-border text-start text-muted-foreground">
                <th className="py-1 pe-3 text-start">{t("egress.priority")}</th>
                <th className="pe-3 text-start">{t("egress.match")}</th>
                <th className="pe-3 text-start">{t("egress.target")}</th>
                <th className="text-start">{t("actions")}</th>
              </tr>
            </thead>
            <tbody>
              {(rules.data?.rules ?? []).map((r) => (
                <tr key={r.id} className="border-b border-border">
                  <td className="py-1 pe-3 text-muted-foreground">{formatNumber(r.priority)}</td>
                  <td className="pe-3 text-foreground">
                    {[
                      ...(r.domains ?? []),
                      ...(r.geosite ?? []),
                      ...(r.ip_cidrs ?? []),
                      ...(r.geoip ?? []),
                      ...(r.ports ?? []),
                    ].join(", ")}
                  </td>
                  <td className="pe-3">
                    {r.balancer_tag ? `${t("balancer.title")}: ${r.balancer_tag}` : r.outbound_tag}
                  </td>
                  <td className="space-x-2 rtl:space-x-reverse">
                    <button
                      type="button"
                      onClick={() => startEditRule(r)}
                      className="text-primary hover:underline"
                    >
                      {t("egress.edit")}
                    </button>
                    <button
                      type="button"
                      onClick={() => deleteRule.mutate(r.id)}
                      className="text-destructive hover:text-destructive"
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
            <span className="text-[11px] text-muted-foreground">{t("egress.priority")}</span>
            <input
              value={rulePriority}
              onChange={(e) => setRulePriority(e.target.value)}
              inputMode="numeric"
              className="w-16 border border-input bg-card px-2 py-1 font-mono text-xs"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[11px] text-muted-foreground">{t("egress.domains")}</span>
            <input
              value={ruleDomains}
              onChange={(e) => setRuleDomains(e.target.value)}
              placeholder={t("egress.commaSeparated")}
              className="w-44 border border-input bg-card px-2 py-1 font-mono text-xs"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[11px] text-muted-foreground">{t("egress.ipCidrs")}</span>
            <input
              value={ruleCIDRs}
              onChange={(e) => setRuleCIDRs(e.target.value)}
              placeholder={t("egress.commaSeparated")}
              className="w-36 border border-input bg-card px-2 py-1 font-mono text-xs"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[11px] text-muted-foreground">{t("egress.ports")}</span>
            <input
              value={rulePorts}
              onChange={(e) => setRulePorts(e.target.value)}
              placeholder={t("egress.portsPlaceholder")}
              className="w-24 border border-input bg-card px-2 py-1 font-mono text-xs"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[11px] text-muted-foreground">{t("egress.target")}</span>
            <select
              value={ruleTarget}
              onChange={(e) => setRuleTarget(e.target.value)}
              className="border border-input bg-card px-2 py-1 font-mono text-xs"
            >
              {targetOptions}
            </select>
          </label>
          <button
            type="button"
            disabled={ruleTarget === "" || createRule.isPending}
            onClick={() => createRule.mutate()}
            className="border border-input px-3 py-1 text-xs hover:bg-accent disabled:opacity-40"
          >
            {createRule.isPending ? t("egress.saving") : t("create")}
          </button>
        </div>
        <MutationError error={createRule.error} />

        {editingRule && (
          <div className="mt-3 border border-primary/40 bg-primary/5 p-3">
            <p className="mb-2 text-[11px] text-muted-foreground">
              {t("egress.editing")}: {t("egress.priority")} {formatNumber(editingRule.priority)}
            </p>
            <div className="flex flex-wrap items-end gap-2">
              <label className="flex flex-col gap-1">
                <span className="text-[11px] text-muted-foreground">{t("egress.priority")}</span>
                <input
                  value={editRulePriority}
                  onChange={(e) => setEditRulePriority(e.target.value)}
                  inputMode="numeric"
                  className="w-16 border border-input bg-card px-2 py-1 font-mono text-xs"
                />
              </label>
              <label className="flex flex-col gap-1">
                <span className="text-[11px] text-muted-foreground">{t("egress.domains")}</span>
                <input
                  value={editRuleDomains}
                  onChange={(e) => setEditRuleDomains(e.target.value)}
                  placeholder={t("egress.commaSeparated")}
                  className="w-44 border border-input bg-card px-2 py-1 font-mono text-xs"
                />
              </label>
              <label className="flex flex-col gap-1">
                <span className="text-[11px] text-muted-foreground">{t("egress.ipCidrs")}</span>
                <input
                  value={editRuleCIDRs}
                  onChange={(e) => setEditRuleCIDRs(e.target.value)}
                  placeholder={t("egress.commaSeparated")}
                  className="w-36 border border-input bg-card px-2 py-1 font-mono text-xs"
                />
              </label>
              <label className="flex flex-col gap-1">
                <span className="text-[11px] text-muted-foreground">{t("egress.ports")}</span>
                <input
                  value={editRulePorts}
                  onChange={(e) => setEditRulePorts(e.target.value)}
                  placeholder={t("egress.portsPlaceholder")}
                  className="w-24 border border-input bg-card px-2 py-1 font-mono text-xs"
                />
              </label>
              <label className="flex flex-col gap-1">
                <span className="text-[11px] text-muted-foreground">{t("egress.target")}</span>
                <select
                  value={editRuleTarget}
                  onChange={(e) => setEditRuleTarget(e.target.value)}
                  className="border border-input bg-card px-2 py-1 font-mono text-xs"
                >
                  {targetOptions}
                </select>
              </label>
              <button
                type="button"
                disabled={editRuleTarget === "" || updateRule.isPending}
                onClick={() => updateRule.mutate()}
                className="border border-input px-3 py-1 text-xs hover:bg-accent disabled:opacity-40"
              >
                {updateRule.isPending ? t("egress.saving") : t("subject.update")}
              </button>
              <button
                type="button"
                onClick={() => setEditingRule(null)}
                className="border border-input px-3 py-1 text-xs hover:bg-accent"
              >
                {t("cancel")}
              </button>
            </div>
            <MutationError error={updateRule.error} />
          </div>
        )}
      </div>

      {/* Default outbound */}
      <div>
        <h4 className="mb-1 text-xs text-muted-foreground">{t("egress.default")}</h4>
        <p className="mb-1 text-[11px] text-muted-foreground">{t("egress.defaultNote")}</p>
        <select
          value={""}
          onChange={(e) => setDefault.mutate(e.target.value)}
          className="border border-input bg-card px-2 py-1 font-mono text-xs"
        >
          <option value="">{t("egress.defaultNone")}</option>
          {selectableOutboundTags.map((tagName) => (
            <option key={tagName} value={tagName}>
              {tagName}
            </option>
          ))}
        </select>
        <MutationError error={setDefault.error} />
      </div>

      {/* Balancers -- only on a node whose adapter has a balancer concept
          at all. A rule selects one via the target picker above, the same
          way it selects an outbound. */}
      {caps.data.supports_balancer && (
        <div>
          <h4 className="mb-1 text-xs text-muted-foreground">{t("balancer.title")}</h4>
          <p className="mb-1 text-[11px] text-muted-foreground">{t("balancer.hint")}</p>
          {(balancers.data?.balancers ?? []).length === 0 ? (
            <p className="text-xs text-muted-foreground">{t("balancer.none")}</p>
          ) : (
            <table className="w-full border-collapse font-mono text-xs">
              <thead>
                <tr className="border-b border-border text-start text-muted-foreground">
                  <th className="py-1 pe-3 text-start">{t("egress.tag")}</th>
                  <th className="pe-3 text-start">{t("balancer.selector")}</th>
                  <th className="pe-3 text-start">{t("balancer.strategy")}</th>
                  <th className="pe-3 text-start">{t("subject.status")}</th>
                  <th className="text-start">{t("actions")}</th>
                </tr>
              </thead>
              <tbody>
                {(balancers.data?.balancers ?? []).map((b) => (
                  <tr key={b.id} className="border-b border-border">
                    <td className="py-1 pe-3">{b.tag}</td>
                    <td className="pe-3 text-muted-foreground">{b.selector.join(", ")}</td>
                    <td className="pe-3 text-muted-foreground">{b.strategy}</td>
                    <td className="pe-3 text-muted-foreground">
                      {b.enabled ? t("subject.enabled") : t("subject.disabled")}
                    </td>
                    <td className="space-x-2 rtl:space-x-reverse">
                      <button
                        type="button"
                        onClick={() => startEditBalancer(b)}
                        className="text-primary hover:underline"
                      >
                        {t("egress.edit")}
                      </button>
                      <button
                        type="button"
                        onClick={() => deleteBalancer.mutate(b.id)}
                        className="text-destructive hover:text-destructive"
                      >
                        {t("delete")}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          <MutationError error={deleteBalancer.error} />

          <div className="mt-2 flex flex-wrap items-end gap-2">
            <label className="flex flex-col gap-1">
              <span className="text-[11px] text-muted-foreground">{t("egress.tag")}</span>
              <input
                value={balancerTag}
                onChange={(e) => setBalancerTag(e.target.value)}
                className="w-32 border border-input bg-card px-2 py-1 font-mono text-xs"
              />
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-[11px] text-muted-foreground">{t("balancer.selector")}</span>
              <input
                value={balancerSelector}
                onChange={(e) => setBalancerSelector(e.target.value)}
                placeholder={t("egress.commaSeparated")}
                className="w-48 border border-input bg-card px-2 py-1 font-mono text-xs"
              />
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-[11px] text-muted-foreground">{t("balancer.strategy")}</span>
              <select
                value={balancerStrategy}
                onChange={(e) => setBalancerStrategy(e.target.value)}
                className="border border-input bg-card px-2 py-1 font-mono text-xs"
              >
                <option value="random">random</option>
                <option value="least_ping">least_ping</option>
              </select>
            </label>
            <button
              type="button"
              disabled={balancerTag.trim() === "" || balancerSelector.trim() === "" || createBalancer.isPending}
              onClick={() => createBalancer.mutate()}
              className="border border-input px-3 py-1 text-xs hover:bg-accent disabled:opacity-40"
            >
              {createBalancer.isPending ? t("egress.saving") : t("create")}
            </button>
          </div>
          <MutationError error={createBalancer.error} />

          {editingBalancer && (
            <div className="mt-3 border border-primary/40 bg-primary/5 p-3">
              <p className="mb-2 text-[11px] text-muted-foreground">
                {t("egress.editing")}: <span className="font-mono">{editingBalancer.tag}</span>
              </p>
              <div className="flex flex-wrap items-end gap-2">
                <label className="flex flex-col gap-1">
                  <span className="text-[11px] text-muted-foreground">{t("egress.tag")}</span>
                  <input
                    value={editBalancerTag}
                    onChange={(e) => setEditBalancerTag(e.target.value)}
                    className="w-32 border border-input bg-card px-2 py-1 font-mono text-xs"
                  />
                </label>
                <label className="flex flex-col gap-1">
                  <span className="text-[11px] text-muted-foreground">{t("balancer.selector")}</span>
                  <input
                    value={editBalancerSelector}
                    onChange={(e) => setEditBalancerSelector(e.target.value)}
                    placeholder={t("egress.commaSeparated")}
                    className="w-48 border border-input bg-card px-2 py-1 font-mono text-xs"
                  />
                </label>
                <label className="flex flex-col gap-1">
                  <span className="text-[11px] text-muted-foreground">{t("balancer.strategy")}</span>
                  <select
                    value={editBalancerStrategy}
                    onChange={(e) => setEditBalancerStrategy(e.target.value)}
                    className="border border-input bg-card px-2 py-1 font-mono text-xs"
                  >
                    <option value="random">random</option>
                    <option value="least_ping">least_ping</option>
                  </select>
                </label>
                <button
                  type="button"
                  disabled={editBalancerTag.trim() === "" || updateBalancer.isPending}
                  onClick={() => updateBalancer.mutate()}
                  className="border border-input px-3 py-1 text-xs hover:bg-accent disabled:opacity-40"
                >
                  {updateBalancer.isPending ? t("egress.saving") : t("subject.update")}
                </button>
                <button
                  type="button"
                  onClick={() => setEditingBalancer(null)}
                  className="border border-input px-3 py-1 text-xs hover:bg-accent"
                >
                  {t("cancel")}
                </button>
              </div>
              <MutationError error={updateBalancer.error} />
            </div>
          )}
        </div>
      )}
    </section>
  );
}
