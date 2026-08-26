import { useState } from "react";

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
          <section>
            <h3 className="mb-2 text-sm font-semibold">{t("bootstrap.title")}</h3>
            <p className="mb-4 text-xs text-muted-foreground">
              {t("bootstrap.selectNode")}
            </p>
            {bootstrapNodeId === null ? (
              <p className="py-8 text-center text-sm text-muted-foreground">
                {t("bootstrap.noNodeSelected")}
              </p>
            ) : (
              <SSHBootstrapWizard
                nodeId={bootstrapNodeId}
                onComplete={() => setBootstrapNodeId(null)}
              />
            )}
          </section>
        </TabsContent>
      </Tabs>
    </div>
  );
}
