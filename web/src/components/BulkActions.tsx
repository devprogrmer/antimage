import { useState } from "react";

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
    <div className="sticky top-0 z-10 bg-zinc-900 border-b border-zinc-800 p-3 flex items-center gap-3">
      <span className="text-sm text-zinc-400">
        {selectedIds.length} selected
      </span>

      <div className="relative">
        <button
          type="button"
          onClick={() => setShowMenu(!showMenu)}
          disabled={loading}
          className="px-3 py-1.5 text-xs bg-blue-600 hover:bg-blue-700 disabled:bg-zinc-700 disabled:cursor-not-allowed rounded"
        >
          Bulk Actions
        </button>

        {showMenu && (
          <div className="absolute top-full left-0 mt-1 bg-zinc-800 border border-zinc-700 rounded shadow-lg min-w-48 z-20">
            <button
              type="button"
              onClick={() => {
                setShowMenu(false);
                setShowConfirm("enable");
              }}
              className="w-full text-left px-4 py-2 text-xs hover:bg-zinc-700"
            >
              Enable
            </button>
            <button
              type="button"
              onClick={() => {
                setShowMenu(false);
                setShowConfirm("disable");
              }}
              className="w-full text-left px-4 py-2 text-xs hover:bg-zinc-700"
            >
              Disable
            </button>
            <button
              type="button"
              onClick={() => {
                setShowMenu(false);
                setShowExtendDialog(true);
              }}
              className="w-full text-left px-4 py-2 text-xs hover:bg-zinc-700"
            >
              Extend Expiry
            </button>
            <button
              type="button"
              onClick={() => {
                setShowMenu(false);
                setShowConfirm("reset-traffic");
              }}
              className="w-full text-left px-4 py-2 text-xs hover:bg-zinc-700"
            >
              Reset Traffic
            </button>
            <button
              type="button"
              onClick={() => {
                setShowMenu(false);
                setShowQuotaDialog(true);
              }}
              className="w-full text-left px-4 py-2 text-xs hover:bg-zinc-700"
            >
              Set Quota
            </button>
            <button
              type="button"
              onClick={() => {
                setShowMenu(false);
                setShowConfirm("delete");
              }}
              className="w-full text-left px-4 py-2 text-xs text-red-400 hover:bg-zinc-700"
            >
              Delete
            </button>
          </div>
        )}
      </div>

      <button
        type="button"
        onClick={onClearSelection}
        className="ml-auto text-xs text-zinc-400 hover:text-zinc-100"
      >
        Clear Selection
      </button>

      {loading && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <div className="bg-zinc-800 p-6 rounded">
            <div className="animate-spin w-8 h-8 border-4 border-blue-500 border-t-transparent rounded-full mx-auto"></div>
            <p className="mt-4 text-sm text-zinc-400">Processing...</p>
          </div>
        </div>
      )}

      {result && (
        <div className={`fixed top-4 right-4 p-4 rounded shadow-lg z-50 ${result.success ? "bg-green-900" : "bg-red-900"}`}>
          <p className="text-sm">
            {result.success ? `${result.action} completed` : `${result.action} failed: ${result.error}`}
          </p>
        </div>
      )}

      {showConfirm && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <div className="bg-zinc-800 p-6 rounded max-w-md">
            <h3 className="text-lg font-semibold mb-4">
              Confirm {showConfirm}
            </h3>
            <p className="text-sm text-zinc-400 mb-6">
              This will {showConfirm} {selectedIds.length} subjects. Continue?
            </p>
            <div className="flex gap-3 justify-end">
              <button
                type="button"
                onClick={() => setShowConfirm(null)}
                className="px-4 py-2 text-xs bg-zinc-700 hover:bg-zinc-600 rounded"
              >
                Cancel
              </button>
              <button
                type="button"
                onClick={() => {
                  setShowConfirm(null);
                  executeAction(showConfirm, { subject_ids: selectedIds });
                }}
                className="px-4 py-2 text-xs bg-red-600 hover:bg-red-700 rounded"
              >
                Confirm
              </button>
            </div>
          </div>
        </div>
      )}

      {showExtendDialog && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <div className="bg-zinc-800 p-6 rounded max-w-md">
            <h3 className="text-lg font-semibold mb-4">Extend Expiry</h3>
            <p className="text-sm text-zinc-400 mb-4">
              Extend expiry for {selectedIds.length} subjects
            </p>
            <div className="mb-6">
              <label className="block text-xs text-zinc-400 mb-2">Days to extend</label>
              <input
                type="number"
                value={extendDays}
                onChange={(e) => setExtendDays(parseInt(e.target.value) || 0)}
                min="1"
                max="3650"
                className="w-full px-3 py-2 bg-zinc-900 border border-zinc-700 rounded text-sm"
              />
            </div>
            <div className="flex gap-3 justify-end">
              <button
                type="button"
                onClick={() => setShowExtendDialog(false)}
                className="px-4 py-2 text-xs bg-zinc-700 hover:bg-zinc-600 rounded"
              >
                Cancel
              </button>
              <button
                type="button"
                onClick={() => {
                  setShowExtendDialog(false);
                  executeAction("extend", { subject_ids: selectedIds, days: extendDays });
                }}
                className="px-4 py-2 text-xs bg-blue-600 hover:bg-blue-700 rounded"
              >
                Extend
              </button>
            </div>
          </div>
        </div>
      )}

      {showQuotaDialog && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <div className="bg-zinc-800 p-6 rounded max-w-md">
            <h3 className="text-lg font-semibold mb-4">Set Quota</h3>
            <p className="text-sm text-zinc-400 mb-4">
              Set quota for {selectedIds.length} subjects
            </p>
            <div className="mb-6">
              <label className="block text-xs text-zinc-400 mb-2">Quota (GB, 0 = unlimited)</label>
              <input
                type="number"
                value={quotaGB}
                onChange={(e) => setQuotaGB(parseInt(e.target.value) || 0)}
                min="0"
                max="10000"
                className="w-full px-3 py-2 bg-zinc-900 border border-zinc-700 rounded text-sm"
              />
            </div>
            <div className="flex gap-3 justify-end">
              <button
                type="button"
                onClick={() => setShowQuotaDialog(false)}
                className="px-4 py-2 text-xs bg-zinc-700 hover:bg-zinc-600 rounded"
              >
                Cancel
              </button>
              <button
                type="button"
                onClick={() => {
                  setShowQuotaDialog(false);
                  const bytes = quotaGB * 1024 * 1024 * 1024;
                  executeAction("set-quota", { subject_ids: selectedIds, quota_bytes: bytes });
                }}
                className="px-4 py-2 text-xs bg-blue-600 hover:bg-blue-700 rounded"
              >
                Set Quota
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
