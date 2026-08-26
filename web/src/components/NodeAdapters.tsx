import { useQuery } from "@tanstack/react-query";

import { api } from "../lib/api";
import { MutationError } from "../routes/Resellers";
import { Badge } from "./ui/badge";
import { formatTimestamp, t } from "../i18n";

interface Adapter {
  kind: string;
  version: string;
  capabilities: string[];
  reported_at: number;
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
  const adapters = useQuery({
    queryKey: ["node", nodeId, "adapters"],
    queryFn: () => api.get<{ adapters: Adapter[] }>(`/api/v1/nodes/${nodeId}/adapters`),
  });
  const capabilities = useQuery({
    queryKey: ["node", nodeId, "capabilities"],
    queryFn: () =>
      api.get<{ capabilities: Capability[] }>(`/api/v1/nodes/${nodeId}/capabilities`),
  });

  return (
    <div className="space-y-6">
      <section>
        <h3 className="mb-2 text-xs uppercase tracking-wide text-muted-foreground">
          {t("node.adapters")}
        </h3>
        <MutationError error={adapters.error} />
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
