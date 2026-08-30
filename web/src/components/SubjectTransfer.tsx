import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";

import { api } from "../lib/api";
import { MutationError } from "../routes/Resellers";
import { Button } from "./ui/button";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "./ui/sheet";
import { formatNumber, t } from "../i18n";

/**
 * CSV export and import for subjects.
 *
 * Both endpoints existed with no client. Export is the highest-volume
 * disclosure surface in the panel -- one request returns every column of every
 * row the caller may see, including subscription_token, which on its own grants
 * access to a user's configuration -- so the button says so before it runs
 * rather than after.
 *
 * Import is deliberately not a silent bulk create: it reports imported and
 * failed counts with the per-row errors, because a CSV with one malformed date
 * is the normal case and "done" would hide it.
 */

interface ImportResult {
  imported: number;
  failed: number;
  errors?: string[];
}

/** Columns the importer reads. Anything else in the file is ignored. */
const IMPORT_COLUMNS = "Name, Note, ExpiresAt, QuotaBytes, Disabled, Frozen";

export function SubjectTransfer() {
  const queryClient = useQueryClient();
  const [importOpen, setImportOpen] = useState(false);
  const [csv, setCsv] = useState("");
  const [fileName, setFileName] = useState("");
  const [result, setResult] = useState<ImportResult | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);

  const exportCsv = useMutation({
    mutationFn: () => api.getText("/api/v1/subjects/export"),
    onSuccess: (text) => {
      // Built in the browser from the response we already hold, rather than
      // pointing window.location at the endpoint: navigating there drops the
      // fetch credentials handling and, on a 403, replaces the panel with a
      // bare error page instead of showing it in place.
      const url = URL.createObjectURL(new Blob([text], { type: "text/csv" }));
      const a = document.createElement("a");
      a.href = url;
      a.download = "subjects.csv";
      a.click();
      URL.revokeObjectURL(url);
    },
  });

  const runImport = useMutation({
    mutationFn: (body: string) =>
      api.post<ImportResult>("/api/v1/subjects/import", { csv: body }),
    onSuccess: (r) => {
      setResult(r);
      queryClient.invalidateQueries({ queryKey: ["subjects"] });
    },
  });

  function readFile(file: File) {
    setFileName(file.name);
    const reader = new FileReader();
    reader.onload = () => setCsv(String(reader.result ?? ""));
    reader.readAsText(file);
  }

  return (
    <>
      <Button
        variant="outline"
        size="sm"
        onClick={() => exportCsv.mutate()}
        disabled={exportCsv.isPending}
      >
        {t("transfer.export")}
      </Button>
      <Button variant="outline" size="sm" onClick={() => setImportOpen(true)}>
        {t("transfer.import")}
      </Button>

      {/* The export failure has nowhere else to go: the download either happens
          or it does not, and a silent no-op is the one outcome an operator
          cannot diagnose. */}
      <MutationError error={exportCsv.error} />

      <Sheet
        open={importOpen}
        onOpenChange={(open) => {
          setImportOpen(open);
          if (!open) {
            setResult(null);
            setCsv("");
            setFileName("");
          }
        }}
      >
        <SheetContent aria-describedby={undefined}>
          <SheetHeader>
            <SheetTitle>{t("transfer.import")}</SheetTitle>
          </SheetHeader>

          <div className="space-y-3">
            <p className="text-xs text-muted-foreground">
              {t("transfer.importColumns", { columns: IMPORT_COLUMNS })}
            </p>

            <div>
              <input
                ref={fileInput}
                type="file"
                accept=".csv,text/csv"
                className="sr-only"
                aria-label={t("transfer.chooseFile")}
                onChange={(e) => {
                  const file = e.target.files?.[0];
                  if (file) readFile(file);
                }}
              />
              <Button
                variant="outline"
                size="sm"
                onClick={() => fileInput.current?.click()}
              >
                {t("transfer.chooseFile")}
              </Button>
              {fileName !== "" && (
                <span className="ms-2 font-mono text-xs text-muted-foreground">
                  {fileName}
                </span>
              )}
            </div>

            <div>
              <label
                className="block text-xs text-muted-foreground"
                htmlFor="import-csv"
              >
                {t("transfer.csvBody")}
              </label>
              {/* Editable after loading a file, not instead of it: the common
                  repair is one bad row, and making the operator fix it in a
                  spreadsheet and re-pick the file is a worse loop than fixing
                  it here. */}
              <textarea
                id="import-csv"
                value={csv}
                onChange={(e) => setCsv(e.target.value)}
                rows={10}
                spellCheck={false}
                className="w-full rounded-md border border-input bg-background px-2 py-1 font-mono text-xs"
              />
            </div>

            <MutationError error={runImport.error} />

            {result && (
              <div role="status" className="space-y-1">
                <p
                  className={
                    result.failed > 0 ? "text-xs text-warning" : "text-xs text-success"
                  }
                >
                  {t("transfer.importResult", {
                    imported: formatNumber(result.imported),
                    failed: formatNumber(result.failed),
                  })}
                </p>
                {/* Every row error, not just the first. An import of 200 rows
                    that skipped 12 needs all twelve reasons to be fixable. */}
                {result.errors && result.errors.length > 0 && (
                  <ul className="max-h-40 overflow-y-auto text-xs text-muted-foreground">
                    {result.errors.map((e, i) => (
                      <li key={i} className="font-mono">
                        {e}
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            )}

            <div className="flex gap-2">
              <Button
                size="sm"
                disabled={csv.trim() === "" || runImport.isPending}
                onClick={() => runImport.mutate(csv)}
              >
                {t("transfer.runImport")}
              </Button>
              <Button variant="outline" size="sm" onClick={() => setImportOpen(false)}>
                {t("cancel")}
              </Button>
            </div>
          </div>
        </SheetContent>
      </Sheet>
    </>
  );
}
