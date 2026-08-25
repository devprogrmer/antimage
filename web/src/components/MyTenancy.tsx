import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { ApiError, api } from "../lib/api";
import { MovementsTable } from "../routes/ResellerDetail";
import type { Movement } from "../routes/ResellerDetail";
import { formatNumber, t } from "../i18n";

interface MyTenancy {
  reseller_id: number;
  display_name: string;
  enabled: boolean;
  balance: number;
  credit_floor: number;
}

type Tab = "overview" | "ledger";

/**
 * A tenant's own account, served by scope rather than by permission.
 *
 * The reseller role deliberately holds no reseller:* permission -- granting
 * reseller:read would let one tenant enumerate the others -- so a tenant
 * reaches their own record through /me and nobody else's through anything.
 * That is why this lives in Profile, next to the other settings that act on the
 * signed-in account alone, and not behind the Tenants nav item.
 *
 * An admin who operates no tenancy gets 404 rather than an empty record, which
 * is what lets this render nothing at all instead of an error: "no tenancy" is
 * a normal state for most accounts, not a failure.
 */
export function MyTenancy() {
  const [tab, setTab] = useState<Tab>("overview");
  const tenancy = useQuery({
    queryKey: ["my-tenancy"],
    queryFn: () => api.get<MyTenancy>("/api/v1/me/reseller"),
    // 404 means this account is not a tenant, which no amount of retrying
    // changes; retrying it would put three failed requests behind every visit
    // to Profile for the majority of accounts.
    retry: false,
  });

  if (tenancy.error instanceof ApiError && tenancy.error.status === 404) return null;
  if (!tenancy.data) return null;

  const record = tenancy.data;
  const atFloor = record.balance <= record.credit_floor;

  return (
    <section className="rounded border border-zinc-800 bg-zinc-900 p-4">
      <h2 className="mb-3 text-sm font-semibold">{t("tenancy.title")}</h2>

      <div className="mb-3 flex gap-3 border-b border-zinc-800 text-xs" role="tablist">
        {(["overview", "ledger"] as const).map((name) => (
          <button
            key={name}
            type="button"
            role="tab"
            aria-selected={tab === name}
            onClick={() => setTab(name)}
            className={
              tab === name
                ? "border-b border-zinc-100 pb-1 text-zinc-100"
                : "pb-1 text-zinc-400 hover:text-zinc-100"
            }
          >
            {name === "overview" ? t("tenancy.overview") : t("reseller.ledger")}
          </button>
        ))}
      </div>

      {tab === "ledger" ? <MyLedger /> : <Overview record={record} atFloor={atFloor} />}
    </section>
  );
}

function Overview({ record, atFloor }: { record: MyTenancy; atFloor: boolean }) {
  return (
    <>
      <dl className="grid grid-cols-2 gap-2 text-sm">
        <dt className="text-xs text-zinc-400">{t("reseller.displayName")}</dt>
        <dd className="font-mono">{record.display_name}</dd>
        <dt className="text-xs text-zinc-400">{t("reseller.status")}</dt>
        <dd>
          {record.enabled ? (
            <span className="text-green-500">{t("reseller.enabled")}</span>
          ) : (
            <span className="text-zinc-500">{t("reseller.disabled")}</span>
          )}
        </dd>
        <dt className="text-xs text-zinc-400">{t("reseller.balance")}</dt>
        <dd className={atFloor ? "font-mono text-amber-400" : "font-mono text-green-400"}>
          {formatNumber(record.balance)}
        </dd>
        <dt className="text-xs text-zinc-400">{t("reseller.creditFloor")}</dt>
        <dd className="font-mono">{formatNumber(record.credit_floor)}</dd>
      </dl>
      {atFloor && (
        <p className="mt-3 text-xs text-amber-400" role="status">
          {t("tenancy.atFloor")}
        </p>
      )}
      {!record.enabled && (
        <p className="mt-3 text-xs text-amber-400" role="status">
          {t("tenancy.deactivated")}
        </p>
      )}
    </>
  );
}

/**
 * A tenant's own movements, from /me/reseller/ledger.
 *
 * The route takes no reseller id -- it resolves the tenancy from the session --
 * so there is nothing to pass and nothing a tenant could substitute to reach
 * another's history. The platform route stays 403 for them.
 *
 * Fetched only when the tab is opened, which is why it is a separate component:
 * most visits to Profile are not about billing.
 */
function MyLedger() {
  const ledger = useQuery({
    queryKey: ["my-tenancy", "ledger"],
    queryFn: () => api.get<{ movements: Movement[] }>("/api/v1/me/reseller/ledger"),
    retry: false,
  });

  if (ledger.isError) {
    const message =
      ledger.error instanceof ApiError ? ledger.error.message : String(ledger.error);
    return (
      <p className="text-xs text-red-400" role="alert">
        {message}
      </p>
    );
  }
  if (!ledger.data) return <p className="text-sm text-zinc-500">{t("common.loading")}</p>;
  return <MovementsTable movements={ledger.data.movements} />;
}
