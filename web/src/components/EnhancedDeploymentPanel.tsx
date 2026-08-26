import { DeploymentPanel } from "./DeploymentPanel";
import { DeploymentWizard } from "./DeploymentWizard";
import { ApplyRunsTimeline } from "./ApplyRunsTimeline";
import { Button } from "./ui/button";
import { t } from "../i18n";
import { useState } from "react";

interface EnhancedDeploymentPanelProps {
  nodeId: number;
  targetRevision: number;
}

/**
 * Enhanced DeploymentPanel: combines the original panel with the wizard and timeline.
 * 
 * This is a drop-in replacement for the original DeploymentPanel that adds:
 * - Deployment wizard (guided workflow)
 * - Apply runs timeline (visual history)
 * - Better layout and organization
 * 
 * Toggle between "simple" (original) and "wizard" modes.
 */
export function EnhancedDeploymentPanel({ nodeId, targetRevision }: EnhancedDeploymentPanelProps) {
  const [mode, setMode] = useState<"simple" | "wizard">("simple");

  return (
    <div className="space-y-6">
      {/* Mode Toggle */}
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold">{t("deploy.title")}</h3>
        <div className="flex gap-2">
          <Button
            size="sm"
            variant={mode === "simple" ? "default" : "outline"}
            onClick={() => setMode("simple")}
          >
            {t("deploy.simpleMode")}
          </Button>
          <Button
            size="sm"
            variant={mode === "wizard" ? "default" : "outline"}
            onClick={() => setMode("wizard")}
          >
            {t("deploy.wizardMode")}
          </Button>
        </div>
      </div>

      {/* Deployment Interface */}
      {mode === "simple" ? (
        <DeploymentPanel nodeId={nodeId} targetRevision={targetRevision} />
      ) : (
        <DeploymentWizard
          nodeId={nodeId}
          targetRevision={targetRevision}
          onClose={() => setMode("simple")}
        />
      )}

      {/* Apply Runs History */}
      <div className="border-t border-border pt-6">
        <ApplyRunsTimeline nodeId={nodeId} limit={10} />
      </div>
    </div>
  );
}
