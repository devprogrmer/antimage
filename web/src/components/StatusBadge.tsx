import { t, type TranslationKey } from "../i18n";

export type NodeStatus =
  | "pending"
  | "enrolling"
  | "online"
  | "degraded"
  | "integrity"
  | "offline"
  | "disabled";

// severity is what assistive technology and any future filtering key off, so
// it is an attribute rather than something inferred from the colour classes.
const styles: Record<NodeStatus, { className: string; severity: string }> = {
  pending: { className: "border-border text-muted-foreground", severity: "info" },
  enrolling: { className: "border-primary text-primary", severity: "info" },
  online: { className: "border-success text-success", severity: "ok" },
  degraded: { className: "border-warning text-warning", severity: "warn" },
  integrity: { className: "border-destructive text-destructive", severity: "alert" },
  offline: { className: "border-input text-muted-foreground", severity: "warn" },
  disabled: { className: "border-input text-muted-foreground", severity: "info" },
};

export function StatusBadge({ status }: { status: NodeStatus }) {
  // A status the server invented after this bundle shipped still has to render
  // something an operator can read, so the raw value is the fallback label.
  const style: { className: string; severity: string } | undefined = styles[status];
  // Colour is never the only signal: the label always carries the meaning.
  const label = style ? t(`status.${status}` as TranslationKey) : status;

  return (
    <span
      role="status"
      data-severity={style?.severity ?? "info"}
      className={`inline-block rounded border px-1.5 py-0.5 font-mono text-[11px] uppercase ${
        style?.className ?? "border-input text-muted-foreground"
      }`}
    >
      {label}
    </span>
  );
}
