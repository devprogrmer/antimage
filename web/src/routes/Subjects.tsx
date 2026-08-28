import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../lib/api";
import { formatTimestamp, t } from "../i18n";
import { Link } from "react-router-dom";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { DataTable } from "../components/DataTable";
import type { Column } from "../components/DataTable";
import { MutationError } from "./Resellers";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { SubjectFilters, searchParamsFor } from "../components/SubjectFilters";
import { BulkActions } from "../components/BulkActions";
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
  created_at: number;
  note: string;
  quota_bytes: number | null;
  quota_used_bytes: number;
  frozen: boolean;
}

export function Subjects({ onSelect }: { onSelect: (id: number) => void }) {
  const queryClient = useQueryClient();
  const [showCreate, setShowCreate] = useState(false);
  // The subject the operator is being asked about. Named in the dialog,
  // because deleting the wrong customer is not recoverable from the UI.
  const [pendingDelete, setPendingDelete] = useState<Subject | null>(null);
  const [selected, setSelected] = useState<number[]>([]);

  // The filter state the bar reports. Kept here rather than inside the bar so
  // it is part of the query key: the list refetches when a filter changes,
  // which is the whole point of a server-side search.
  const [filters, setFilters] = useState<FilterParams | null>(null);

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

  const deleteSubject = useMutation({
    mutationFn: (id: number) => api.del(`/api/v1/subjects/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["subjects"] });
      setPendingDelete(null);
    },
  });

  const columns: Column<Subject>[] = [
    {
      id: "select",
      header: t("subject.selected"),
      hideable: false,
      cell: (s) => (
        <input
          type="checkbox"
          checked={selected.includes(s.id)}
          aria-label={s.name}
          onClick={(e) => e.stopPropagation()}
          onChange={(e) => {
            e.stopPropagation();
            setSelected((cur) =>
              e.target.checked ? [...cur, s.id] : cur.filter((id) => id !== s.id),
            );
          }}
        />
      ),
    },
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
      sortValue: (s) => (!s.enabled ? 2 : s.expired_at ? 1 : 0),
      cell: (s) => <StatusBadge subject={s} />,
    },
    {
      id: "quota",
      header: t("subject.quota"),
      sortValue: (s) => s.quota_used_bytes,
      className: "font-mono text-xs",
      cell: (s) =>
        s.quota_bytes
          ? `${Math.round(s.quota_used_bytes / 1_048_576)} / ${Math.round(s.quota_bytes / 1_048_576)} MB`
          : t("filters.unlimited"),
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
        <button
          type="button"
          onClick={() => setShowCreate(true)}
          className="rounded bg-primary px-3 py-1 text-sm hover:bg-primary/90"
        >
          {t("subjects.create")}
        </button>
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

      <BulkActions
        selectedIds={selected}
        onClearSelection={() => setSelected([])}
        onAction={async (action, params) => {
          const body = { subject_ids: selected, ...(params ?? {}) };
          if (action === "enable") await api.post("/api/v1/subjects/bulk/enable", body);
          else if (action === "disable") await api.post("/api/v1/subjects/bulk/disable", body);
          else if (action === "delete") await api.post("/api/v1/subjects/bulk/delete", body);
          else if (action === "extend") await api.post("/api/v1/subjects/bulk/extend", body);
          else if (action === "reset-traffic")
            await api.post("/api/v1/subjects/bulk/reset-traffic", body);
          else if (action === "set-quota")
            await api.post("/api/v1/subjects/bulk/set-quota", body);
          await queryClient.invalidateQueries({ queryKey: ["subjects"] });
          setSelected([]);
        }}
      />
      <SubjectFilters onFilterChange={setFilters} />

      <DataTable
        rows={subjects.data?.subjects ?? []}
        columns={columns}
        rowKey={(s) => s.id}
        onRowActivate={(s) => onSelect(s.id)}
        storageKey="subjects"
        empty={t("subjects.none")}
        caption={t("subjects.title")}
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

function StatusBadge({ subject }: { subject: Subject }) {
  if (!subject.enabled) {
    return <span className="text-muted-foreground">{t("subject.disabled")}</span>;
  }
  if (subject.expired_at) {
    return <span className="text-warning">{t("subject.expired")}</span>;
  }
  return <span className="text-success">{t("subject.active")}</span>;
}

function CreateSubjectForm({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [note, setNote] = useState("");
  const [expireDays, setExpireDays] = useState("");
  const [quotaGB, setQuotaGB] = useState("");
  const [serviceIDs, setServiceIDs] = useState<number[]>([]);

  const services = useQuery({
    queryKey: ["services-catalog"],
    queryFn: () =>
      api.get<{
        services: Array<{
          id: number;
          node_name: string;
          adapter_kind: string;
          params: { protocol?: string; port?: number };
        }>;
      }>("/api/v1/services"),
  });

  const create = useMutation({
    mutationFn: () =>
      api.post("/api/v1/subjects", {
        name,
        note,
        service_ids: serviceIDs,
        expire_days: expireDays ? Number(expireDays) : undefined,
        quota_bytes: quotaGB ? Number(quotaGB) * 1024 * 1024 * 1024 : undefined,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["subjects"] });
      onClose();
    },
  });

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
        <div>
          <label className="block text-xs text-muted-foreground" htmlFor="subject-expire">
            {t("subject.expireDays")}
          </label>
          <Input
            id="subject-expire"
            type="number"
            min={0}
            value={expireDays}
            onChange={(e) => setExpireDays(e.target.value)}
            placeholder={t("filters.unlimited")}
          />
        </div>
        <div>
          <label className="block text-xs text-muted-foreground" htmlFor="subject-quota">
            {t("subject.quotaGB")}
          </label>
          <Input
            id="subject-quota"
            type="number"
            min={0}
            value={quotaGB}
            onChange={(e) => setQuotaGB(e.target.value)}
            placeholder={t("filters.unlimited")}
          />
        </div>
        <fieldset>
          <legend className="mb-1 block text-xs text-muted-foreground">{t("subject.inbounds")}</legend>
          <div className="max-h-40 space-y-1 overflow-y-auto rounded-md border border-border p-2">
            {(services.data?.services ?? []).length === 0 ? (
              <p className="text-xs text-muted-foreground">{t("subject.noInbounds")}</p>
            ) : (
              (services.data?.services ?? []).map((s) => (
                <label key={s.id} className="flex items-center gap-2 text-xs">
                  <input
                    type="checkbox"
                    checked={serviceIDs.includes(s.id)}
                    onChange={(e) =>
                      setServiceIDs((cur) =>
                        e.target.checked ? [...cur, s.id] : cur.filter((id) => id !== s.id),
                      )
                    }
                  />
                  {s.node_name} · {s.adapter_kind}
                  {s.params?.protocol ? `/${s.params.protocol}` : ""}
                  {s.params?.port ? `:${s.params.port}` : ""}
                </label>
              ))
            )}
          </div>
        </fieldset>
        <MutationError error={create.error} />
        <div className="flex gap-2">
          <Button
            size="sm"
            onClick={() => create.mutate()}
            disabled={!name || create.isPending}
          >
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
