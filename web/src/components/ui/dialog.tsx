import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";
import { t } from "@/i18n";

/**
 * Dialog, on Radix.
 *
 * Radix rather than a hand-rolled modal because the parts that are easy to get
 * wrong are the ones nobody notices until an operator hits them: focus is
 * trapped and restored to whatever opened it, Escape closes, the rest of the
 * page is inert to a screen reader, and the scroll position underneath does not
 * jump. Section 65 asks for keyboard navigation end to end, and a modal is
 * where that requirement is usually quietly dropped.
 *
 * Positioned with logical properties throughout. shadcn upstream ships this
 * with `left-[50%]` and `slide-in-from-left`, both of which scripts/check-rtl.sh
 * refuses -- and correctly so, because a dialog that slides in from the wrong
 * edge under Persian is the kind of detail that makes a product feel foreign.
 */
export const Dialog = DialogPrimitive.Root;
export const DialogTrigger = DialogPrimitive.Trigger;
export const DialogClose = DialogPrimitive.Close;

export function DialogOverlay({
  className,
  ...props
}: ComponentProps<typeof DialogPrimitive.Overlay>) {
  return (
    <DialogPrimitive.Overlay
      className={cn(
        "fixed inset-0 z-50 bg-black/60",
        "data-[state=open]:animate-in data-[state=closed]:animate-out",
        "data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0",
        className,
      )}
      {...props}
    />
  );
}

export function DialogContent({
  className,
  children,
  ...props
}: ComponentProps<typeof DialogPrimitive.Content>) {
  return (
    <DialogPrimitive.Portal>
      <DialogOverlay />
      <DialogPrimitive.Content
        className={cn(
          // Centred with a transform rather than inset-inline offsets, so the
          // same rule holds in both directions without a mirrored override.
          "fixed top-1/2 start-1/2 z-50 w-full max-w-lg -translate-y-1/2 rtl:translate-x-1/2 ltr:-translate-x-1/2",
          "border border-border bg-card p-6 shadow-lg sm:rounded-lg",
          "data-[state=open]:animate-in data-[state=closed]:animate-out",
          "data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0",
          className,
        )}
        {...props}
      >
        {children}
        <DialogPrimitive.Close
          className={cn(
            "absolute end-4 top-4 rounded-sm text-muted-foreground",
            "transition-opacity hover:text-foreground disabled:pointer-events-none",
          )}
        >
          <X className="size-4" />
          <span className="sr-only">{t("close")}</span>
        </DialogPrimitive.Close>
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  );
}

export function DialogHeader({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      className={cn("flex flex-col gap-2 text-center sm:text-start", className)}
      {...props}
    />
  );
}

export function DialogFooter({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      className={cn(
        "flex flex-col-reverse gap-2 sm:flex-row sm:justify-end",
        className,
      )}
      {...props}
    />
  );
}

export function DialogTitle({
  className,
  ...props
}: ComponentProps<typeof DialogPrimitive.Title>) {
  return (
    <DialogPrimitive.Title
      className={cn("text-base font-semibold leading-none", className)}
      {...props}
    />
  );
}

export function DialogDescription({
  className,
  ...props
}: ComponentProps<typeof DialogPrimitive.Description>) {
  return (
    <DialogPrimitive.Description
      className={cn("text-sm text-muted-foreground", className)}
      {...props}
    />
  );
}
