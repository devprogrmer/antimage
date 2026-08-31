import { useState } from "react";
import { t } from "../i18n";

interface BulkActionsProps {
  selectedIds: number[];
  onAction: (action: string, params?: any) => Promise<void>;
  onClearSelection: () => void;
}

export function BulkActions({ selectedIds, onAction, onClearSelection }: BulkActionsProps) {
  const [showMenu, setShowMenu] = useState(false);
  const [showExtendDialog, setShowExtendDialog] = useState(false);
  const [showQuotaDialog, setShowQuotaDialog] = useState(false);
  const [showConfirm, setShowConfirm] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [result, setResult] = useState<any>(null);

  const [extendDays, setExtendDays] = useState(30);
  const [quotaGB, setQuotaGB] = useState(50);

  if (selectedIds.length === 0) {
    return null;
  }

  async function executeAction(action: string, params?: any) {
    setLoading(true);
    setShowMenu(false);
    try {
      await onAction(action, params);
      setResult({ success: true, action });
      setTimeout(() => setResult(null), 3000);
    } catch (error: any) {
      setResult({ success: false, action, error: error.message });
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="sticky top-0 z-10 bg-card border-b border-border p-3 flex items-center gap-3">
      <span className="text-sm text-muted-foreground">
        {selectedIds.length} {t("subject.selected")}
      </span>

      <div className="relative">
        <button
          type="button"
          onClick={() => setShowMenu(!showMenu)}
          disabled={loading}
          className="px-3 py-1.5 text-xs bg-primary hover:bg-primary/90 disabled:bg-secondary disabled:cursor-not-allowed rounded"
        >
          {t("bulk.actions")}
        </button>

        {showMenu && (
          <div className="absolute top-full start-0 mt-1 bg-secondary border border-input rounded shadow-lg min-w-48 z-20">
            <button
              type="button"
              onClick={() => {
                setShowMenu(false);
                setShowConfirm("enable");
              }}
              className="w-full text-start px-4 py-2 text-xs hover:bg-secondary/80"
            >
              {t("subject.enable")}
            </button>
            <button
              type="button"
              onClick={() => {
                setShowMenu(false);
                setShowConfirm("disable");
              }}
              className="w-full text-start px-4 py-2 text-xs hover:bg-secondary/80"
            >
              {t("subject.disable")}
            </button>
            <button
              type="button"
              onClick={() => {
                setShowMenu(false);
                setShowExtendDialog(true);
              }}
              className="w-full text-start px-4 py-2 text-xs hover:bg-secondary/80"
            >
              {t("subject.extendExpiry")}
            </button>
            <button
              type="button"
              onClick={() => {
                setShowMenu(false);
                setShowConfirm("reset-traffic");
              }}
              className="w-full text-start px-4 py-2 text-xs hover:bg-secondary/80"
            >
              {t("subject.resetTraffic")}
            </button>
            <button
              type="button"
              onClick={() => {
                setShowMenu(false);
                setShowQuotaDialog(true);
              }}
              className="w-full text-start px-4 py-2 text-xs hover:bg-secondary/80"
            >
              {t("subject.setQuota")}
            </button>
            <button
              type="button"
              onClick={() => {
                setShowMenu(false);
                setShowConfirm("delete");
              }}
              className="w-full text-start px-4 py-2 text-xs text-destructive hover:bg-secondary/80"
            >
              {t("delete")}
            </button>
          </div>
        )}
      </div>

      <button
        type="button"
        onClick={onClearSelection}
        className="ms-auto text-xs text-muted-foreground hover:text-foreground"
      >
        {t("subject.clearSelection")}
      </button>

      {loading && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <div className="bg-secondary p-6 rounded">
            <div className="animate-spin w-8 h-8 border-4 border-primary border-t-transparent rounded-full mx-auto"></div>
            <p className="mt-4 text-sm text-muted-foreground">{t("loading")}</p>
          </div>
        </div>
      )}

      {result && (
        <div className={`fixed top-4 end-4 p-4 rounded shadow-lg z-50 ${result.success ? "bg-success/20 text-success" : "bg-destructive/20 text-destructive"}`}>
          <p className="text-sm">
            {result.success ? `${result.action} completed` : `${result.action} failed: ${result.error}`}
          </p>
        </div>
      )}

      {showConfirm && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <div className="bg-secondary p-6 rounded max-w-md">
            <h3 className="text-lg font-semibold mb-4">
              {t("confirm")} {showConfirm}
            </h3>
            <p className="text-sm text-muted-foreground mb-6">
              {`${t("bulk.confirmMessage")} ${selectedIds.length}`}
            </p>
            <div className="flex gap-3 justify-end">
              <button
                type="button"
                onClick={() => setShowConfirm(null)}
                className="px-4 py-2 text-xs bg-secondary hover:bg-secondary rounded"
              >
                {t("cancel")}
              </button>
              <button
                type="button"
                onClick={() => {
                  setShowConfirm(null);
                  executeAction(showConfirm, { subject_ids: selectedIds });
                }}
                className="px-4 py-2 text-xs bg-destructive hover:bg-destructive/90 rounded"
              >
                {t("confirm")}
              </button>
            </div>
          </div>
        </div>
      )}

      {showExtendDialog && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <div className="bg-secondary p-6 rounded max-w-md">
            <h3 className="text-lg font-semibold mb-4">{t("subject.extendExpiry")}</h3>
            <p className="text-sm text-muted-foreground mb-4">
              {`${t("bulk.extendMessage")} ${selectedIds.length}`}
            </p>
            <div className="mb-6">
              <label className="block text-xs text-muted-foreground mb-2">{t("bulk.daysToExtend")}</label>
              <input
                type="number"
                value={extendDays}
                onChange={(e) => setExtendDays(parseInt(e.target.value) || 0)}
                min="1"
                max="3650"
                className="w-full px-3 py-2 bg-card border border-input rounded text-sm"
              />
            </div>
            <div className="flex gap-3 justify-end">
              <button
                type="button"
                onClick={() => setShowExtendDialog(false)}
                className="px-4 py-2 text-xs bg-secondary hover:bg-secondary rounded"
              >
                {t("cancel")}
              </button>
              <button
                type="button"
                onClick={() => {
                  setShowExtendDialog(false);
                  executeAction("extend", { subject_ids: selectedIds, days: extendDays });
                }}
                className="px-4 py-2 text-xs bg-primary hover:bg-primary/90 rounded"
              >
                {t("subject.extend")}
              </button>
            </div>
          </div>
        </div>
      )}

      {showQuotaDialog && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <div className="bg-secondary p-6 rounded max-w-md">
            <h3 className="text-lg font-semibold mb-4">{t("subject.setQuota")}</h3>
            <p className="text-sm text-muted-foreground mb-4">
              {`${t("bulk.quotaMessage")} ${selectedIds.length}`}
            </p>
            <div className="mb-6">
              <label className="block text-xs text-muted-foreground mb-2">{t("bulk.quotaGB")}</label>
              <input
                type="number"
                value={quotaGB}
                onChange={(e) => setQuotaGB(parseInt(e.target.value) || 0)}
                min="0"
                max="10000"
                className="w-full px-3 py-2 bg-card border border-input rounded text-sm"
              />
            </div>
            <div className="flex gap-3 justify-end">
              <button
                type="button"
                onClick={() => setShowQuotaDialog(false)}
                className="px-4 py-2 text-xs bg-secondary hover:bg-secondary rounded"
              >
                {t("cancel")}
              </button>
              <button
                type="button"
                onClick={() => {
                  setShowQuotaDialog(false);
                  const bytes = quotaGB * 1024 * 1024 * 1024;
                  executeAction("set-quota", { subject_ids: selectedIds, quota_bytes: bytes });
                }}
                className="px-4 py-2 text-xs bg-primary hover:bg-primary/90 rounded"
              >
                {t("subject.setQuota")}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
