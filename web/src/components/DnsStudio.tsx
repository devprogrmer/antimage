import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { api, ApiError } from "../lib/api";
import { t } from "../i18n";
import { Button } from "./ui/button";

/** DNS: which servers a node queries, static host overrides that skip
 *  resolution entirely, and fake-IP pools that defer resolution until a
 *  connection's real destination is known.
 *
 *  Whole-object, unlike Egress's outbounds and routing rules: there is
 *  exactly one DNS config per node, so this loads it once, edits it as one
 *  form, and PUTs it back as one document rather than managing independently
 *  addressable rows. The panel hides itself on a node whose adapter has no
 *  DNS concept, the same reasoning EgressPanel hides itself for routing. */

interface DNSServerOut {
  address: string;
  domains?: string[];
  skip_fallback?: boolean;
}

interface FakeDNSPoolOut {
  ip_pool: string;
  pool_size: number;
}

interface DNSResponse {
  supported: boolean;
  adapter_kind?: string;
  reason?: string;
  servers?: DNSServerOut[];
  hosts?: Record<string, string[]>;
  fakedns?: FakeDNSPoolOut[];
  query_strategy?: string;
  disable_cache?: boolean;
}

interface ServerRow {
  address: string;
  domains: string;
  skipFallback: boolean;
}

interface HostRow {
  domain: string;
  ips: string;
}

interface PoolRow {
  ipPool: string;
  poolSize: string;
}

// Xray's own wire values, not human language -- rendered through {qs}
// interpolation rather than as literal JSX text so they read as the
// adapter's vocabulary, not prose a translator would touch.
const QUERY_STRATEGIES = ["UseIP", "UseIPv4", "UseIPv6"];

function splitList(raw: string): string[] {
  return raw
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

function MutationError({ error }: { error: unknown }) {
  if (!error) return null;
  const message = error instanceof ApiError ? error.message : String(error);
  return (
    <p className="mt-1 text-xs text-destructive" role="alert">
      {message}
    </p>
  );
}

export function DnsStudio({ nodeId }: { nodeId: number }) {
  const queryClient = useQueryClient();
  const dns = useQuery({
    queryKey: ["node", nodeId, "dns"],
    queryFn: () => api.get<DNSResponse>(`/api/v1/nodes/${nodeId}/dns`),
  });

  const [servers, setServers] = useState<ServerRow[]>([]);
  const [hosts, setHosts] = useState<HostRow[]>([]);
  const [pools, setPools] = useState<PoolRow[]>([]);
  const [queryStrategy, setQueryStrategy] = useState("");
  const [disableCache, setDisableCache] = useState(false);

  // Seeded from the server once loaded, not controlled by it thereafter --
  // the operator edits local state and Save is what pushes it back. Re-runs
  // after a successful save too, since that invalidates the query and
  // refetches the now-canonical stored value.
  useEffect(() => {
    if (!dns.data?.supported) return;
    setServers(
      (dns.data.servers ?? []).map((s) => ({
        address: s.address,
        domains: (s.domains ?? []).join(", "),
        skipFallback: s.skip_fallback ?? false,
      })),
    );
    setHosts(
      Object.entries(dns.data.hosts ?? {}).map(([domain, ips]) => ({
        domain,
        ips: ips.join(", "),
      })),
    );
    setPools(
      (dns.data.fakedns ?? []).map((p) => ({
        ipPool: p.ip_pool,
        poolSize: String(p.pool_size),
      })),
    );
    setQueryStrategy(dns.data.query_strategy ?? "");
    setDisableCache(dns.data.disable_cache ?? false);
  }, [dns.data]);

  const save = useMutation({
    mutationFn: () =>
      api.put(`/api/v1/nodes/${nodeId}/dns`, {
        servers: servers
          .filter((s) => s.address.trim() !== "")
          .map((s) => ({
            address: s.address.trim(),
            domains: splitList(s.domains),
            skip_fallback: s.skipFallback,
          })),
        hosts: Object.fromEntries(
          hosts
            .filter((h) => h.domain.trim() !== "")
            .map((h) => [h.domain.trim(), splitList(h.ips)]),
        ),
        fakedns: pools
          .filter((p) => p.ipPool.trim() !== "")
          .map((p) => ({ ip_pool: p.ipPool.trim(), pool_size: Number(p.poolSize) || 0 })),
        query_strategy: queryStrategy,
        disable_cache: disableCache,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["node", nodeId, "dns"] });
      // A saved DNS config bumps the node's desired revision, so the header
      // showing desired versus applied is now stale too.
      queryClient.invalidateQueries({ queryKey: ["node", nodeId] });
    },
  });

  if (dns.isLoading) return <p className="text-xs text-muted-foreground">{t("loading")}</p>;

  if (!dns.data?.supported) {
    return (
      <section>
        <h3 className="mb-1 text-xs uppercase tracking-wide text-muted-foreground">
          {t("dns.title")}
        </h3>
        <p className="text-xs text-muted-foreground">{dns.data?.reason || t("dns.unsupported")}</p>
      </section>
    );
  }

  return (
    <section className="space-y-4">
      <header className="flex items-baseline gap-3">
        <h3 className="text-xs uppercase tracking-wide text-muted-foreground">{t("dns.title")}</h3>
        <span className="font-mono text-[11px] text-muted-foreground">{dns.data.adapter_kind}</span>
      </header>

      {/* Servers */}
      <div>
        <h4 className="mb-1 text-xs text-muted-foreground">{t("dns.servers")}</h4>
        {servers.map((row, i) => (
          <div key={i} className="mb-1 flex flex-wrap items-end gap-2">
            <label className="flex flex-col gap-1">
              <span className="text-[11px] text-muted-foreground">{t("dns.address")}</span>
              <input
                value={row.address}
                onChange={(e) => {
                  const next = [...servers];
                  next[i] = { ...next[i], address: e.target.value };
                  setServers(next);
                }}
                className="w-40 border border-input bg-card px-2 py-1 font-mono text-xs"
              />
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-[11px] text-muted-foreground">{t("dns.domains")}</span>
              <input
                value={row.domains}
                onChange={(e) => {
                  const next = [...servers];
                  next[i] = { ...next[i], domains: e.target.value };
                  setServers(next);
                }}
                placeholder={t("egress.commaSeparated")}
                className="w-56 border border-input bg-card px-2 py-1 font-mono text-xs"
              />
            </label>
            <label className="flex items-center gap-1 text-[11px] text-muted-foreground">
              <input
                type="checkbox"
                checked={row.skipFallback}
                onChange={(e) => {
                  const next = [...servers];
                  next[i] = { ...next[i], skipFallback: e.target.checked };
                  setServers(next);
                }}
              />
              {t("dns.skipFallback")}
            </label>
            <button
              type="button"
              onClick={() => setServers(servers.filter((_, j) => j !== i))}
              className="text-destructive hover:underline"
            >
              {t("delete")}
            </button>
          </div>
        ))}
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => setServers([...servers, { address: "", domains: "", skipFallback: false }])}
        >
          {t("dns.addServer")}
        </Button>
      </div>

      {/* Static hosts */}
      <div>
        <h4 className="mb-1 text-xs text-muted-foreground">{t("dns.hosts")}</h4>
        {hosts.map((row, i) => (
          <div key={i} className="mb-1 flex flex-wrap items-end gap-2">
            <label className="flex flex-col gap-1">
              <span className="text-[11px] text-muted-foreground">{t("dns.domain")}</span>
              <input
                value={row.domain}
                onChange={(e) => {
                  const next = [...hosts];
                  next[i] = { ...next[i], domain: e.target.value };
                  setHosts(next);
                }}
                className="w-44 border border-input bg-card px-2 py-1 font-mono text-xs"
              />
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-[11px] text-muted-foreground">{t("dns.addresses")}</span>
              <input
                value={row.ips}
                onChange={(e) => {
                  const next = [...hosts];
                  next[i] = { ...next[i], ips: e.target.value };
                  setHosts(next);
                }}
                placeholder={t("egress.commaSeparated")}
                className="w-44 border border-input bg-card px-2 py-1 font-mono text-xs"
              />
            </label>
            <button
              type="button"
              onClick={() => setHosts(hosts.filter((_, j) => j !== i))}
              className="text-destructive hover:underline"
            >
              {t("delete")}
            </button>
          </div>
        ))}
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => setHosts([...hosts, { domain: "", ips: "" }])}
        >
          {t("dns.addHost")}
        </Button>
      </div>

      {/* FakeDNS pools */}
      <div>
        <h4 className="mb-1 text-xs text-muted-foreground">{t("dns.fakedns")}</h4>
        {pools.map((row, i) => (
          <div key={i} className="mb-1 flex flex-wrap items-end gap-2">
            <label className="flex flex-col gap-1">
              <span className="text-[11px] text-muted-foreground">{t("dns.ipPool")}</span>
              <input
                value={row.ipPool}
                onChange={(e) => {
                  const next = [...pools];
                  next[i] = { ...next[i], ipPool: e.target.value };
                  setPools(next);
                }}
                placeholder="198.18.0.0/15"
                className="w-40 border border-input bg-card px-2 py-1 font-mono text-xs"
              />
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-[11px] text-muted-foreground">{t("dns.poolSize")}</span>
              <input
                value={row.poolSize}
                onChange={(e) => {
                  const next = [...pools];
                  next[i] = { ...next[i], poolSize: e.target.value };
                  setPools(next);
                }}
                inputMode="numeric"
                className="w-24 border border-input bg-card px-2 py-1 font-mono text-xs"
              />
            </label>
            <button
              type="button"
              onClick={() => setPools(pools.filter((_, j) => j !== i))}
              className="text-destructive hover:underline"
            >
              {t("delete")}
            </button>
          </div>
        ))}
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => setPools([...pools, { ipPool: "", poolSize: "65535" }])}
        >
          {t("dns.addPool")}
        </Button>
      </div>

      {/* Query strategy + cache */}
      <div className="flex flex-wrap items-end gap-4">
        <label className="flex flex-col gap-1">
          <span className="text-[11px] text-muted-foreground">{t("dns.queryStrategy")}</span>
          <select
            value={queryStrategy}
            onChange={(e) => setQueryStrategy(e.target.value)}
            className="border border-input bg-card px-2 py-1 font-mono text-xs"
          >
            <option value="">{t("dns.queryStrategyAny")}</option>
            {QUERY_STRATEGIES.map((qs) => (
              <option key={qs} value={qs}>
                {qs}
              </option>
            ))}
          </select>
        </label>
        <label className="flex items-center gap-1 text-[11px] text-muted-foreground">
          <input
            type="checkbox"
            checked={disableCache}
            onChange={(e) => setDisableCache(e.target.checked)}
          />
          {t("dns.disableCache")}
        </label>
      </div>

      <Button
        type="button"
        disabled={save.isPending}
        onClick={() => save.mutate()}
        className="border border-input px-3 py-1 text-xs hover:bg-accent disabled:opacity-40"
      >
        {save.isPending ? t("egress.saving") : t("save")}
      </Button>
      <MutationError error={save.error} />
    </section>
  );
}
