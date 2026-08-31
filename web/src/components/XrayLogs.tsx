import { useMutation } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { MutationError } from "../routes/Resellers";
import { Button } from "./ui/button";
import { t } from "../i18n";

interface XrayLogsResponse {
  delivered: boolean;
  ok: boolean;
  logs: string;
  error: string;
  message: string;
}

const LINE_OPTIONS = [50, 100, 200, 500, 1000, 2000];

/**
 * Xray's own runtime log, fetched on demand through the same command
 * channel restart/geo-update/core-upgrade use -- distinct from the "Logs"
 * tab (NodeLogs), which is the panel's own timeline of apply steps and
 * audit records, not anything Xray itself wrote. There is no live tail
 * here: each click is one round trip to a connected agent, which reads
 * `journalctl -u xray` and returns what it found at that moment.
 */
export function XrayLogs({ nodeId }: { nodeId: number }) {
  const [lines, setLines] = useState(200);
  const [result, setResult] = useState<XrayLogsResponse | null>(null);
  const fetchLogs = useMutation({
    mutationFn: () =>
      api.get<XrayLogsResponse>(`/api/v1/nodes/${nodeId}/xray-logs?lines=${lines}`),
    onSuccess: (data) => setResult(data),
  });

  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold">{t("node.xrayLogs")}</h3>
        <p className="text-xs text-muted-foreground">{t("node.xrayLogsHint")}</p>
      </div>

      <div className="flex items-center gap-2">
        <select
          value={lines}
          onChange={(e) => setLines(Number(e.target.value))}
          className="border border-input bg-card px-2 py-1 font-mono text-xs"
          aria-label={t("node.xrayLogLines")}
        >
          {LINE_OPTIONS.map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </select>
        <Button
          size="sm"
          variant="outline"
          disabled={fetchLogs.isPending}
          onClick={() => {
            setResult(null);
            fetchLogs.mutate();
          }}
        >
          {fetchLogs.isPending ? t("egress.saving") : t("node.fetchXrayLogs")}
        </Button>
      </div>
      <MutationError error={fetchLogs.error} />

      {result &&
        (!result.delivered || !result.ok ? (
          <p role="status" className="text-xs text-muted-foreground">
            {result.message}
          </p>
        ) : (
          <pre
            role="status"
            className="max-h-96 overflow-y-auto whitespace-pre-wrap rounded border border-border bg-background p-2 font-mono text-[11px]"
          >
            {result.logs}
          </pre>
        ))}
    </section>
  );
}
