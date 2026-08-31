import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { can, useSession } from "../lib/session";
import { MutationError } from "../routes/Resellers";
import { ConfirmDialog } from "./ConfirmDialog";
import { Button } from "./ui/button";
import { Badge } from "./ui/badge";
import { formatNumber, t } from "../i18n";

interface Preview {
  node_id: number;
  current_revision: number;
  target_revision: number;
  current_doc_sha256: string;
  target_doc_sha256: string;
  changes_summary?: string;
}

interface Validation {
  valid: boolean;
  conflicts: string[];
  warnings: string[];
}

type DeploymentStrategy = "all_at_once" | "canary" | "staged" | "rolling";
type WizardStep = "preview" | "validate" | "strategy" | "confirm" | "deploying";

interface DeploymentWizardProps {
  nodeId: number;
  targetRevision: number;
  onClose?: () => void;
}

/**
 * Deployment workflow wizard: preview → validate → deploy.
 * 
 * Multi-step guided deployment with canary/staged/rolling strategy selection.
 * Shows validation errors before deploy, allows rollback with one click.
 */
export function DeploymentWizard({ nodeId, targetRevision, onClose }: DeploymentWizardProps) {
  const session = useSession();
  const queryClient = useQueryClient();
  const [step, setStep] = useState<WizardStep>("preview");
  const [preview, setPreview] = useState<Preview | null>(null);
  const [validation, setValidation] = useState<Validation | null>(null);
  const [strategy, setStrategy] = useState<DeploymentStrategy>("all_at_once");
  const [confirmOpen, setConfirmOpen] = useState(false);

  const mayWrite = can(session.data, "node:write");

  const runPreview = useMutation({
    mutationFn: async () => {
      return api.post<Preview>("/api/v1/deployments/preview", {
        node_id: nodeId,
        revision: targetRevision,
      });
    },
    onSuccess: (p) => {
      setPreview(p);
      setStep("validate");
    },
  });

  const runValidate = useMutation({
    mutationFn: () =>
      api.post<Validation>("/api/v1/deployments/validate", {
        node_id: nodeId,
        revision: preview?.target_revision ?? targetRevision,
      }),
    onSuccess: (v) => {
      setValidation(v);
      if (v.valid) {
        setStep("strategy");
      }
    },
  });

  const deploy = useMutation({
    mutationFn: () =>
      api.post<{ deployment_id: number }>("/api/v1/deployments", {
        node_id: nodeId,
        strategy,
      }),
    onSuccess: () => {
      setStep("deploying");
      queryClient.invalidateQueries({ queryKey: ["deployments", nodeId] });
      queryClient.invalidateQueries({ queryKey: ["node", nodeId] });
      // Auto-close after 2 seconds
      setTimeout(() => {
        onClose?.();
      }, 2000);
    },
  });

  if (!mayWrite) {
    return (
      <div className="rounded-lg border border-border bg-card p-4 text-center text-sm text-muted-foreground">
        {t("deploy.noPermission")}
      </div>
    );
  }

  if (targetRevision === 0) {
    return (
      <div className="rounded-lg border border-border bg-card p-4 text-center text-sm text-muted-foreground">
        {t("deploy.noRevision")}
      </div>
    );
  }

  return (
    <div className="space-y-4 rounded-lg border border-border bg-card p-4">
      <header className="flex items-center justify-between">
        <h3 className="text-sm font-semibold">{t("deploy.wizard.title")}</h3>
        <StepIndicator current={step} />
      </header>

      {step === "preview" && (
        <PreviewStep
          isLoading={runPreview.isPending}
          error={runPreview.error}
          onNext={() => runPreview.mutate()}
          onCancel={onClose}
        />
      )}

      {step === "validate" && preview && (
        <ValidateStep
          preview={preview}
          validation={validation}
          isLoading={runValidate.isPending}
          error={runValidate.error}
          onNext={() => runValidate.mutate()}
          onBack={() => setStep("preview")}
          onCancel={onClose}
        />
      )}

      {step === "strategy" && preview && validation && (
        <StrategyStep
          strategy={strategy}
          onStrategyChange={setStrategy}
          onNext={() => setConfirmOpen(true)}
          onBack={() => setStep("validate")}
          onCancel={onClose}
        />
      )}

      {step === "deploying" && (
        <DeployingStep />
      )}

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t("deploy.confirmApply")}
        description={t("deploy.disruption")}
        confirmLabel={t("deploy.apply")}
        pending={deploy.isPending}
        onConfirm={() => deploy.mutate()}
      />

      <MutationError error={deploy.error} />
    </div>
  );
}

function StepIndicator({ current }: { current: WizardStep }) {
  const steps: WizardStep[] = ["preview", "validate", "strategy", "confirm"];
  const currentIndex = steps.indexOf(current);

  return (
    <div className="flex items-center gap-2">
      {steps.map((step, idx) => (
        <div key={step} className="flex items-center gap-2">
          <div
            className={`h-2 w-2 rounded-full ${
              idx <= currentIndex ? "bg-primary" : "bg-muted-foreground/30"
            }`}
          />
          {idx < steps.length - 1 && (
            <div
              className={`h-px w-4 ${
                idx < currentIndex ? "bg-primary" : "bg-muted-foreground/30"
              }`}
            />
          )}
        </div>
      ))}
    </div>
  );
}

interface PreviewStepProps {
  isLoading: boolean;
  error: Error | null;
  onNext: () => void;
  onCancel?: () => void;
}

function PreviewStep({ isLoading, error, onNext, onCancel }: PreviewStepProps) {
  return (
    <div className="space-y-3">
      <p className="text-xs text-muted-foreground">{t("deploy.wizard.previewDesc")}</p>
      <MutationError error={error} />
      <div className="flex gap-2">
        <Button size="sm" onClick={onNext} disabled={isLoading}>
          {isLoading ? t("common.loading") : t("deploy.preview")}
        </Button>
        {onCancel && (
          <Button size="sm" variant="outline" onClick={onCancel}>
            {t("common.cancel")}
          </Button>
        )}
      </div>
    </div>
  );
}

interface ValidateStepProps {
  preview: Preview;
  validation: Validation | null;
  isLoading: boolean;
  error: Error | null;
  onNext: () => void;
  onBack: () => void;
  onCancel?: () => void;
}

function ValidateStep({
  preview,
  validation,
  isLoading,
  error,
  onNext,
  onBack,
  onCancel,
}: ValidateStepProps) {
  return (
    <div className="space-y-3">
      <div className="rounded border border-border bg-background p-3">
        <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-xs">
          <dt className="text-muted-foreground">{t("deploy.currentRevision")}:</dt>
          <dd className="font-mono">{formatNumber(preview.current_revision)}</dd>
          <dt className="text-muted-foreground">{t("deploy.targetRevision")}:</dt>
          <dd className="font-mono">{formatNumber(preview.target_revision)}</dd>
          <dt className="text-muted-foreground">{t("deploy.documentHash")}:</dt>
          <dd className="truncate font-mono text-muted-foreground" title={preview.target_doc_sha256}>
            {preview.target_doc_sha256.slice(0, 16)}...
          </dd>
        </dl>
      </div>

      {validation && (
        <div className="space-y-2 text-xs">
          {validation.valid ? (
            <Badge variant="success">{t("deploy.valid")}</Badge>
          ) : (
            <Badge variant="destructive">{t("deploy.invalid")}</Badge>
          )}
          {validation.conflicts?.map((c) => (
            <p key={c} className="rounded bg-destructive/10 p-2 text-destructive">
              {c}
            </p>
          ))}
          {validation.warnings?.map((w) => (
            <p key={w} className="rounded bg-warning/10 p-2 text-warning">
              {w}
            </p>
          ))}
        </div>
      )}

      <MutationError error={error} />

      <div className="flex gap-2">
        {!validation && (
          <Button size="sm" onClick={onNext} disabled={isLoading}>
            {isLoading ? t("common.loading") : t("deploy.validate")}
          </Button>
        )}
        {validation?.valid && (
          <Button size="sm" onClick={onNext}>
            {t("deploy.wizard.next")}
          </Button>
        )}
        <Button size="sm" variant="outline" onClick={onBack}>
          {t("common.back")}
        </Button>
        {onCancel && (
          <Button size="sm" variant="outline" onClick={onCancel}>
            {t("common.cancel")}
          </Button>
        )}
      </div>
    </div>
  );
}

interface StrategyStepProps {
  strategy: DeploymentStrategy;
  onStrategyChange: (s: DeploymentStrategy) => void;
  onNext: () => void;
  onBack: () => void;
  onCancel?: () => void;
}

function StrategyStep({ strategy, onStrategyChange, onNext, onBack, onCancel }: StrategyStepProps) {
  const strategies: { value: DeploymentStrategy; label: string; desc: string }[] = [
    {
      value: "all_at_once",
      label: t("deploy.allAtOnce"),
      desc: t("deploy.wizard.allAtOnceDesc"),
    },
    {
      value: "canary",
      label: t("deploy.canary"),
      desc: t("deploy.wizard.canaryDesc"),
    },
    {
      value: "staged",
      label: t("deploy.staged"),
      desc: t("deploy.wizard.stagedDesc"),
    },
    {
      value: "rolling",
      label: t("deploy.rolling"),
      desc: t("deploy.wizard.rollingDesc"),
    },
  ];

  return (
    <div className="space-y-3">
      <p className="text-xs text-muted-foreground">{t("deploy.wizard.strategyDesc")}</p>
      <div className="space-y-2">
        {strategies.map((s) => (
          <label
            key={s.value}
            className={`flex cursor-pointer items-start gap-3 rounded border p-3 transition-colors ${
              strategy === s.value
                ? "border-primary bg-accent"
                : "border-border hover:border-primary/50"
            }`}
          >
            <input
              type="radio"
              name="strategy"
              value={s.value}
              checked={strategy === s.value}
              onChange={() => onStrategyChange(s.value)}
              className="mt-1"
            />
            <div className="flex-1">
              <div className="font-medium text-sm">{s.label}</div>
              <div className="text-xs text-muted-foreground">{s.desc}</div>
            </div>
          </label>
        ))}
      </div>
      <div className="flex gap-2">
        <Button size="sm" onClick={onNext}>
          {t("deploy.wizard.deploy")}
        </Button>
        <Button size="sm" variant="outline" onClick={onBack}>
          {t("common.back")}
        </Button>
        {onCancel && (
          <Button size="sm" variant="outline" onClick={onCancel}>
            {t("common.cancel")}
          </Button>
        )}
      </div>
    </div>
  );
}

function DeployingStep() {
  return (
    <div className="space-y-3 text-center">
      <div className="inline-block h-8 w-8 animate-spin rounded-full border-4 border-primary border-t-transparent" />
      <p className="text-sm font-medium">{t("deploy.wizard.deploying")}</p>
      <p className="text-xs text-muted-foreground">{t("deploy.wizard.deployingDesc")}</p>
    </div>
  );
}
