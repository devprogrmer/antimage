import { formatNumber, t } from "../i18n";
import type { TranslationKey } from "../i18n";

const MIB = 1_048_576;
const GIB = 1_073_741_824;

/** Whole days between now and a unix-seconds timestamp, rounded up so "today"
 *  reads as the last day rather than as zero. Null when there is no expiry. */
export function daysLeft(expiresAt: number | null, now = Date.now()): number | null {
  if (!expiresAt) return null;
  const ms = expiresAt * 1000 - now;
  return Math.max(0, Math.ceil(ms / 86_400_000));
}

/** Traffic amounts the way the other panels render them: GiB with one
 *  decimal once the value is at least a GiB, MiB below that. Tabular digits
 *  keep columns of them aligned. */
export function formatTraffic(bytes: number): string {
  if (bytes >= GIB) return `${formatNumber(Math.round((bytes / GIB) * 10) / 10)} GiB`;
  if (bytes >= MIB) return `${formatNumber(Math.round(bytes / MIB))} MiB`;
  return `${formatNumber(Math.max(0, Math.round(bytes / 1024)))} KiB`;
}

/**
 * Quota usage as a bar with a percentage, coloured by pressure.
 *
 * Every panel in this space renders quota this way -- a bare "1234 / 4096 MB"
 * forces the operator to do arithmetic row by row, while a bar that turns
 * warning-coloured near the limit and destructive over it can be scanned.
 * A null total means unmetered and renders as a word instead of a full bar,
 * because "100% of infinity" is not a number.
 */
export function QuotaBar({
  used,
  total,
  labelKey = "subject.quota",
}: {
  used: number;
  total: number | null;
  labelKey?: TranslationKey;
}) {
  if (!total) {
    return <span className="text-xs text-muted-foreground">{t("filters.unlimited")}</span>;
  }
  const pct = Math.min(100, Math.round((used / total) * 100));
  // Green while there is headroom, warning in the last fifth, destructive
  // at the limit. All design tokens, so the bar reads correctly in both
  // themes.
  const fill = pct >= 100 ? "bg-destructive" : pct >= 80 ? "bg-warning" : "bg-success";
  return (
    <div className="flex items-center gap-2" style={{ minWidth: "8rem" }}>
      <div
        className="h-1.5 w-full max-w-24 overflow-hidden rounded-full bg-secondary"
        role="progressbar"
        aria-valuenow={pct}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-label={t(labelKey)}
      >
        <div className={`h-full rounded-full ${fill}`} style={{ width: `${pct}%` }} />
      </div>
      <span className="whitespace-nowrap font-mono text-xs text-muted-foreground">
        {formatNumber(pct)}%
      </span>
    </div>
  );
}
