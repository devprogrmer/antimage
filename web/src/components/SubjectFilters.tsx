import { useState, useEffect, useRef } from "react";
import { t } from "../i18n";

export interface FilterParams {
  search: string;
  status: string;
  trafficMin: string;
  trafficMax: string;
  quotaStatus: string;
  expiresBefore: string;
  expiresAfter: string;
  sort: string;
  order: string;
}

/**
 * searchParamsFor turns the filter state into the query /api/v2/subjects reads.
 *
 * Empty values are OMITTED rather than sent blank: the handler trims and
 * ignores empties, but an omitted parameter is the honest statement of "no
 * opinion" and keeps the query key -- and so the cache entry -- stable.
 */
export function searchParamsFor(filters: FilterParams | null): string {
  const params = new URLSearchParams();
  if (!filters) return params.toString();
  const pairs: Array<[string, string]> = [
    ["search", filters.search],
    ["status", filters.status],
    ["traffic_min", filters.trafficMin],
    ["traffic_max", filters.trafficMax],
    ["quota_status", filters.quotaStatus],
    ["expires_before", filters.expiresBefore],
    ["expires_after", filters.expiresAfter],
    ["sort", filters.sort],
    ["order", filters.order],
  ];
  for (const [key, value] of pairs) {
    if (value !== "") params.set(key, value);
  }
  return params.toString();
}

interface SubjectFiltersProps {
  onFilterChange: (filters: FilterParams) => void;
}

export function SubjectFilters({ onFilterChange }: SubjectFiltersProps) {
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("");
  const [trafficMin, setTrafficMin] = useState("");
  const [trafficMax, setTrafficMax] = useState("");
  const [quotaStatus, setQuotaStatus] = useState("");
  const [expiresBefore, setExpiresBefore] = useState("");
  const [expiresAfter, setExpiresAfter] = useState("");
  const [sort, setSort] = useState("created");
  const [order, setOrder] = useState("desc");
  const timeoutRef = useRef<number | null>(null);

  useEffect(() => {
    if (timeoutRef.current) {
      clearTimeout(timeoutRef.current);
    }

    timeoutRef.current = setTimeout(() => {
      onFilterChange({
        search,
        status,
        trafficMin,
        trafficMax,
        quotaStatus,
        expiresBefore,
        expiresAfter,
        sort,
        order,
      });
    }, 300);

    return () => {
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
      }
    };
  }, [search, status, trafficMin, trafficMax, quotaStatus, expiresBefore, expiresAfter, sort, order, onFilterChange]);

  function clearFilters() {
    setSearch("");
    setStatus("");
    setTrafficMin("");
    setTrafficMax("");
    setQuotaStatus("");
    setExpiresBefore("");
    setExpiresAfter("");
    setSort("created");
    setOrder("desc");
  }

  const hasFilters = search || status || trafficMin || trafficMax || quotaStatus || expiresBefore || expiresAfter || sort !== "created" || order !== "desc";

  return (
    <div className="mb-4 rounded-lg border border-border bg-card p-4">
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
        <div>
          <label className="block mb-1 text-xs text-muted-foreground">{t('filters.search')}</label>
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t('filters.search_placeholder')}
            className="w-full px-3 py-1.5 rounded-md border border-input bg-background text-sm"
          />
        </div>

        <div>
          <label className="block mb-1 text-xs text-muted-foreground">{t('filters.status')}</label>
          <select
            value={status}
            onChange={(e) => setStatus(e.target.value)}
            className="w-full px-3 py-1.5 rounded-md border border-input bg-background text-sm"
          >
            <option value="">{t('filters.all')}</option>
            <option value="active">{t('filters.active')}</option>
            <option value="disabled">{t('filters.disabled')}</option>
            <option value="frozen">{t('filters.frozen')}</option>
            <option value="expired">{t('filters.expired')}</option>
          </select>
        </div>

        <div>
          <label className="block mb-1 text-xs text-muted-foreground">{t('filters.quota_status')}</label>
          <select
            value={quotaStatus}
            onChange={(e) => setQuotaStatus(e.target.value)}
            className="w-full px-3 py-1.5 rounded-md border border-input bg-background text-sm"
          >
            <option value="">{t('filters.all')}</option>
            <option value="under_limit">{t('filters.under_limit')}</option>
            <option value="near_limit">{t('filters.near_limit')}</option>
            <option value="over_limit">{t('filters.over_limit')}</option>
          </select>
        </div>

        <div>
          <label className="block mb-1 text-xs text-muted-foreground">{t('filters.sort_by')}</label>
          <div className="flex gap-2">
            <select
              value={sort}
              onChange={(e) => setSort(e.target.value)}
              className="flex-1 px-3 py-1.5 rounded-md border border-input bg-background text-sm"
            >
              <option value="created">{t('filters.sort_created')}</option>
              <option value="name">{t('filters.sort_name')}</option>
              <option value="expires">{t('filters.sort_expiry')}</option>
              <option value="traffic">{t('filters.sort_traffic')}</option>
              <option value="quota">{t('filters.sort_quota')}</option>
            </select>
            <button
              type="button"
              onClick={() => setOrder(order === "asc" ? "desc" : "asc")}
              className="px-3 py-1.5 bg-secondary border border-input rounded text-xs hover:bg-zinc-700"
            >
              {order === "asc" ? "↑" : "↓"}
            </button>
          </div>
        </div>

        <div>
          <label className="block mb-1 text-xs text-muted-foreground">{t('filters.traffic_min')}</label>
          <input
            type="number"
            value={trafficMin}
            onChange={(e) => setTrafficMin(e.target.value)}
            placeholder="0"
            min="0"
            className="w-full px-3 py-1.5 rounded-md border border-input bg-background text-sm"
          />
        </div>

        <div>
          <label className="block mb-1 text-xs text-muted-foreground">{t('filters.traffic_max')}</label>
          <input
            type="number"
            value={trafficMax}
            onChange={(e) => setTrafficMax(e.target.value)}
            placeholder={t('filters.unlimited')}
            min="0"
            className="w-full px-3 py-1.5 rounded-md border border-input bg-background text-sm"
          />
        </div>

        <div>
          <label className="block mb-1 text-xs text-muted-foreground">{t('filters.expires_after')}</label>
          <input
            type="date"
            value={expiresAfter}
            onChange={(e) => setExpiresAfter(e.target.value)}
            className="w-full px-3 py-1.5 rounded-md border border-input bg-background text-sm"
          />
        </div>

        <div>
          <label className="block mb-1 text-xs text-muted-foreground">{t('filters.expires_before')}</label>
          <input
            type="date"
            value={expiresBefore}
            onChange={(e) => setExpiresBefore(e.target.value)}
            className="w-full px-3 py-1.5 rounded-md border border-input bg-background text-sm"
          />
        </div>
      </div>

      {hasFilters && (
        <div className="mt-4 flex justify-end">
          <button
            type="button"
            onClick={clearFilters}
            className="px-4 py-1.5 text-xs text-muted-foreground hover:text-zinc-100"
          >
            {t('filters.clear_filters')}
          </button>
        </div>
      )}
    </div>
  );
}
