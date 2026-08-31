import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { MutationError } from "../routes/Resellers";
import { ConfirmDialog } from "./ConfirmDialog";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { t } from "../i18n";

/**
 * Subscription groups: a named protocol selection that decides what a
 * customer's subscription contains.
 *
 * Two things drive this in practice. Clients differ -- a build with no
 * Hysteria2 support gets entries it cannot use and picks one at random when
 * the list rotates -- and tiers differ, where the same nodes are sold twice
 * and the only thing separating the plans is what the subscription hands out.
 */

export interface Group {
  id: number;
  name: string;
  description: string;
  protocols: string[];
  is_public: boolean;
  created_by: number | null;
  created_at: number;
  updated_at: number;
}

interface GroupsResponse {
  groups: Group[];
  /** What the panel can actually produce, so the form cannot offer a protocol
   *  that would match nothing. */
  available_protocols: string[];
}

export function SubscriptionGroups() {
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState<Group | "new" | null>(null);
  const [pendingDelete, setPendingDelete] = useState<Group | null>(null);

  const groups = useQuery({
    queryKey: ["subscription-groups"],
    queryFn: () => api.get<GroupsResponse>("/api/v1/subscription-groups"),
  });

  const remove = useMutation({
    mutationFn: (id: number) => api.del(`/api/v1/subscription-groups/${id}`),
    onSuccess: () => {
      setPendingDelete(null);
      queryClient.invalidateQueries({ queryKey: ["subscription-groups"] });
    },
  });

  const available = groups.data?.available_protocols ?? [];

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold">{t("group.title")}</h3>
        {editing === null && (
          <Button size="sm" onClick={() => setEditing("new")}>
            {t("group.new")}
          </Button>
        )}
      </div>
      <p className="text-xs text-muted-foreground">{t("group.explain")}</p>

      <MutationError error={groups.error} />
      <MutationError error={remove.error} />

      {editing !== null && (
        <GroupForm
          group={editing === "new" ? undefined : editing}
          available={available}
          onClose={() => setEditing(null)}
        />
      )}

      {groups.data?.groups.length === 0 && (
        <p className="text-sm text-muted-foreground">{t("group.none")}</p>
      )}

      {(groups.data?.groups ?? []).map((g) => (
        <div key={g.id} className="rounded border border-border bg-background p-3">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-sm">{g.name}</span>
            {g.is_public && (
              <span className="rounded bg-secondary px-2 py-0.5 text-xs">
                {t("templates.public")}
              </span>
            )}
            <span className="ms-auto flex gap-2 text-xs">
              <button
                type="button"
                onClick={() => setEditing(g)}
                className="text-primary hover:underline"
              >
                {t("edit")}
              </button>
              <button
                type="button"
                onClick={() => setPendingDelete(g)}
                className="text-destructive hover:underline"
              >
                {t("delete")}
              </button>
            </span>
          </div>
          {g.description !== "" && (
            <p className="mt-1 text-xs text-muted-foreground">{g.description}</p>
          )}
          {/* Empty means everything. Saying so beats an empty row the operator
              has to interpret -- and the two readings are opposites. */}
          <p className="mt-1 text-xs">
            {g.protocols.length === 0 ? (
              <span className="text-muted-foreground">{t("group.carriesAll")}</span>
            ) : (
              <span className="font-mono">{g.protocols.join(", ")}</span>
            )}
          </p>
        </div>
      ))}

      <ConfirmDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => !open && setPendingDelete(null)}
        title={t("group.confirmDelete")}
        // Says what happens to the customers on it: deleting a tier must not
        // read as deleting its users.
        description={t("group.deleteWarning", { name: pendingDelete?.name ?? "" })}
        confirmLabel={t("delete")}
        pending={remove.isPending}
        onConfirm={() => pendingDelete && remove.mutate(pendingDelete.id)}
      />
    </div>
  );
}

function GroupForm({
  group,
  available,
  onClose,
}: {
  group?: Group;
  available: string[];
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [name, setName] = useState(group?.name ?? "");
  const [description, setDescription] = useState(group?.description ?? "");
  const [isPublic, setIsPublic] = useState(group?.is_public ?? false);
  const [protocols, setProtocols] = useState<Set<string>>(
    new Set(group?.protocols ?? []),
  );

  const save = useMutation({
    mutationFn: (body: unknown) =>
      group
        ? api.put(`/api/v1/subscription-groups/${group.id}`, body)
        : api.post("/api/v1/subscription-groups", body),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["subscription-groups"] });
      onClose();
    },
  });

  function toggle(p: string) {
    const next = new Set(protocols);
    if (next.has(p)) next.delete(p);
    else next.add(p);
    setProtocols(next);
  }

  return (
    <div className="rounded border border-border bg-background p-3">
      <div className="space-y-3">
        <div>
          <label className="block text-xs text-muted-foreground" htmlFor="group-name">
            {t("group.name")}
          </label>
          <Input id="group-name" value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <div>
          <label className="block text-xs text-muted-foreground" htmlFor="group-desc">
            {t("templates.description")}
          </label>
          <Input
            id="group-desc"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </div>

        <fieldset>
          <legend className="text-xs text-muted-foreground">{t("group.protocols")}</legend>
          <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1">
            {available.map((p) => (
              <label key={p} className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={protocols.has(p)}
                  onChange={() => toggle(p)}
                  className="size-4 accent-primary"
                />
                <span className="font-mono">{p}</span>
              </label>
            ))}
          </div>
          {/* The rule stated where the decision is made. Nothing selected is
              not "no access" -- it is "everything" -- and an operator who
              reads it the other way cuts their customers off. */}
          <p className="mt-1 text-xs text-muted-foreground">
            {protocols.size === 0 ? t("group.noneSelectedMeansAll") : t("group.selectedNote")}
          </p>
        </fieldset>

        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={isPublic}
            onChange={(e) => setIsPublic(e.target.checked)}
            className="size-4 accent-primary"
          />
          {t("group.public")}
        </label>

        <MutationError error={save.error} />

        <div className="flex gap-2">
          <Button
            size="sm"
            disabled={name.trim() === "" || save.isPending}
            onClick={() =>
              save.mutate({
                name,
                description,
                protocols: [...protocols],
                is_public: isPublic,
              })
            }
          >
            {group ? t("save") : t("create")}
          </Button>
          <Button size="sm" variant="outline" onClick={onClose}>
            {t("cancel")}
          </Button>
        </div>
      </div>
    </div>
  );
}

/** The group picker shown on a subject, so a customer can be put on a tier. */
export function SubjectGroupPicker({
  subjectId,
  current,
}: {
  subjectId: number;
  current: string[];
}) {
  const queryClient = useQueryClient();

  const groups = useQuery({
    queryKey: ["subscription-groups"],
    queryFn: () => api.get<GroupsResponse>("/api/v1/subscription-groups"),
  });

  // Which group the subject is on is not carried on the subject DTO; the
  // configs response reports the resulting protocol filter instead. Matching
  // on that is enough to preselect, and avoids a second field that could
  // disagree with the filter actually being applied.
  const matching = (groups.data?.groups ?? []).find(
    (g) => g.protocols.slice().sort().join(",") === current.slice().sort().join(","),
  );
  const [selected, setSelected] = useState<string>(
    current.length > 0 && matching ? String(matching.id) : "",
  );

  const assign = useMutation({
    mutationFn: (groupID: string) =>
      api.put(`/api/v1/subjects/${subjectId}/subscription-group`, {
        group_id: groupID === "" ? null : Number(groupID),
      }),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: ["subject", subjectId, "configs"] }),
  });

  return (
    <div className="mb-3">
      <label className="block text-xs text-muted-foreground" htmlFor="subject-group">
        {t("group.title")}
      </label>
      <div className="flex flex-wrap items-center gap-2">
        <select
          id="subject-group"
          value={selected}
          onChange={(e) => {
            setSelected(e.target.value);
            assign.mutate(e.target.value);
          }}
          className="h-9 rounded-md border border-input bg-background px-2 text-sm"
        >
          <option value="">{t("group.noGroup")}</option>
          {(groups.data?.groups ?? []).map((g) => (
            <option key={g.id} value={g.id}>
              {g.name}
            </option>
          ))}
        </select>
        {current.length > 0 && (
          <span className="font-mono text-xs text-muted-foreground">
            {current.join(", ")}
          </span>
        )}
      </div>
      <MutationError error={assign.error} />
    </div>
  );
}
