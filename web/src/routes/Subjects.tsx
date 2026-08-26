import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../lib/api";
import { formatTimestamp, t } from "../i18n";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { MutationError } from "./Resellers";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { SubjectFilters, searchParamsFor } from "../components/SubjectFilters";
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
}

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

  return (
    <div>
      <div className="mb-4 flex items-center justify-between">
        <h2 className="text-lg font-semibold">{t("subjects.title")}</h2>
        <button
          type="button"
          onClick={() => setShowCreate(true)}
          className="rounded bg-blue-600 px-3 py-1 text-sm hover:bg-blue-700"
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

      <SubjectFilters onFilterChange={setFilters} />

      <table className="w-full border-collapse text-sm text-zinc-200">
        <thead>
          <tr className="border-b border-zinc-800 text-xs uppercase tracking-wide text-zinc-500">
            <th className="py-2 pe-3 text-start">{t("subject.name")}</th>
            <th className="pe-3 text-start">{t("subject.status")}</th>
            <th className="pe-3 text-start">{t("subject.expires")}</th>
            <th className="pe-3 text-start">{t("subject.created")}</th>
            <th className="text-start">{t("actions")}</th>
          </tr>
        </thead>
        <tbody>
          {subjects.data?.subjects.map((subject) => (
            <tr
              key={subject.id}
              className="cursor-pointer border-b border-zinc-900 hover:bg-zinc-900"
            >
              <td
                onClick={() => onSelect(subject.id)}
                className="py-1.5 pe-3 font-mono"
              >
                {subject.name}
              </td>
              <td className="pe-3">
                <StatusBadge subject={subject} />
              </td>
              <td className="pe-3 font-mono text-xs text-zinc-500">
                {formatTimestamp(subject.expires_at)}
              </td>
              <td className="pe-3 font-mono text-xs text-zinc-500">
                {formatTimestamp(subject.created_at)}
              </td>
              <td>
                <button
                  type="button"
                  onClick={() => setPendingDelete(subject)}
                  className="text-xs text-destructive hover:underline"
                >
                  {t("delete")}
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
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
    return <span className="text-zinc-500">{t("subject.disabled")}</span>;
  }
  if (subject.expired_at) {
    return <span className="text-amber-500">{t("subject.expired")}</span>;
  }
  return <span className="text-green-500">{t("subject.active")}</span>;
}

function CreateSubjectForm({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [note, setNote] = useState("");

  const create = useMutation({
    mutationFn: (data: { name: string; note: string; service_ids: number[] }) =>
      api.post("/api/v1/subjects", data),
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
        <MutationError error={create.error} />
        <div className="flex gap-2">
          <Button
            size="sm"
            onClick={() => create.mutate({ name, note, service_ids: [] })}
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
