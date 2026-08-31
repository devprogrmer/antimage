import { useQuery } from "@tanstack/react-query";

import { api } from "../lib/api";
import { MutationError } from "../routes/Resellers";
import { formatNumber, t } from "../i18n";

/**
 * What the node is actually enforcing for one subject, against what was
 * configured.
 *
 * The endpoint existed with no client, which meant the panel could set
 * max_devices, max_ips, max_connections and speed limits and then show nobody
 * whether any of them were being hit. When a customer says "it stopped
 * working", the answer is usually on this card: they are at their device or IP
 * ceiling, and the enforcement layer is refusing new connections exactly as
 * asked.
 *
 * Every limit is nullable, and null means UNLIMITED -- not zero. Rendering an
 * absent cap as 0 would turn "no restriction" into "no allowance", which is
 * the same collapse the user-preset screen has a comment about.
 */

interface EnforcementStatus {
  subject_id: number;
  max_devices?: number | null;
  max_ips?: number | null;
  max_connections?: number | null;
  speed_limit_up_kbps?: number | null;
  speed_limit_down_kbps?: number | null;
  current_devices: number;
  current_ips: number;
  current_connections: number;
}

export function EnforcementStatus({ subjectId }: { subjectId: number }) {
  const status = useQuery({
    queryKey: ["enforcement", subjectId],
    queryFn: () =>
      api.get<EnforcementStatus>(`/api/v1/subjects/${subjectId}/enforcement`),
  });

  return (
    <div className="rounded border border-border bg-card p-4">
      <h3 className="mb-3 text-sm font-semibold">{t("enforcement.title")}</h3>
      <MutationError error={status.error} />

      {status.data && (
        <dl className="space-y-3">
          <Usage
            label={t("enforcement.devices")}
            current={status.data.current_devices}
            limit={status.data.max_devices}
          />
          <Usage
            label={t("enforcement.ips")}
            current={status.data.current_ips}
            limit={status.data.max_ips}
          />
          <Usage
            label={t("enforcement.connections")}
            current={status.data.current_connections}
            limit={status.data.max_connections}
          />
          {/* Speed has a configured cap and no live counter -- the node shapes
              the stream rather than counting it -- so showing it as a bar would
              invent a measurement nobody took. */}
          <Speed label={t("enforcement.speedUp")} kbps={status.data.speed_limit_up_kbps} />
          <Speed label={t("enforcement.speedDown")} kbps={status.data.speed_limit_down_kbps} />
        </dl>
      )}
    </div>
  );
}

/** One limit, its current value, and how close the two are. */
function Usage({
  label,
  current,
  limit,
}: {
  label: string;
  current: number;
  limit?: number | null;
}) {
  const unlimited = limit === null || limit === undefined;
  // A limit of 0 is a real cap meaning "none allowed", and dividing by it would
  // give Infinity, so it is treated as full rather than as an error.
  const atLimit = !unlimited && current >= limit;
  const percent = unlimited || limit === 0 ? 0 : Math.min(100, (current / limit) * 100);

  return (
    <div>
      <div className="flex items-baseline justify-between text-sm">
        <dt className="text-muted-foreground">{label}</dt>
        <dd className={atLimit ? "font-mono text-warning" : "font-mono"}>
          {unlimited
            ? t("enforcement.ofUnlimited", { current: formatNumber(current) })
            : t("enforcement.ofLimit", {
                current: formatNumber(current),
                limit: formatNumber(limit),
              })}
        </dd>
      </div>
      {/* No bar for an unlimited allowance: a track with nothing to fill
          against implies a ceiling that does not exist. */}
      {!unlimited && (
        <div
          className="mt-1 h-1.5 w-full overflow-hidden rounded-full bg-muted"
          role="progressbar"
          aria-label={label}
          aria-valuenow={current}
          aria-valuemin={0}
          aria-valuemax={limit}
        >
          <div
            className={atLimit ? "h-full bg-warning" : "h-full bg-primary"}
            style={{ width: `${percent}%` }}
          />
        </div>
      )}
      {atLimit && (
        <p className="mt-1 text-xs text-warning">{t("enforcement.atLimit")}</p>
      )}
    </div>
  );
}

/** A configured speed cap, rendered in the unit an operator sold it in. */
function Speed({ label, kbps }: { label: string; kbps?: number | null }) {
  return (
    <div className="flex items-baseline justify-between text-sm">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="font-mono">
        {kbps === null || kbps === undefined
          ? t("reseller.unlimited")
          : formatSpeed(kbps)}
      </dd>
    </div>
  );
}

/**
 * Kilobits per second as the operator would say it.
 *
 * Plans are sold in Mbps, and 20480 kbps on a card is a number an operator has
 * to divide in their head to check against what they sold. Below 1 Mbps it
 * stays in kbps rather than rendering "0.4 Mbps".
 */
export function formatSpeed(kbps: number): string {
  if (kbps >= 1000) {
    // Rounded to one decimal, then formatted -- not String()/toFixed(), which
    // emit Latin digits and would put "5.5" on a Persian or Arabic card where
    // every other number is localised. formatNumber drops a trailing .0 on its
    // own, so 5000 kbps reads "5 Mbps" rather than "5.0 Mbps".
    return t("enforcement.mbps", {
      value: formatNumber(Math.round(kbps / 100) / 10),
    });
  }
  return t("enforcement.kbps", { value: formatNumber(kbps) });
}
