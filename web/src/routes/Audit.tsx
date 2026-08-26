import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { MutationError } from "./Resellers";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { formatTimestamp, t } from "../i18n";

interface AuditEntry {
  id: number;
  at: number;
  actor_type: string;
  actor_name: string;
  actor_label: string;
  actor_ip: string;
  request_id: string;
  action: string;
  target_type: string;
  target_id: number;
  result: string;
  before?: unknown;
  after?: unknown;
}

interface Filters {
  action: string;
  result: string;
  actor: string;
  request_id: string;
}

const EMPTY: Filters = { action: "", result: "", actor: "", request_id: "" };

/**
 * The audit log, searchable, showing what each action changed.
 *
 * The table has carried before_json and after_json since SP1 and the query
 * never selected them, so the log could say an action happened and not what it
 * did. Both are shown here, side by side, because the question an operator
 * brings to an audit log is almost never "did this happen" -- it is "what did
 * it change, and who changed it".
 *
 * The request id filter is the reason WriteError returns one: an operator
 * quotes the id off a failure screen and it resolves to the row.
 */
export function Audit() {
  const [draft, setDraft] = useState<Filters>(EMPTY);
  const [applied, setApplied] = useState<Filters>(EMPTY);

  const entries = useQuery({
    queryKey: ["audit", applied],
    queryFn: () => {
      const params = new URLSearchParams();
      for (const [key, value] of Object.entries(applied)) {
        if (value !== "") params.set(key, value);
      }
      return api.get<{ entries: AuditEntry[] }>("/api/v1/audit?" + params.toString());
    },
  });

  return (
    <div className="space-y-4">
      <h2 className="text-lg font-semibold">{t("nav.audit")}</h2>

      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          // Applied on submit rather than per keystroke: the audit log is the
          // one screen where a half-typed filter refetching on every character
          // is a query storm against the largest table in the panel.
          setApplied(draft);
        }}
      >
        <Field label={t("audit.action")} id="audit-action"
          value={draft.action} onChange={(v) => setDraft({ ...draft, action: v })} />
        <Field label={t("audit.actor")} id="audit-actor"
          value={draft.actor} onChange={(v) => setDraft({ ...draft, actor: v })} />
        <Field label={t("error.requestId")} id="audit-request-id"
          value={draft.request_id} onChange={(v) => setDraft({ ...draft, request_id: v })} />
        <div>
          <label className="block text-xs text-muted-foreground" htmlFor="audit-result">
            {t("audit.result")}
          </label>
          <select
            id="audit-result"
            value={draft.result}
            onChange={(e) => setDraft({ ...draft, result: e.target.value })}
            className="h-9 rounded-md border border-input bg-background px-2 text-sm"
          >
            <option value="">{t("filters.all")}</option>
            <option value="ok">ok</option>
            <option value="denied">denied</option>
            <option value="failed">failed</option>
          </select>
        </div>
        <Button size="sm" type="submit">
          {t("audit.search")}
        </Button>
        {applied !== EMPTY && (
          <Button
            size="sm"
            variant="ghost"
            type="button"
            onClick={() => {
              setDraft(EMPTY);
              setApplied(EMPTY);
            }}
          >
            {t("filters.clear")}
          </Button>
        )}
      </form>

      <MutationError error={entries.error} />

      {entries.data?.entries.length === 0 && (
        <p className="text-sm text-muted-foreground">{t("audit.none")}</p>
      )}

      <div className="space-y-1">
        {(entries.data?.entries ?? []).map((e) => (
          <AuditRow key={e.id} entry={e} />
        ))}
      </div>
    </div>
  );
}

function Field({
  label, id, value, onChange,
}: {
  label: string; id: string; value: string; onChange: (v: string) => void;
}) {
  return (
    <div>
      <label className="block text-xs text-muted-foreground" htmlFor={id}>
        {label}
      </label>
      <Input id={id} value={value} onChange={(e) => onChange(e.target.value)} className="w-40" />
    </div>
  );
}

function AuditRow({ entry }: { entry: AuditEntry }) {
  const changed = entry.before !== undefined || entry.after !== undefined;

  const summary = (
    <div className="flex flex-wrap items-center gap-2 text-xs">
      <span className="font-mono text-muted-foreground">{formatTimestamp(entry.at)}</span>
      <span className="font-mono">{entry.action}</span>
      <ResultBadge result={entry.result} />
      <span className="text-muted-foreground">
        {entry.actor_name || entry.actor_label || entry.actor_type}
      </span>
      {entry.actor_ip !== "" && (
        <span className="font-mono text-muted-foreground">{entry.actor_ip}</span>
      )}
      {entry.request_id !== "" && (
        <span className="ms-auto select-all font-mono text-[11px] text-muted-foreground">
          {entry.request_id}
        </span>
      )}
    </div>
  );

  // A row with nothing recorded either side is not expandable. A disclosure
  // triangle that opens onto nothing teaches an operator to stop clicking it.
  if (!changed) {
    return <div className="border-b border-border/50 py-1.5">{summary}</div>;
  }

  return (
    <details className="border-b border-border/50 py-1.5">
      <summary className="cursor-pointer">{summary}</summary>
      <div className="mt-2 grid gap-2 sm:grid-cols-2">
        <Payload label={t("audit.before")} value={entry.before} />
        <Payload label={t("audit.after")} value={entry.after} />
      </div>
    </details>
  );
}

function Payload({ label, value }: { label: string; value: unknown }) {
  return (
    <div>
      <p className="mb-1 text-[11px] uppercase tracking-wide text-muted-foreground">{label}</p>
      <pre className="overflow-x-auto rounded border border-border bg-muted p-2 font-mono text-[11px]">
        {value === undefined ? "—" : JSON.stringify(value, null, 2)}
      </pre>
    </div>
  );
}

function ResultBadge({ result }: { result: string }) {
  if (result === "ok") return <Badge variant="success">{result}</Badge>;
  if (result === "denied") return <Badge variant="warning">{result}</Badge>;
  return <Badge variant="destructive">{result}</Badge>;
}
