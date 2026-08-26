import { useMutation, useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { can, useSession } from "../lib/session";
import { MutationError } from "../routes/Resellers";
import { formatTimestamp, t } from "../i18n";
import { Button } from "./ui/button";
import { Badge } from "./ui/badge";

interface SSHConfig {
  host: string;
  port: number;
  username: string;
  use_key: boolean;
  key_path?: string;
}

interface BootstrapStep {
  name: string;
  status: "pending" | "running" | "completed" | "failed";
  output?: string;
  error?: string;
}

interface BootstrapJob {
  id: string;
  node_id: number;
  status: string;
  steps: BootstrapStep[];
  started_at: number;
  completed_at: number | null;
}

interface SSHBootstrapWizardProps {
  nodeId: number;
  onComplete?: () => void;
}

/**
 * SSH bootstrap wizard: guided node onboarding.
 * 
 * Step-by-step SSH-based node enrollment with real-time progress.
 * Installs agent, configures systemd, and enrolls with the panel.
 */
export function SSHBootstrapWizard({ nodeId, onComplete }: SSHBootstrapWizardProps) {
  const session = useSession();
  const [step, setStep] = useState<"config" | "confirm" | "running" | "complete">("config");
  const [config, setConfig] = useState<SSHConfig>({
    host: "",
    port: 22,
    username: "root",
    use_key: true,
    key_path: "~/.ssh/id_rsa",
  });
  const [password, setPassword] = useState("");
  const [jobId, setJobId] = useState<string | null>(null);

  const mayWrite = can(session.data, "node:write");

  const job = useQuery({
    queryKey: ["bootstrap-job", jobId],
    queryFn: () => api.get<BootstrapJob>(`/api/v1/nodes/${nodeId}/bootstrap-ssh/status/${jobId}`),
    enabled: jobId !== null && step === "running",
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      return status === "running" || status === "pending" ? 1000 : false;
    },
  });

  const startBootstrap = useMutation({
    mutationFn: () =>
      api.post<{ job_id: string }>(`/api/v1/nodes/${nodeId}/bootstrap-ssh`, {
        ...config,
        password: config.use_key ? undefined : password,
      }),
    onSuccess: (data) => {
      setJobId(data.job_id);
      setStep("running");
    },
  });

  if (!mayWrite) {
    return (
      <div className="rounded-lg border border-border bg-card p-4 text-center text-sm text-muted-foreground">
        {t("deploy.noPermission")}
      </div>
    );
  }

  if (step === "complete" || job.data?.status === "completed") {
    return (
      <div className="space-y-4 rounded-lg border border-success bg-success/10 p-4 text-center">
        <div className="mx-auto h-12 w-12 rounded-full bg-success/20 flex items-center justify-center">
          <span className="text-2xl text-success">✓</span>
        </div>
        <div>
          <h3 className="text-sm font-semibold">{t("bootstrap.complete")}</h3>
          <p className="mt-1 text-xs text-muted-foreground">{t("bootstrap.completeDesc")}</p>
        </div>
        <Button size="sm" onClick={onComplete}>
          {t("common.close")}
        </Button>
      </div>
    );
  }

  return (
    <div className="space-y-4 rounded-lg border border-border bg-card p-4">
      <header>
        <h3 className="text-sm font-semibold">{t("bootstrap.title")}</h3>
        <p className="mt-1 text-xs text-muted-foreground">{t("bootstrap.description")}</p>
      </header>

      {step === "config" && (
        <ConfigStep
          config={config}
          password={password}
          onConfigChange={setConfig}
          onPasswordChange={setPassword}
          onNext={() => setStep("confirm")}
        />
      )}

      {step === "confirm" && (
        <ConfirmStep
          config={config}
          onBack={() => setStep("config")}
          onConfirm={() => startBootstrap.mutate()}
          isLoading={startBootstrap.isPending}
          error={startBootstrap.error}
        />
      )}

      {step === "running" && job.data && (
        <RunningStep job={job.data} />
      )}
    </div>
  );
}

interface ConfigStepProps {
  config: SSHConfig;
  password: string;
  onConfigChange: (config: SSHConfig) => void;
  onPasswordChange: (password: string) => void;
  onNext: () => void;
}

function ConfigStep({ config, password, onConfigChange, onPasswordChange, onNext }: ConfigStepProps) {
  const update = (field: keyof SSHConfig, value: any) => {
    onConfigChange({ ...config, [field]: value });
  };

  return (
    <div className="space-y-3">
      <div>
        <label className="block text-xs font-medium text-muted-foreground mb-1">
          {t("bootstrap.host")}
        </label>
        <input
          type="text"
          value={config.host}
          onChange={(e) => update("host", e.target.value)}
          placeholder="192.168.1.100"
          className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
        />
      </div>

      <div className="grid grid-cols-2 gap-3">
        <div>
          <label className="block text-xs font-medium text-muted-foreground mb-1">
            {t("bootstrap.port")}
          </label>
          <input
            type="number"
            value={config.port}
            onChange={(e) => update("port", parseInt(e.target.value, 10))}
            className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
          />
        </div>
        <div>
          <label className="block text-xs font-medium text-muted-foreground mb-1">
            {t("bootstrap.username")}
          </label>
          <input
            type="text"
            value={config.username}
            onChange={(e) => update("username", e.target.value)}
            className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
          />
        </div>
      </div>

      <div className="space-y-2">
        <label className="flex items-center gap-2 text-sm">
          <input
            type="radio"
            checked={config.use_key}
            onChange={() => update("use_key", true)}
          />
          {t("bootstrap.useSSHKey")}
        </label>
        {config.use_key && (
          <input
            type="text"
            value={config.key_path || ""}
            onChange={(e) => update("key_path", e.target.value)}
            placeholder="~/.ssh/id_rsa"
            className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
          />
        )}

        <label className="flex items-center gap-2 text-sm">
          <input
            type="radio"
            checked={!config.use_key}
            onChange={() => update("use_key", false)}
          />
          {t("bootstrap.usePassword")}
        </label>
        {!config.use_key && (
          <input
            type="password"
            value={password}
            onChange={(e) => onPasswordChange(e.target.value)}
            placeholder={t("bootstrap.password")}
            className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
          />
        )}
      </div>

      <Button
        size="sm"
        onClick={onNext}
        disabled={!config.host || (!config.use_key && !password)}
      >
        {t("common.next")}
      </Button>
    </div>
  );
}

interface ConfirmStepProps {
  config: SSHConfig;
  onBack: () => void;
  onConfirm: () => void;
  isLoading: boolean;
  error: Error | null;
}

function ConfirmStep({ config, onBack, onConfirm, isLoading, error }: ConfirmStepProps) {
  return (
    <div className="space-y-3">
      <div className="rounded-lg border border-border bg-background p-3">
        <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-xs">
          <dt className="text-muted-foreground">{t("bootstrap.host")}:</dt>
          <dd className="font-mono">{config.host}</dd>
          <dt className="text-muted-foreground">{t("bootstrap.port")}:</dt>
          <dd className="font-mono">{config.port}</dd>
          <dt className="text-muted-foreground">{t("bootstrap.username")}:</dt>
          <dd className="font-mono">{config.username}</dd>
          <dt className="text-muted-foreground">{t("bootstrap.auth")}:</dt>
          <dd className="font-mono">
            {config.use_key ? t("bootstrap.sshKey") : t("bootstrap.password")}
          </dd>
        </dl>
      </div>

      <p className="text-xs text-muted-foreground">{t("bootstrap.confirmDesc")}</p>

      <MutationError error={error} />

      <div className="flex gap-2">
        <Button size="sm" onClick={onConfirm} disabled={isLoading}>
          {isLoading ? t("common.loading") : t("bootstrap.start")}
        </Button>
        <Button size="sm" variant="outline" onClick={onBack}>
          {t("common.back")}
        </Button>
      </div>
    </div>
  );
}

function RunningStep({ job }: { job: BootstrapJob }) {
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <span className="text-sm font-medium">{t("bootstrap.progress")}</span>
        <Badge variant={job.status === "running" ? "warning" : "outline"}>
          {job.status}
        </Badge>
      </div>

      <div className="space-y-2">
        {job.steps.map((step, idx) => (
          <StepItem key={idx} step={step} />
        ))}
      </div>

      {job.completed_at && (
        <p className="text-xs text-muted-foreground">
          {t("bootstrap.completedAt")}: {formatTimestamp(job.completed_at)}
        </p>
      )}
    </div>
  );
}

function StepItem({ step }: { step: BootstrapStep }) {
  const icon =
    step.status === "completed"
      ? "✓"
      : step.status === "failed"
      ? "✗"
      : step.status === "running"
      ? "⟳"
      : "○";

  const color =
    step.status === "completed"
      ? "text-success"
      : step.status === "failed"
      ? "text-destructive"
      : step.status === "running"
      ? "text-warning"
      : "text-muted-foreground";

  return (
    <div className="rounded border border-border bg-background p-2">
      <div className="flex items-center gap-2">
        <span className={`text-lg ${color} ${step.status === "running" ? "animate-spin" : ""}`}>
          {icon}
        </span>
        <span className="text-sm">{step.name}</span>
      </div>
      {step.output && (
        <pre className="mt-2 overflow-x-auto rounded bg-muted p-2 font-mono text-[10px] text-muted-foreground">
          {step.output}
        </pre>
      )}
      {step.error && (
        <p className="mt-2 text-xs text-destructive">{step.error}</p>
      )}
    </div>
  );
}
