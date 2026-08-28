import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { can, useSession } from "@/lib/session";
import { t } from "@/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { MutationError } from "@/routes/Resellers";

export function Settings() {
  const session = useSession();
  const canWrite = can(session.data, "settings:write");
  const queryClient = useQueryClient();
  const settings = useQuery({
    queryKey: ["settings"],
    queryFn: () => api.get<{ settings: Record<string, string> }>("/api/v1/settings"),
  });
  const [publicUrl, setPublicUrl] = useState("");
  const [remark, setRemark] = useState("");
  const [brand, setBrand] = useState("");

  useEffect(() => {
    const s = settings.data?.settings ?? {};
    setPublicUrl(s.public_url ?? "");
    setRemark(s.remark_template ?? "");
    setBrand(s.brand_name ?? "");
  }, [settings.data]);

  const save = useMutation({
    mutationFn: () =>
      api.put("/api/v1/settings", {
        settings: {
          public_url: publicUrl,
          remark_template: remark,
          brand_name: brand,
        },
      }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["settings"] }),
  });

  return (
    <div className="max-w-xl space-y-8">
      <div>
        <h2 className="text-lg font-semibold">{t("settings.title")}</h2>
        <p className="text-sm text-muted-foreground">{t("settings.hint")}</p>
      </div>

      <div className="space-y-3 rounded-lg border border-border bg-card p-4">
        <div>
          <label className="block text-xs text-muted-foreground" htmlFor="public-url">
            {t("settings.publicUrl")}
          </label>
          <Input
            id="public-url"
            value={publicUrl}
            onChange={(e) => setPublicUrl(e.target.value)}
            placeholder="https://panel.example.com"
            disabled={!canWrite}
          />
          <p className="mt-1 text-xs text-muted-foreground">{t("settings.publicUrlHint")}</p>
        </div>
        <div>
          <label className="block text-xs text-muted-foreground" htmlFor="remark">
            {t("settings.remarkTemplate")}
          </label>
          <Input
            id="remark"
            value={remark}
            onChange={(e) => setRemark(e.target.value)}
            placeholder="{name} · {node}"
            disabled={!canWrite}
          />
          <p className="mt-1 text-xs text-muted-foreground">{t("settings.remarkTemplateHint")}</p>
        </div>
        <div>
          <label className="block text-xs text-muted-foreground" htmlFor="brand">
            {t("settings.brandName")}
          </label>
          <Input
            id="brand"
            value={brand}
            onChange={(e) => setBrand(e.target.value)}
            disabled={!canWrite}
          />
        </div>
        <MutationError error={save.error} />
        {canWrite && (
          <Button size="sm" disabled={save.isPending} onClick={() => save.mutate()}>
            {save.isSuccess ? t("common.saved") : t("save")}
          </Button>
        )}
      </div>

      {canWrite && (
        <div className="space-y-2 rounded-lg border border-border bg-card p-4">
          <h3 className="text-sm font-semibold">{t("settings.backup")}</h3>
          <p className="text-xs text-muted-foreground">{t("settings.backupHint")}</p>
          <a
            href="/api/v1/backup"
            className="inline-flex h-8 items-center rounded-md bg-secondary px-3 text-xs"
          >
            {t("settings.downloadBackup")}
          </a>
        </div>
      )}
    </div>
  );
}
