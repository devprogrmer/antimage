import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../lib/api";
import { formatTimestamp, t } from "../i18n";

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

  const subjects = useQuery({
    queryKey: ["subjects"],
    queryFn: () => api.get<{ subjects: Subject[] }>("/api/v1/subjects"),
  });

  const deleteSubject = useMutation({
    mutationFn: (id: number) => api.del(`/api/v1/subjects/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["subjects"] });
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

      {showCreate && (
        <CreateSubjectForm onClose={() => setShowCreate(false)} />
      )}

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
                  onClick={() => {
                    if (confirm(t("subject.confirmDelete"))) {
                      deleteSubject.mutate(subject.id);
                    }
                  }}
                  className="text-xs text-red-500 hover:text-red-400"
                >
                  {t("delete")}
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
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
    <div className="mb-4 rounded border border-zinc-800 bg-zinc-900 p-4">
      <h3 className="mb-3 text-sm font-semibold">{t("subjects.createNew")}</h3>
      <div className="space-y-3">
        <div>
          <label className="block text-xs text-zinc-400">{t("subject.name")}</label>
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            className="w-full rounded border border-zinc-700 bg-zinc-950 px-2 py-1 text-sm"
          />
        </div>
        <div>
          <label className="block text-xs text-zinc-400">{t("subject.note")}</label>
          <input
            type="text"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            className="w-full rounded border border-zinc-700 bg-zinc-950 px-2 py-1 text-sm"
          />
        </div>
        <div className="flex gap-2">
          <button
            type="button"
            onClick={() => create.mutate({ name, note, service_ids: [] })}
            disabled={!name || create.isPending}
            className="rounded bg-blue-600 px-3 py-1 text-sm hover:bg-blue-700 disabled:opacity-50"
          >
            {t("create")}
          </button>
          <button
            type="button"
            onClick={onClose}
            className="rounded bg-zinc-800 px-3 py-1 text-sm hover:bg-zinc-700"
          >
            {t("cancel")}
          </button>
        </div>
      </div>
    </div>
  );
}
