import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { t } from "@/i18n";

/**
 * ConfirmDialog replaces window.confirm for destructive actions.
 *
 * window.confirm has four problems that matter here. Its buttons are the
 * browser's, so they are in the browser's language rather than the operator's
 * -- an Arabic panel with an English "OK" is the clearest possible signal that
 * the localisation is skin deep. It blocks the main thread, so nothing renders
 * behind it. It cannot say what is about to happen beyond one line of text. And
 * §77 of the spec asks that disruption be stated before the click, which a
 * yes/no box has no room for.
 *
 * Deliberately NOT promise-based. A `const ok = await confirm()` helper reads
 * better at the call site and hides that the dialog is a component with a
 * lifetime: it leaks when the caller unmounts mid-await, and it cannot be
 * driven by a test without faking a microtask. State in, callback out.
 */
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  onConfirm,
  destructive = true,
  pending = false,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  /** What the action will actually do, including anything irreversible. */
  description?: string;
  confirmLabel?: string;
  onConfirm: () => void;
  /** Styles the confirm button as destructive. Most callers here are. */
  destructive?: boolean;
  /** Disables the buttons while the mutation is in flight. */
  pending?: boolean;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description && <DialogDescription>{description}</DialogDescription>}
        </DialogHeader>
        <DialogFooter className="mt-4">
          <Button
            variant="outline"
            size="sm"
            onClick={() => onOpenChange(false)}
            disabled={pending}
          >
            {t("cancel")}
          </Button>
          <Button
            variant={destructive ? "destructive" : "default"}
            size="sm"
            onClick={onConfirm}
            disabled={pending}
          >
            {confirmLabel ?? t("confirm")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
