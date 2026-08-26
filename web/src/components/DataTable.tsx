import { useMemo, useState } from "react";
import type { ReactNode } from "react";
import { ArrowDown, ArrowUp, ChevronsUpDown, SlidersHorizontal } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";
import { t } from "@/i18n";

export interface Column<T> {
  id: string;
  /** Already translated by the caller; this component holds no copy of its own. */
  header: string;
  cell: (row: T) => ReactNode;
  /**
   * Supplying this makes the column sortable. It returns the value to compare,
   * not a comparator, so every column sorts by the same rules -- nulls last,
   * numbers numerically, strings by locale.
   */
  sortValue?: (row: T) => string | number | null;
  /** false pins the column visible. Defaults to hideable. */
  hideable?: boolean;
  /** Extra classes for the cells in this column. */
  className?: string;
}

type Direction = "asc" | "desc";

/**
 * DataTable: one table with sorting and column visibility, so every list in the
 * panel behaves the same way.
 *
 * The screens each hand-rolled a <table>, which meant none of them could sort
 * and all of them showed every column whether or not it was useful on a laptop.
 * More quietly, they made the whole row clickable with onClick and no keyboard
 * path, so a list of nodes could be read without a mouse and not opened. The
 * caller puts a link in one cell for that; this adds Enter and Space on the row
 * so the mouse affordance is not the only one.
 *
 * Sorting is CLIENT-side, which is honest for the sizes here -- the panel
 * already fetches whole lists -- and would be wrong the moment a list is paged.
 * When that changes this needs to hand sorting to the server rather than grow a
 * second, silently different, ordering.
 */
export function DataTable<T>({
  rows,
  columns,
  rowKey,
  onRowActivate,
  storageKey,
  empty,
  caption,
}: {
  rows: T[];
  columns: Column<T>[];
  rowKey: (row: T) => string | number;
  /** Called on click, Enter or Space. Omit for a table that is only read. */
  onRowActivate?: (row: T) => void;
  /**
   * Persists which columns are hidden. Omit and the choice lasts one visit --
   * which is worse than not offering it, so callers that show the control
   * should pass one.
   */
  storageKey?: string;
  empty: string;
  caption?: string;
}) {
  const [sort, setSort] = useState<{ id: string; dir: Direction } | null>(null);
  const [hidden, setHidden] = useState<Set<string>>(() => loadHidden(storageKey));

  const visible = columns.filter((c) => !hidden.has(c.id));

  const sorted = useMemo(() => {
    if (!sort) return rows;
    const column = columns.find((c) => c.id === sort.id);
    if (!column?.sortValue) return rows;
    const read = column.sortValue;
    // A copy: sorting the array the caller handed us would reorder their state.
    return [...rows].sort((a, b) => {
      const av = read(a);
      const bv = read(b);
      // Nulls last in both directions. A node that has never been seen belongs
      // at the bottom whichever way the column is pointing, because "unknown"
      // is not the smallest value -- it is not a value.
      if (av === null && bv === null) return 0;
      if (av === null) return 1;
      if (bv === null) return -1;
      const cmp =
        typeof av === "number" && typeof bv === "number"
          ? av - bv
          : String(av).localeCompare(String(bv));
      return sort.dir === "asc" ? cmp : -cmp;
    });
  }, [rows, columns, sort]);

  function toggleSort(id: string) {
    setSort((current) => {
      if (current?.id !== id) return { id, dir: "asc" };
      // asc -> desc -> off, so an operator can get back to the order the
      // server sent without reloading.
      if (current.dir === "asc") return { id, dir: "desc" };
      return null;
    });
  }

  function toggleColumn(id: string) {
    setHidden((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      saveHidden(storageKey, next);
      return next;
    });
  }

  const hideable = columns.filter((c) => c.hideable !== false);

  return (
    <div className="space-y-2">
      {hideable.length > 0 && (
        <div className="flex justify-end">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="outline" size="sm">
                <SlidersHorizontal />
                {t("table.columns")}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent>
              <DropdownMenuLabel>{t("table.columns")}</DropdownMenuLabel>
              <DropdownMenuSeparator />
              {hideable.map((column) => (
                <DropdownMenuCheckboxItem
                  key={column.id}
                  checked={!hidden.has(column.id)}
                  // Radix would close the menu on select; keeping it open lets
                  // an operator turn three columns off in one go.
                  onSelect={(e) => e.preventDefault()}
                  onCheckedChange={() => toggleColumn(column.id)}
                >
                  {column.header}
                </DropdownMenuCheckboxItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      )}

      <table className="w-full border-collapse text-sm">
        {caption && <caption className="sr-only">{caption}</caption>}
        <thead>
          <tr className="border-b border-border text-xs uppercase tracking-wide text-muted-foreground">
            {visible.map((column) => {
              const active = sort?.id === column.id;
              return (
                <th
                  key={column.id}
                  className={cn("py-2 pe-3 text-start font-medium", column.className)}
                  // Announced by screen readers as the sort state of the
                  // column, which an icon alone does not convey.
                  aria-sort={
                    active ? (sort.dir === "asc" ? "ascending" : "descending") : undefined
                  }
                >
                  {column.sortValue ? (
                    <button
                      type="button"
                      onClick={() => toggleSort(column.id)}
                      className="inline-flex items-center gap-1 hover:text-foreground"
                    >
                      {column.header}
                      {active ? (
                        sort.dir === "asc" ? (
                          <ArrowUp className="size-3" />
                        ) : (
                          <ArrowDown className="size-3" />
                        )
                      ) : (
                        <ChevronsUpDown className="size-3 opacity-40" />
                      )}
                    </button>
                  ) : (
                    column.header
                  )}
                </th>
              );
            })}
          </tr>
        </thead>
        <tbody>
          {sorted.length === 0 ? (
            <tr>
              <td
                colSpan={visible.length}
                className="py-6 text-center text-sm text-muted-foreground"
              >
                {empty}
              </td>
            </tr>
          ) : (
            sorted.map((row) => (
              <tr
                key={rowKey(row)}
                onClick={onRowActivate ? () => onRowActivate(row) : undefined}
                onKeyDown={
                  onRowActivate
                    ? (e) => {
                        if (e.key === "Enter" || e.key === " ") {
                          // Space scrolls the page otherwise, which moves the
                          // row out from under the operator as it activates.
                          e.preventDefault();
                          onRowActivate(row);
                        }
                      }
                    : undefined
                }
                tabIndex={onRowActivate ? 0 : undefined}
                className={cn(
                  "border-b border-border/50",
                  onRowActivate && "cursor-pointer hover:bg-accent/50",
                )}
              >
                {visible.map((column) => (
                  <td key={column.id} className={cn("py-1.5 pe-3", column.className)}>
                    {column.cell(row)}
                  </td>
                ))}
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}

const STORAGE_PREFIX = "antimage.table.hidden.";

function loadHidden(storageKey?: string): Set<string> {
  if (!storageKey) return new Set();
  try {
    const raw = localStorage.getItem(STORAGE_PREFIX + storageKey);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    return Array.isArray(parsed) ? new Set(parsed.filter((v) => typeof v === "string")) : new Set();
  } catch {
    // A corrupt preference is not worth failing a screen over; showing every
    // column is the safe reading of "we do not know what you hid".
    return new Set();
  }
}

function saveHidden(storageKey: string | undefined, hidden: Set<string>) {
  if (!storageKey) return;
  try {
    localStorage.setItem(STORAGE_PREFIX + storageKey, JSON.stringify([...hidden]));
  } catch {
    // Private browsing and full quotas both land here. The table still works;
    // the choice just does not outlive the visit.
  }
}
