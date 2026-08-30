import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { api } from "../lib/api";
import { formatNumber, formatTimestamp, t } from "../i18n";
import { Link } from "react-router-dom";
import { BulkActions } from "../components/BulkActions";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { DataTable } from "../components/DataTable";
import type { Column } from "../components/DataTable";
import { MutationError } from "./Resellers";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { SubjectFilters, searchParamsFor } from "../components/SubjectFilters";
import { SubjectTransfer } from "../components/SubjectTransfer";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "../components/ui/sheet";
import type { FilterParams } from "../components/SubjectFilters";

interface Subject {
  id: number;
  name: string;
  enabled: boolean;
  expires_at: number | null;
  expired_at: number | null;
  frozen_at: number | null;
  /** Derived server-side from enabled/frozen/expiry/on-hold. */
  status: SubjectStatus;
  on_hold_seconds: number | null;
  created_at: number;
  note: string;
}

type SubjectStatus = "active" | "on_hold" | "expired" | "disabled" | "frozen";

/** A saved plan: quota, validity, and whether it starts on first use. */
interface Plan {
  id: number;
  name: string;
  quota_bytes: number | null;
  validity_days: number | null;
  on_hold: boolean;
}

/** Display order for the status column: worst-to-best, so the rows an operator
 *  is looking for sort together regardless of language. */
const STATUS_RANK: Record<SubjectStatus, number> = {
  frozen: 4,
  disabled: 3,
  expired: 2,
  on_hold: 1,
  active: 0,
};

export function Subjects({ onSelect }: { onSelect: (id: number) => void }) {
  const queryClient = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  // The subject the operator is being asked about. Named in the dialog,
  // because deleting the wrong customer is not recoverable from the UI.
  const [pendingDelete, setPendingDelete] = useState<Subject | null>(null);

  // The filter state the bar reports. Kept here rather than inside the bar so
  // it is part of the query key: the list refetches when a filter changes,
  // which is the whole point of a server-side search.
  const [filters, setFilters] = useState<FilterParams | null>(null);

  // Selection for the bulk bar.
  const [selected, setSelected] = useState<Set<string | number>>(new Set());

  // /api/v2/subjects, not v1. The v2 endpoint is the paginated, filterable,
  // scope-checked search -- it and SubjectFilters were both written and never
  // connected, so the panel shipped a filter bar that rendered nowhere and a
  // search API that nothing called.
  const subjects = useQuery({
    queryKey: ["subjects", filters],
    queryFn: () =>
      api.get<{ subjects: Subject[]; total: number }>(
        "/api/v2/subjects?" + searchParamsFor(filters),
      ),
  });

  // Keep the selection to what is actually on screen.
  //
  // The invariant is "never act on a row the operator cannot see" -- filters
  // run server-side, so a narrowed list can hide something already ticked. The
  // first attempt at this cleared the whole selection whenever the filter bar
  // reported, which looked equivalent and was not: the bar reports its own
  // initial state shortly after mount, so ticking rows quickly meant watching
  // them silently untick a moment later. Pruning against the rows actually
  // returned enforces the same rule without depending on when the bar speaks.
  const rows = subjects.data?.subjects;
  useEffect(() => {
    if (rows === undefined) return;
    setSelected((prev) => {
      if (prev.size === 0) return prev;
      const visible = new Set<string | number>(rows.map((r) => r.id));
      const next = new Set([...prev].filter((id) => visible.has(id)));
      // Same set means the same object, or this schedules a render every fetch.
      return next.size === prev.size ? prev : next;
    });
  }, [rows]);

  const deleteSubject = useMutation({
    mutationFn: (id: number) => api.del(`/api/v1/subjects/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["subjects"] });
      setPendingDelete(null);
    },
  });

  const columns: Column<Subject>[] = [
    {
      id: "name",
      header: t("subject.name"),
      sortValue: (s) => s.name,
      // Pinned: the link is the keyboard path to the detail screen, and a row
      // with no name cannot be told from the one above it.
      hideable: false,
      cell: (s) => (
        <Link
          to={`/subjects/${s.id}`}
          onClick={(e) => e.stopPropagation()}
          className="font-mono hover:underline"
        >
          {s.name}
        </Link>
      ),
    },
    {
      id: "status",
      header: t("subject.status"),
      // Sorted by the state an operator is looking for, not alphabetically by
      // its label -- which would order it differently in every language.
      sortValue: (s) => STATUS_RANK[s.status] ?? 0,
      cell: (s) => <StatusBadge subject={s} />,
    },
    {
      id: "expires",
      header: t("subject.expires"),
      sortValue: (s) => s.expires_at,
      className: "font-mono text-xs text-muted-foreground",
      cell: (s) => formatTimestamp(s.expires_at),
    },
    {
      id: "created",
      header: t("subject.created"),
      sortValue: (s) => s.created_at,
      className: "font-mono text-xs text-muted-foreground",
      cell: (s) => formatTimestamp(s.created_at),
    },
    {
      id: "actions",
      header: t("actions"),
      cell: (s) => (
        <button
          type="button"
          onClick={(e) => {
            // The row activates on click; without this, asking to delete would
            // also navigate to the record being deleted.
            e.stopPropagation();
            setPendingDelete(s);
          }}
          className="text-xs text-destructive hover:underline"
        >
          {t("delete")}
        </button>
      ),
    },
  ];

  return (
    <div>
      <div className="mb-4 flex items-center justify-between">
        <h2 className="text-lg font-semibold">{t("subjects.title")}</h2>
        <div className="flex flex-wrap items-center gap-2">
          <SubjectTransfer />
          <Button size="sm" onClick={() => setShowCreate(true)}>
            {t("subjects.create")}
          </Button>
        </div>
      </div>

      {/* In a sheet rather than inline. The form used to appear between the
          header and the table and push the list down the page, so the rows an
          operator was reading moved as they clicked New. It also had no focus
          management: the form opened and the keyboard stayed on the button
          behind it. */}
      <Sheet open={showCreate} onOpenChange={setShowCreate}>
        <SheetContent aria-describedby={undefined}>
          <SheetHeader>
            <SheetTitle>{t("subjects.create")}</SheetTitle>
          </SheetHeader>
          <CreateSubjectForm onClose={() => setShowCreate(false)} />
        </SheetContent>
      </Sheet>

      <SubjectFilters onFilterChange={setFilters} />

      <BulkActions
        selectedIds={[...selected].map(Number)}
        onClearSelection={() => setSelected(new Set())}
      />

      <DataTable
        rows={subjects.data?.subjects ?? []}
        columns={columns}
        rowKey={(s) => s.id}
        onRowActivate={(s) => onSelect(s.id)}
        storageKey="subjects"
        empty={t("subjects.none")}
        caption={t("subjects.title")}
        selected={selected}
        onSelectedChange={setSelected}
        selectionLabel={(s) => s.name}
      />
      <ConfirmDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => !open && setPendingDelete(null)}
        title={t("subject.confirmDelete")}
        description={pendingDelete?.name}
        confirmLabel={t("delete")}
        pending={deleteSubject.isPending}
        onConfirm={() => pendingDelete && deleteSubject.mutate(pendingDelete.id)}
      />
    </div>
  );
}

/**
 * The status the server derived, not one re-derived here.
 *
 * This used to reconstruct the state from enabled/frozen_at/expired_at, which
 * meant the precedence lived in two places -- and it disagreed with the detail
 * screen about which of frozen and disabled to show. The server sends one word
 * now; this only chooses how to paint it.
 */
function StatusBadge({ subject }: { subject: Subject }) {
  switch (subject.status) {
    case "frozen":
      return <span className="text-warning">{t("subject.frozen")}</span>;
    case "disabled":
      return <span className="text-muted-foreground">{t("subject.disabled")}</span>;
    case "expired":
      return <span className="text-warning">{t("subject.expired")}</span>;
    // Not an error state: sold and waiting for its first use. Muted rather
    // than green, because it is not carrying traffic yet.
    case "on_hold":
      return <span className="text-muted-foreground">{t("subject.onHold")}</span>;
    default:
      return <span className="text-success">{t("subject.active")}</span>;
  }
}

const SECONDS_PER_DAY = 24 * 60 * 60;

function CreateSubjectForm({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [note, setNote] = useState("");
  // Whether the plan starts now or on the customer's first use. Held as a
  // boolean rather than inferred from a filled-in duration, so clearing the
  // field to retype it does not silently switch the sale back.
  const [onHold, setOnHold] = useState(false);
  const [holdDays, setHoldDays] = useState("30");
  const [presetID, setPresetID] = useState("");

  // The plans an operator may sell: public ones plus their own. The server
  // enforces that on every read, so this list is what they can actually apply.
  const presets = useQuery({
    queryKey: ["presets", "users"],
    queryFn: () => api.get<{ presets: Plan[] }>("/api/v1/presets/users"),
  });

  const create = useMutation({
    mutationFn: (data: {
      name: string;
      note: string;
      service_ids: number[];
      on_hold_seconds?: number;
    }) => api.post("/api/v1/subjects", data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["subjects"] });
      onClose();
    },
  });

  const days = Number(holdDays);
  const daysValid = holdDays.trim() !== "" && Number.isInteger(days) && days > 0;
  const canSubmit = name !== "" && (!onHold || daysValid) && !create.isPending;

  function submit() {
    create.mutate({
      name,
      note,
      service_ids: [],
      // Omitted entirely when the plan starts now. Sending 0 or null would
      // reach a server that rejects a non-positive duration, turning an
      // ordinary sale into an error.
      ...(onHold ? { on_hold_seconds: days * SECONDS_PER_DAY } : {}),
      ...(presetID !== "" ? { preset_id: Number(presetID) } : {}),
    });
  }

  return (
    // No card or heading of its own: the sheet supplies both, and nesting a
    // bordered panel inside a panel is the doubled chrome that makes a UI look
    // assembled rather than designed.
    <div>
      <div className="space-y-3">
        <div>
          <label className="block text-xs text-muted-foreground" htmlFor="subject-name">
            {t("subject.name")}
          </label>
          <Input
            id="subject-name"
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </div>
        <div>
          <label className="block text-xs text-muted-foreground" htmlFor="subject-note">
            {t("subject.note")}
          </label>
          <Input
            id="subject-note"
            type="text"
            value={note}
            onChange={(e) => setNote(e.target.value)}
          />
        </div>
        {/* Plans were a catalogue with no way to sell from it: user_presets had
            quota, validity and auto-assigned services, and nothing applied one
            to a subject. */}
        <div>
          <label className="block text-xs text-muted-foreground" htmlFor="subject-plan">
            {t("subject.plan")}
          </label>
          <select
            id="subject-plan"
            value={presetID}
            onChange={(e) => setPresetID(e.target.value)}
            className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
          >
            <option value="">{t("subject.noPlan")}</option>
            {(presets.data?.presets ?? []).map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
                {p.validity_days !== null &&
                  ` — ${t(p.on_hold ? "subject.planHoldDays" : "subject.planDays", {
                    days: formatNumber(p.validity_days),
                  })}`}
              </option>
            ))}
          </select>
          {presetID !== "" && (
            <p className="mt-1 text-xs text-muted-foreground">{t("subject.planHint")}</p>
          )}
        </div>

        {/* The commercial decision, made at the point of sale. A credential
            handed over today whose 30 days start today costs the customer
            whatever time passes before they set it up. Left alone, the plan
            decides it; set here, this wins. */}
        <fieldset className="rounded-md border border-border p-3">
          <legend className="px-1 text-xs text-muted-foreground">
            {t("subject.startWhen")}
          </legend>
          <label className="flex items-center gap-2 text-sm">
            <input
              type="radio"
              name="start-when"
              checked={!onHold}
              onChange={() => setOnHold(false)}
              className="accent-primary"
            />
            {t("subject.startNow")}
          </label>
          <label className="mt-1 flex items-center gap-2 text-sm">
            <input
              type="radio"
              name="start-when"
              checked={onHold}
              onChange={() => setOnHold(true)}
              className="accent-primary"
            />
            {t("subject.startOnFirstUse")}
          </label>
          {onHold && (
            <div className="mt-2">
              <label
                className="block text-xs text-muted-foreground"
                htmlFor="subject-hold-days"
              >
                {t("subject.validityDays")}
              </label>
              <Input
                id="subject-hold-days"
                type="number"
                inputMode="numeric"
                min={1}
                value={holdDays}
                onChange={(e) => setHoldDays(e.target.value)}
              />
              <p className="mt-1 text-xs text-muted-foreground">
                {t("subject.onHoldHint")}
              </p>
            </div>
          )}
        </fieldset>

        <MutationError error={create.error} />
        <div className="flex gap-2">
          <Button size="sm" onClick={submit} disabled={!canSubmit}>
            {t("create")}
          </Button>
          <Button variant="outline" size="sm" onClick={onClose}>
            {t("cancel")}
          </Button>
        </div>
      </div>
    </div>
  );
}
