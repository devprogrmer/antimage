import { useQuery } from "@tanstack/react-query";

import { api } from "../lib/api";
import { formatTimestamp, t } from "../i18n";

/**
 * The node's own timeline: failing apply steps with the stderr the tool
 * produced, admin actions that targeted the node, and the current last_error
 * the agent sent. This is what the backend `/nodes/{id}/logs` route returns
 * and what an operator investigating a node during an incident asks for
 * first.
 *
 * Not agent syslog -- the agent does not stream logs, and there is no place
 * to fabricate one from. What is shown here is what the panel knows.
 */

interface LogEntry {
  timestamp: number;
  level: "info" | "warn" | "error";
  message: string;
  source: "agent" | "apply" | "audit";
}

interface NodeLogsProps {
  nodeId: number;
}

const SOURCE_LABEL: Record<LogEntry["source"], string> = {
  agent: "agent",
  apply: "apply",
  audit: "audit",
};

export function NodeLogs({ nodeId }: NodeLogsProps) {
  const logs = useQuery({
    queryKey: ["node", nodeId, "logs"],
    queryFn: () =>
      api.get<{ logs: LogEntry[] }>(`/api/v1/nodes/${nodeId}/logs?limit=200`),
    // Live-ish. Long enough not to hammer the backend, short enough that a
    // failing reconcile shows up while an operator is still looking.
    refetchInterval: 10000,
  });

  const entries = logs.data?.logs ?? [];

  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold">{t("node.logs")}</h3>
        <p className="text-xs text-muted-foreground">{t("node.logsHint")}</p>
      </div>

      {logs.isLoading ? (
        <p className="text-xs text-muted-foreground">{t("common.loading")}</p>
      ) : entries.length === 0 ? (
        <p className="text-xs text-muted-foreground">{t("node.noLogs")}</p>
      ) : (
        <div className="rounded border border-border bg-background font-mono text-[11px]">
          <div className="max-h-96 overflow-y-auto p-2">
            {entries.map((entry, i) => (
              <div
                key={i}
                className={`border-b border-border/50 py-1 last:border-0 ${
                  entry.level === "error"
                    ? "text-destructive"
                    : entry.level === "warn"
                    ? "text-warning"
                    : "text-foreground"
                }`}
              >
                <span className="text-muted-foreground">
                  {formatTimestamp(entry.timestamp)}
                </span>{" "}
                <span className="rounded bg-muted px-1 text-[10px] uppercase text-muted-foreground">
                  {SOURCE_LABEL[entry.source] ?? entry.source}
                </span>{" "}
                <span className="whitespace-pre-wrap">{entry.message}</span>
              </div>
            ))}
          </div>
        </div>
      )}
    </section>
  );
}
