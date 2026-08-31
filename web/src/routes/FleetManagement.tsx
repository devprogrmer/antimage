import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { api } from "../lib/api";
import { NodeTopology } from "../components/NodeTopology";
import { DriftDetectionDashboard } from "../components/DriftDetectionDashboard";
import { CertificateManagement } from "../components/CertificateManagement";
import { SSHBootstrapWizard } from "../components/SSHBootstrapWizard";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../components/ui/tabs";
import { t } from "../i18n";

/**
 * Fleet Management: Comprehensive node operations dashboard.
 * 
 * Combines topology view, drift detection, certificate management,
 * and SSH bootstrap into a unified fleet operations interface.
 */
export function FleetManagement() {
  const [bootstrapNodeId, setBootstrapNodeId] = useState<number | null>(null);

  return (
    <div className="space-y-6 p-4">
      <header>
        <h1 className="text-xl font-bold">{t("fleet.title")}</h1>
        <p className="mt-1 text-sm text-muted-foreground">{t("fleet.description")}</p>
      </header>

      <Tabs defaultValue="topology">
        <TabsList>
          <TabsTrigger value="topology">{t("fleet.topology")}</TabsTrigger>
          <TabsTrigger value="drift">{t("fleet.drift")}</TabsTrigger>
          <TabsTrigger value="certificates">{t("fleet.certificates")}</TabsTrigger>
          <TabsTrigger value="bootstrap">{t("fleet.bootstrap")}</TabsTrigger>
        </TabsList>

        <TabsContent value="topology" className="space-y-4">
          <NodeTopology />
        </TabsContent>

        <TabsContent value="drift" className="space-y-4">
          <DriftDetectionDashboard />
        </TabsContent>

        <TabsContent value="certificates" className="space-y-4">
          <CertificateManagement />
        </TabsContent>

        <TabsContent value="bootstrap" className="space-y-4">
          <BootstrapTab
            nodeId={bootstrapNodeId}
            onNodeChange={setBootstrapNodeId}
          />
        </TabsContent>
      </Tabs>
    </div>
  );
}

interface BootstrapTabProps {
  nodeId: number | null;
  onNodeChange: (id: number | null) => void;
}

// The bootstrap wizard needs to know WHICH node to enrol -- there is no
// "fleet bootstrap", every run targets exactly one row in the nodes table.
// The old tab said "select a node" and offered nothing to select from, so a
// working wizard was gated behind a control that had never been built.
function BootstrapTab({ nodeId, onNodeChange }: BootstrapTabProps) {
  // Only pending nodes are legitimate targets. Enrolling an already-enrolled
  // node re-issues its identity and drops it off the control plane in the
  // meantime -- the recovery path for that is ReissueEnrolment on NodeDetail,
  // not this wizard.
  const candidates = useQuery({
    queryKey: ["nodes"],
    queryFn: () => api.get<{ nodes: Array<{ id: number; name: string; status: string }> }>("/api/v1/nodes"),
    select: (d) => d.nodes.filter((n) => n.status === "pending"),
  });

  return (
    <section className="space-y-4">
      <div>
        <h3 className="mb-2 text-sm font-semibold">{t("bootstrap.title")}</h3>
        <p className="mb-3 text-xs text-muted-foreground">{t("bootstrap.description")}</p>
        <label className="block text-xs text-muted-foreground" htmlFor="bootstrap-node">
          {t("bootstrap.selectNode")}
        </label>
        <select
          id="bootstrap-node"
          value={nodeId ?? ""}
          onChange={(e) => onNodeChange(e.target.value ? Number(e.target.value) : null)}
          className="mt-1 w-full max-w-md rounded-md border border-input bg-background px-3 py-2 text-sm"
        >
          <option value="">{t("bootstrap.noNodeSelected")}</option>
          {candidates.data?.map((n) => (
            <option key={n.id} value={n.id}>
              {n.name}
            </option>
          ))}
        </select>
        {candidates.data?.length === 0 && (
          <p className="mt-2 text-xs text-muted-foreground">
            {t("bootstrap.noPendingNodes")}
          </p>
        )}
      </div>

      {nodeId !== null && (
        <SSHBootstrapWizard nodeId={nodeId} onComplete={() => onNodeChange(null)} />
      )}
    </section>
  );
}
