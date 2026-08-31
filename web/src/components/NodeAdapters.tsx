import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { MutationError } from "../routes/Resellers";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { formatTimestamp, t } from "../i18n";

interface Adapter {
  kind: string;
  version: string;
  capabilities: string[];
  reported_at: number;
  // Null when this adapter's geo data has never been updated through the
  // panel -- true for every adapter kind with no geo-data concept at all,
  // so the update control only ever renders where it could be non-null.
  geo_updated_at: number | null;
  geoip_sha256?: string;
  geosite_sha256?: string;
  core_upgraded_at: number | null;
}

interface XrayCoreVersion {
  version: string;
  binary_url: string;
  binary_sha256: string;
}

interface CoreUpgradeResponse {
  delivered: boolean;
  ok: boolean;
  installed_version: string;
  rolled_back: boolean;
  error: string;
  message: string;
}

interface GeoUpdateOutcome {
  kind: string;
  ok: boolean;
  error: string;
  geoip_sha256: string;
  geosite_sha256: string;
}

interface GeoUpdateResponse {
  delivered: boolean;
  outcomes: GeoUpdateOutcome[] | null;
  message: string;
}

interface Capability {
  protocol: string;
  available: boolean;
  version?: string;
  detected_at: number;
  last_check_at: number;
}

/**
 * What the node reports it can run, and what the panel found when it looked.
 *
 * Two endpoints that existed and had no client. They answer different
 * questions and are shown together because the interesting case is when they
 * disagree: an adapter the agent registered but whose binary the probe cannot
 * find is a node that will accept an inbound and fail to apply it.
 */
export function NodeAdapters({ nodeId }: { nodeId: number }) {
  const queryClient = useQueryClient();
  const adapters = useQuery({
    queryKey: ["node", nodeId, "adapters"],
    queryFn: () => api.get<{ adapters: Adapter[] }>(`/api/v1/nodes/${nodeId}/adapters`),
  });
  const capabilities = useQuery({
    queryKey: ["node", nodeId, "capabilities"],
    queryFn: () =>
      api.get<{ capabilities: Capability[] }>(`/api/v1/nodes/${nodeId}/capabilities`),
  });

  // Only fetched when this node actually has an xray row -- there is no
  // reason to touch GitHub for a node that only runs, say, WireGuard.
  const hasXray = (adapters.data?.adapters ?? []).some((a) => a.kind === "xray");
  const coreVersions = useQuery({
    queryKey: ["xray-core-versions"],
    queryFn: () => api.get<{ versions: XrayCoreVersion[] }>("/api/v1/xray-core-versions"),
    enabled: hasXray,
    // Fleet-wide, slow-changing data; no reason to refetch it as often as
    // node-specific state.
    staleTime: 5 * 60 * 1000,
  });
  const [selectedVersion, setSelectedVersion] = useState<string>("");
  const [coreResult, setCoreResult] = useState<CoreUpgradeResponse | null>(null);
  const upgradeCore = useMutation({
    mutationFn: (v: XrayCoreVersion) =>
      api.post<CoreUpgradeResponse>(`/api/v1/nodes/${nodeId}/core-upgrade`, {
        kind: "xray",
        binary_url: v.binary_url,
        binary_sha256: v.binary_sha256,
        expected_version: v.version,
      }),
    onSuccess: (data) => {
      setCoreResult(data);
      queryClient.invalidateQueries({ queryKey: ["node", nodeId, "adapters"] });
    },
  });

  // One button for the node, not one per adapter row: the operator's intent
  // is "refresh whatever geo data this node has," not a per-protocol
  // decision, and RestartAdapters/NodeActions already use the same
  // node-level shape. Which adapter kinds actually have geo data is decided
  // agent-side (adapter.GeoDataUpdater) and reported back per outcome, so
  // this deliberately does not hardcode "xray" here -- a node running only
  // wireguard sees the button do nothing useful and say so, rather than the
  // browser guessing in advance which protocols qualify.
  const [geoResult, setGeoResult] = useState<GeoUpdateResponse | null>(null);
  const updateGeoData = useMutation({
    mutationFn: () => api.post<GeoUpdateResponse>(`/api/v1/nodes/${nodeId}/geo-update`, {}),
    onSuccess: (data) => {
      setGeoResult(data);
      queryClient.invalidateQueries({ queryKey: ["node", nodeId, "adapters"] });
    },
  });

  return (
    <div className="space-y-6">
      <section>
        <div className="mb-2 flex items-center gap-2">
          <h3 className="text-xs uppercase tracking-wide text-muted-foreground">
            {t("node.adapters")}
          </h3>
          <Button
            size="sm"
            variant="outline"
            className="ms-auto"
            disabled={updateGeoData.isPending}
            onClick={() => {
              setGeoResult(null);
              updateGeoData.mutate();
            }}
          >
            {updateGeoData.isPending ? t("egress.saving") : t("node.updateGeoData")}
          </Button>
        </div>
        <MutationError error={adapters.error} />
        <MutationError error={updateGeoData.error} />
        <MutationError error={upgradeCore.error} />
        {geoResult && (
          <div role="status" className="mb-2 rounded border border-border bg-card p-2 text-xs">
            {!geoResult.delivered || !geoResult.outcomes || geoResult.outcomes.length === 0 ? (
              <span className="text-muted-foreground">{geoResult.message}</span>
            ) : (
              <ul className="space-y-0.5">
                {geoResult.outcomes.map((o) => (
                  <li key={o.kind} className={o.ok ? "text-success" : "text-destructive"}>
                    {o.kind}: {o.ok ? t("node.updateGeoDataOK") : o.error}
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
        {adapters.data?.adapters.length === 0 && (
          // Distinct from "no adapters": a node that has never connected has
          // reported nothing, which is a different thing to explain.
          <p className="text-xs text-muted-foreground">{t("node.noAdapters")}</p>
        )}
        {(adapters.data?.adapters ?? []).map((a) => (
          <div key={a.kind} className="border-b border-border/50 py-2 last:border-0">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-mono text-sm">{a.kind}</span>
              <span className="font-mono text-xs text-muted-foreground">{a.version}</span>
              <span className="ms-auto font-mono text-xs text-muted-foreground">
                {formatTimestamp(a.reported_at)}
              </span>
            </div>
            {a.capabilities.length > 0 && (
              <div className="mt-1 flex flex-wrap gap-1">
                {a.capabilities.map((c) => (
                  <Badge key={c} variant="outline">
                    {c}
                  </Badge>
                ))}
              </div>
            )}
            {/* Only rendered for an adapter that has actually had a
                successful geo update at some point -- most adapter kinds
                never will, and printing "never" on every row for protocols
                with no geo concept at all would be noise, not information. */}
            {a.geo_updated_at != null && (
              <p className="mt-1 font-mono text-[11px] text-muted-foreground">
                {t("node.geoDataUpdatedAt")}: {formatTimestamp(a.geo_updated_at)}
                {a.geoip_sha256 && ` · geoip ${a.geoip_sha256.slice(0, 12)}`}
                {a.geosite_sha256 && ` · geosite ${a.geosite_sha256.slice(0, 12)}`}
              </p>
            )}
            {a.core_upgraded_at != null && (
              <p className="mt-1 font-mono text-[11px] text-muted-foreground">
                {t("node.coreUpgradedAt")}: {formatTimestamp(a.core_upgraded_at)}
              </p>
            )}
            {/* The version picker is xray-specific because the version
                LIST endpoint is -- it queries Xray-core's own GitHub
                releases, and sing-box (or any future core-managed
                protocol) would need its own source before this control
                could honestly offer it a version to pick from. The
                POST /core-upgrade route itself is generic and takes any
                kind; only this convenience picker is scoped. */}
            {a.kind === "xray" && (
              <div className="mt-2 flex flex-wrap items-center gap-2">
                <select
                  value={selectedVersion}
                  onChange={(e) => setSelectedVersion(e.target.value)}
                  disabled={coreVersions.isLoading || upgradeCore.isPending}
                  className="border border-input bg-card px-2 py-1 font-mono text-xs"
                  aria-label={t("node.coreVersionPicker")}
                >
                  <option value="">{t("node.chooseVersion")}</option>
                  {(coreVersions.data?.versions ?? []).map((v) => (
                    <option key={v.version} value={v.version}>
                      {v.version}
                    </option>
                  ))}
                </select>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={selectedVersion === "" || upgradeCore.isPending}
                  onClick={() => {
                    const v = (coreVersions.data?.versions ?? []).find(
                      (x) => x.version === selectedVersion,
                    );
                    if (!v) return;
                    setCoreResult(null);
                    upgradeCore.mutate(v);
                  }}
                >
                  {upgradeCore.isPending ? t("egress.saving") : t("node.upgradeCore")}
                </Button>
                <MutationError error={coreVersions.error} />
              </div>
            )}
            {a.kind === "xray" && coreResult && (
              <div role="status" className="mt-1 rounded border border-border bg-card p-2 text-xs">
                {coreResult.rolled_back ? (
                  <span className="text-warning">{coreResult.message}</span>
                ) : coreResult.ok ? (
                  <span className="text-success">
                    {t("node.coreUpgraded")}: {coreResult.installed_version}
                  </span>
                ) : (
                  <span className="text-muted-foreground">{coreResult.message}</span>
                )}
              </div>
            )}
          </div>
        ))}
      </section>

      <section>
        <h3 className="mb-2 text-xs uppercase tracking-wide text-muted-foreground">
          {t("node.capabilities")}
        </h3>
        <MutationError error={capabilities.error} />
        {capabilities.data?.capabilities.length === 0 && (
          <p className="text-xs text-muted-foreground">{t("node.noCapabilities")}</p>
        )}
        {(capabilities.data?.capabilities ?? []).map((c) => (
          <div
            key={c.protocol}
            className="flex flex-wrap items-center gap-2 border-b border-border/50 py-1.5 text-xs last:border-0"
          >
            <span className="font-mono text-sm">{c.protocol}</span>
            {c.version && (
              <span className="font-mono text-muted-foreground">{c.version}</span>
            )}
            {c.available ? (
              <Badge variant="success">{t("node.available")}</Badge>
            ) : (
              <Badge variant="destructive">{t("node.unavailable")}</Badge>
            )}
            <span className="ms-auto font-mono text-muted-foreground">
              {formatTimestamp(c.last_check_at)}
            </span>
          </div>
        ))}
      </section>
    </div>
  );
}
