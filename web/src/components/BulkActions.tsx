import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { ConfirmDialog } from "./ConfirmDialog";
import { MutationError } from "../routes/Resellers";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "./ui/dropdown-menu";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";
import { formatNumber, t } from "../i18n";

/**
 * Bulk actions over the selected subjects.
 *
 * This component existed for 262 lines and was rendered nowhere. It took an
 * `onAction` callback that no caller ever supplied, so none of its five actions
 * reached the server -- and it offered "disable" against a route that did not
 * exist at all. It now calls the endpoints directly, and the disable route was
 * added to match the enable one it had always been paired with in the menu.
 *
 * Every action reports how many subjects it actually changed rather than a bare
 * "done": these endpoints are partial-success by design, returning a count of
 * changed and failed rows, and collapsing that into a checkmark would hide a
 * batch where nine of ten subjects were skipped for want of scope.
 */

const GIB = 1024 * 1024 * 1024;

interface BulkResult {
  changed: number;
  failed: number;
  errors?: string[];
}

/** The endpoints all answer with a differently-named count of what changed. */
function changedCount(body: Record<string, unknown>): number {
  for (const key of ["enabled", "disabled", "deleted", "extended", "reset", "updated"]) {
    const v = body[key];
    if (typeof v === "number") return v;
  }
  return 0;
}

export function BulkActions({
  selectedIds,
  onClearSelection,
}: {
  selectedIds: number[];
  onClearSelection: () => void;
}) {
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState<"delete" | "reset-traffic" | null>(null);
  const [extendOpen, setExtendOpen] = useState(false);
  const [quotaOpen, setQuotaOpen] = useState(false);
  const [extendDays, setExtendDays] = useState("30");
  const [quotaGB, setQuotaGB] = useState("50");
  const [result, setResult] = useState<BulkResult | null>(null);

  const run = useMutation({
    mutationFn: async ({ path, body }: { path: string; body: Record<string, unknown> }) =>
      api.post<Record<string, unknown>>(`/api/v1/subjects/bulk/${path}`, {
        subject_ids: selectedIds,
        ...body,
      }),
    onSuccess: (body) => {
      setResult({
        changed: changedCount(body),
        failed: typeof body.failed === "number" ? body.failed : 0,
        errors: Array.isArray(body.errors) ? (body.errors as string[]) : undefined,
      });
      queryClient.invalidateQueries({ queryKey: ["subjects"] });
      setConfirming(null);
      setExtendOpen(false);
      setQuotaOpen(false);
      onClearSelection();
    },
  });

  const hasSelection = selectedIds.length > 0;

  // The bar outlives the selection on purpose. A successful action clears the
  // selection, and returning null on an empty one would unmount the bar --
  // taking the "3 changed, 1 failed" line with it in the same tick it was
  // written, so the operator would never learn that a row had been skipped.
  if (!hasSelection && result === null) return null;

  const count = formatNumber(selectedIds.length);

  return (
    <div className="mb-2 flex flex-wrap items-center gap-3 rounded-lg border border-border bg-card p-2">
      {hasSelection && <span className="text-sm">{t("bulk.selected", { count })}</span>}

      {hasSelection && (
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button size="sm" disabled={run.isPending}>
            {t("bulk.actions")}
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuItem onSelect={() => run.mutate({ path: "enable", body: {} })}>
            {t("subject.enable")}
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => run.mutate({ path: "disable", body: {} })}>
            {t("subject.disable")}
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setExtendOpen(true)}>
            {t("subject.extendExpiry")}
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setQuotaOpen(true)}>
            {t("subject.setQuota")}
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setConfirming("reset-traffic")}>
            {t("subject.resetTraffic")}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem variant="destructive" onSelect={() => setConfirming("delete")}>
            {t("delete")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      )}

      {hasSelection && (
        <Button
          variant="ghost"
          size="sm"
          onClick={() => {
            setResult(null);
            onClearSelection();
          }}
          className="ms-auto"
        >
          {t("bulk.clearSelection")}
        </Button>
      )}

      <div className="w-full">
        <MutationError error={run.error} />
        {result && (
          <p
            className={
              result.failed > 0 ? "text-xs text-warning" : "text-xs text-success"
            }
            role="status"
          >
            {t("bulk.result", {
              changed: formatNumber(result.changed),
              failed: formatNumber(result.failed),
            })}
            {result.errors && result.errors.length > 0 && (
              <span className="ms-2 text-muted-foreground">{result.errors[0]}</span>
            )}
          </p>
        )}
      </div>

      {/* Delete and reset-traffic are the two that cannot be undone from the
          UI, so they are the two that ask. */}
      <ConfirmDialog
        open={confirming !== null}
        onOpenChange={(open) => !open && setConfirming(null)}
        title={
          confirming === "delete" ? t("bulk.confirmDelete") : t("bulk.confirmResetTraffic")
        }
        description={t("bulk.affects", { count })}
        confirmLabel={confirming === "delete" ? t("delete") : t("subject.resetTraffic")}
        pending={run.isPending}
        onConfirm={() => confirming && run.mutate({ path: confirming, body: {} })}
      />

      <ValueDialog
        open={extendOpen}
        onOpenChange={setExtendOpen}
        title={t("subject.extendExpiry")}
        description={t("bulk.affects", { count })}
        label={t("bulk.daysToExtend")}
        value={extendDays}
        onValueChange={setExtendDays}
        min={1}
        confirmLabel={t("subject.extend")}
        pending={run.isPending}
        onConfirm={(n) => run.mutate({ path: "extend", body: { days: n } })}
      />

      <ValueDialog
        open={quotaOpen}
        onOpenChange={setQuotaOpen}
        title={t("subject.setQuota")}
        description={t("bulk.affects", { count })}
        label={t("bulk.quotaGB")}
        value={quotaGB}
        onValueChange={setQuotaGB}
        min={0}
        // 0 is unlimited on this endpoint, which is why min is 0 and not 1.
        hint={t("bulk.quotaZeroHint")}
        confirmLabel={t("subject.setQuota")}
        pending={run.isPending}
        onConfirm={(n) => run.mutate({ path: "set-quota", body: { quota_bytes: n * GIB } })}
      />
    </div>
  );
}

/**
 * A dialog that collects one number.
 *
 * The value is held as a string, not a number: parseInt("") is NaN, and the
 * previous version reset the field to 0 the moment an operator cleared it to
 * type a new figure.
 */
function ValueDialog({
  open,
  onOpenChange,
  title,
  description,
  label,
  hint,
  value,
  onValueChange,
  min,
  confirmLabel,
  pending,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  label: string;
  hint?: string;
  value: string;
  onValueChange: (v: string) => void;
  min: number;
  confirmLabel: string;
  pending: boolean;
  onConfirm: (n: number) => void;
}) {
  const parsed = Number(value);
  const valid = value.trim() !== "" && Number.isInteger(parsed) && parsed >= min;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent aria-describedby={undefined}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <p className="text-sm text-muted-foreground">{description}</p>
        <div>
          <label className="block text-xs text-muted-foreground" htmlFor="bulk-value">
            {label}
          </label>
          <Input
            id="bulk-value"
            type="number"
            inputMode="numeric"
            min={min}
            value={value}
            onChange={(e) => onValueChange(e.target.value)}
          />
          {hint && <p className="mt-1 text-xs text-muted-foreground">{hint}</p>}
        </div>
        <DialogFooter>
          <Button variant="outline" size="sm" onClick={() => onOpenChange(false)}>
            {t("cancel")}
          </Button>
          <Button size="sm" disabled={!valid || pending} onClick={() => onConfirm(parsed)}>
            {confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
