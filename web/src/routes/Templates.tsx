import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { MutationError } from "./Resellers";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../components/ui/tabs";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "../components/ui/sheet";
import { formatNumber, formatTimestamp, t } from "../i18n";

interface ServiceTemplate {
  id: number;
  name: string;
  adapter_kind: string;
  params_json: string;
  description: string;
  tags: string[];
  is_public: boolean;
  created_by: number | null;
  created_at: number;
}

interface UserPreset {
  id: number;
  name: string;
  description: string;
  quota_bytes: number | null;
  validity_days: number | null;
  is_public: boolean;
  created_by: number | null;
  created_at: number;
}

/**
 * Saved inbound configurations and saved subject defaults.
 *
 * Ten endpoints with no client. They are ownership-scoped in the service layer
 * rather than permission-gated: a caller sees public entries plus their own,
 * and may only change what they created unless they are a super admin. So this
 * screen offers edit and delete on every row it shows and lets the server
 * refuse -- the alternative is duplicating an ownership rule in the browser
 * that would drift from the one that counts.
 */
export function Templates() {
  return (
    <div className="space-y-4">
      <h2 className="text-lg font-semibold">{t("templates.title")}</h2>
      <Tabs defaultValue="services">
        <TabsList>
          <TabsTrigger value="services">{t("templates.services")}</TabsTrigger>
          <TabsTrigger value="presets">{t("templates.presets")}</TabsTrigger>
        </TabsList>
        <TabsContent value="services">
          <ServiceTemplates />
        </TabsContent>
        <TabsContent value="presets">
          <UserPresets />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function ServiceTemplates() {
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [pendingDelete, setPendingDelete] = useState<ServiceTemplate | null>(null);

  const list = useQuery({
    queryKey: ["templates", "services"],
    queryFn: () =>
      api.get<{ templates: ServiceTemplate[] }>("/api/v1/templates/services"),
  });

  const remove = useMutation({
    mutationFn: (id: number) => api.del(`/api/v1/templates/services/${id}`),
    onSuccess: () => {
      setPendingDelete(null);
      queryClient.invalidateQueries({ queryKey: ["templates", "services"] });
    },
  });

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <Button size="sm" onClick={() => setCreating(true)}>
          {t("templates.newService")}
        </Button>
      </div>

      <MutationError error={list.error} />
      {list.data?.templates.length === 0 && (
        <p className="text-sm text-muted-foreground">{t("templates.noServices")}</p>
      )}

      {(list.data?.templates ?? []).map((tpl) => (
        <div key={tpl.id} className="rounded-lg border border-border bg-card p-3">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-sm">{tpl.name}</span>
            <Badge variant="outline">{tpl.adapter_kind}</Badge>
            {tpl.is_public && <Badge variant="secondary">{t("templates.public")}</Badge>}
            {tpl.tags?.map((tag) => (
              <Badge key={tag} variant="outline">
                {tag}
              </Badge>
            ))}
            <span className="ms-auto font-mono text-xs text-muted-foreground">
              {formatTimestamp(tpl.created_at)}
            </span>
            <button
              type="button"
              onClick={() => setPendingDelete(tpl)}
              className="text-xs text-destructive hover:underline"
            >
              {t("delete")}
            </button>
          </div>
          {tpl.description !== "" && (
            <p className="mt-1 text-xs text-muted-foreground">{tpl.description}</p>
          )}
          <pre className="mt-2 overflow-x-auto rounded border border-border bg-muted p-2 font-mono text-[11px]">
            {tpl.params_json}
          </pre>
        </div>
      ))}
      <MutationError error={remove.error} />

      <Sheet open={creating} onOpenChange={setCreating}>
        <SheetContent aria-describedby={undefined}>
          <SheetHeader>
            <SheetTitle>{t("templates.newService")}</SheetTitle>
          </SheetHeader>
          <CreateServiceTemplate onClose={() => setCreating(false)} />
        </SheetContent>
      </Sheet>

      <ConfirmDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => !open && setPendingDelete(null)}
        title={t("templates.confirmDelete")}
        description={pendingDelete?.name}
        confirmLabel={t("delete")}
        pending={remove.isPending}
        onConfirm={() => pendingDelete && remove.mutate(pendingDelete.id)}
      />
    </div>
  );
}

function CreateServiceTemplate({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [adapterKind, setAdapterKind] = useState("xray");
  const [params, setParams] = useState("{}");
  const [description, setDescription] = useState("");
  const [jsonError, setJsonError] = useState("");

  const create = useMutation({
    mutationFn: (body: unknown) => api.post("/api/v1/templates/services", body),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["templates", "services"] });
      onClose();
    },
  });

  function submit() {
    try {
      JSON.parse(params);
    } catch (err) {
      // Only a SYNTAX check. Whether the params suit the adapter is the
      // panel's answer when the template is applied, and duplicating that
      // judgement here would be a second validator that drifts from it.
      setJsonError(err instanceof Error ? err.message : String(err));
      return;
    }
    setJsonError("");
    create.mutate({
      name,
      adapter_kind: adapterKind,
      params_json: params,
      description,
    });
  }

  return (
    <div className="space-y-3">
      <div>
        <label className="block text-xs text-muted-foreground" htmlFor="tpl-name">
          {t("templates.name")}
        </label>
        <Input id="tpl-name" value={name} onChange={(e) => setName(e.target.value)} />
      </div>
      <div>
        <label className="block text-xs text-muted-foreground" htmlFor="tpl-kind">
          {t("studio.protocol")}
        </label>
        <Input
          id="tpl-kind"
          value={adapterKind}
          onChange={(e) => setAdapterKind(e.target.value)}
        />
      </div>
      <div>
        <label className="block text-xs text-muted-foreground" htmlFor="tpl-desc">
          {t("templates.description")}
        </label>
        <Input
          id="tpl-desc"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </div>
      <div>
        <label className="block text-xs text-muted-foreground" htmlFor="tpl-params">
          {t("studio.paramsDocument")}
        </label>
        <textarea
          id="tpl-params"
          value={params}
          onChange={(e) => setParams(e.target.value)}
          rows={10}
          spellCheck={false}
          className="w-full rounded-md border border-input bg-background px-2 py-1 font-mono text-xs"
        />
        {jsonError !== "" && (
          <p className="mt-1 text-xs text-destructive" role="alert">
            {jsonError}
          </p>
        )}
      </div>
      <MutationError error={create.error} />
      <div className="flex gap-2">
        <Button size="sm" onClick={submit} disabled={name === "" || create.isPending}>
          {t("create")}
        </Button>
        <Button size="sm" variant="outline" onClick={onClose}>
          {t("cancel")}
        </Button>
      </div>
    </div>
  );
}

function UserPresets() {
  const queryClient = useQueryClient();
  const [pendingDelete, setPendingDelete] = useState<UserPreset | null>(null);

  const list = useQuery({
    queryKey: ["presets", "users"],
    queryFn: () => api.get<{ presets: UserPreset[] }>("/api/v1/presets/users"),
  });

  const remove = useMutation({
    mutationFn: (id: number) => api.del(`/api/v1/presets/users/${id}`),
    onSuccess: () => {
      setPendingDelete(null);
      queryClient.invalidateQueries({ queryKey: ["presets", "users"] });
    },
  });

  return (
    <div className="space-y-3">
      <MutationError error={list.error} />
      {list.data?.presets.length === 0 && (
        <p className="text-sm text-muted-foreground">{t("templates.noPresets")}</p>
      )}

      {(list.data?.presets ?? []).map((preset) => (
        <div key={preset.id} className="rounded-lg border border-border bg-card p-3">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-sm">{preset.name}</span>
            {preset.is_public && <Badge variant="secondary">{t("templates.public")}</Badge>}
            <span className="ms-auto font-mono text-xs text-muted-foreground">
              {formatTimestamp(preset.created_at)}
            </span>
            <button
              type="button"
              onClick={() => setPendingDelete(preset)}
              className="text-xs text-destructive hover:underline"
            >
              {t("delete")}
            </button>
          </div>
          <dl className="mt-1 flex flex-wrap gap-x-6 gap-y-1 text-xs">
            <div className="flex gap-2">
              <dt className="text-muted-foreground">{t("reseller.quotaBytes")}</dt>
              {/* null is unlimited, not zero. Collapsing the two is how a
                  preset meaning "no cap" becomes one meaning "no traffic". */}
              <dd className="font-mono">
                {preset.quota_bytes === null
                  ? t("reseller.unlimited")
                  : formatNumber(preset.quota_bytes)}
              </dd>
            </div>
            <div className="flex gap-2">
              <dt className="text-muted-foreground">{t("templates.validity")}</dt>
              <dd className="font-mono">
                {preset.validity_days === null
                  ? t("reseller.unlimited")
                  : formatNumber(preset.validity_days)}
              </dd>
            </div>
          </dl>
          {preset.description !== "" && (
            <p className="mt-1 text-xs text-muted-foreground">{preset.description}</p>
          )}
        </div>
      ))}
      <MutationError error={remove.error} />

      <ConfirmDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => !open && setPendingDelete(null)}
        title={t("templates.confirmDeletePreset")}
        description={pendingDelete?.name}
        confirmLabel={t("delete")}
        pending={remove.isPending}
        onConfirm={() => pendingDelete && remove.mutate(pendingDelete.id)}
      />
    </div>
  );
}
