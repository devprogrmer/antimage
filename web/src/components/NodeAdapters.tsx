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
