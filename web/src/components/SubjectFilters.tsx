import React, { useState, useEffect } from "react";

interface FilterParams {
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
  const [searchTimeout, setSearchTimeout] = useState<NodeJS.Timeout | null>(null);

  useEffect(() => {
    if (searchTimeout) {
      clearTimeout(searchTimeout);
    }

    const timeout = setTimeout(() => {
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

    setSearchTimeout(timeout);

    return () => {
      if (timeout) {
        clearTimeout(timeout);
      }
    };
  }, [search, status, trafficMin, trafficMax, quotaStatus, expiresBefore, expiresAfter, sort, order]);

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
    <div className="bg-zinc-900 border border-zinc-800 rounded p-4 mb-4">
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
        <div>
          <label className="block text-xs text-zinc-400 mb-1">Search</label>
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Name or note..."
            className="w-full px-3 py-1.5 bg-zinc-800 border border-zinc-700 rounded text-sm"
          />
        </div>

        <div>
          <label className="block text-xs text-zinc-400 mb-1">Status</label>
          <select
            value={status}
            onChange={(e) => setStatus(e.target.value)}
            className="w-full px-3 py-1.5 bg-zinc-800 border border-zinc-700 rounded text-sm"
          >
            <option value="">All</option>
            <option value="active">Active</option>
            <option value="disabled">Disabled</option>
            <option value="frozen">Frozen</option>
            <option value="expired">Expired</option>
          </select>
        </div>

        <div>
          <label className="block text-xs text-zinc-400 mb-1">Quota Status</label>
          <select
            value={quotaStatus}
            onChange={(e) => setQuotaStatus(e.target.value)}
            className="w-full px-3 py-1.5 bg-zinc-800 border border-zinc-700 rounded text-sm"
          >
            <option value="">All</option>
            <option value="under_limit">Under Limit</option>
            <option value="near_limit">Near Limit (80%+)</option>
            <option value="over_limit">Over Limit</option>
          </select>
        </div>

        <div>
          <label className="block text-xs text-zinc-400 mb-1">Sort By</label>
          <div className="flex gap-2">
            <select
              value={sort}
              onChange={(e) => setSort(e.target.value)}
              className="flex-1 px-3 py-1.5 bg-zinc-800 border border-zinc-700 rounded text-sm"
            >
              <option value="created">Created</option>
              <option value="name">Name</option>
              <option value="expires">Expiry</option>
              <option value="traffic">Traffic</option>
              <option value="quota">Quota</option>
            </select>
            <button
              type="button"
              onClick={() => setOrder(order === "asc" ? "desc" : "asc")}
              className="px-3 py-1.5 bg-zinc-800 border border-zinc-700 rounded text-xs hover:bg-zinc-700"
            >
              {order === "asc" ? "↑" : "↓"}
            </button>
          </div>
        </div>

        <div>
          <label className="block text-xs text-zinc-400 mb-1">Traffic Min (GB)</label>
          <input
            type="number"
            value={trafficMin}
            onChange={(e) => setTrafficMin(e.target.value)}
            placeholder="0"
            min="0"
            className="w-full px-3 py-1.5 bg-zinc-800 border border-zinc-700 rounded text-sm"
          />
        </div>

        <div>
          <label className="block text-xs text-zinc-400 mb-1">Traffic Max (GB)</label>
          <input
            type="number"
            value={trafficMax}
            onChange={(e) => setTrafficMax(e.target.value)}
            placeholder="Unlimited"
            min="0"
            className="w-full px-3 py-1.5 bg-zinc-800 border border-zinc-700 rounded text-sm"
          />
        </div>

        <div>
          <label className="block text-xs text-zinc-400 mb-1">Expires After</label>
          <input
            type="date"
            value={expiresAfter}
            onChange={(e) => setExpiresAfter(e.target.value)}
            className="w-full px-3 py-1.5 bg-zinc-800 border border-zinc-700 rounded text-sm"
          />
        </div>

        <div>
          <label className="block text-xs text-zinc-400 mb-1">Expires Before</label>
          <input
            type="date"
            value={expiresBefore}
            onChange={(e) => setExpiresBefore(e.target.value)}
            className="w-full px-3 py-1.5 bg-zinc-800 border border-zinc-700 rounded text-sm"
          />
        </div>
      </div>

      {hasFilters && (
        <div className="mt-4 flex justify-end">
          <button
            type="button"
            onClick={clearFilters}
            className="px-4 py-1.5 text-xs text-zinc-400 hover:text-zinc-100"
          >
            Clear Filters
          </button>
        </div>
      )}
    </div>
  );
}
