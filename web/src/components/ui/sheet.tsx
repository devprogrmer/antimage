import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";
import { t } from "@/i18n";

/**
 * Sheet: a panel that slides in from the edge, for detail and for forms that
 * would otherwise push a list around.
 *
 * A Dialog underneath, so it inherits the behaviour that makes a panel safe --
 * focus trapped and returned, Escape closes, the page behind inert to a screen
 * reader. It is not a <dialog> element and not a bare div: the first has
 * patchy behaviour across browsers, and the second has none of it.
 *
 * It enters from the INLINE END edge, which differs by direction: the trailing
 * side under English, the leading one under Persian and Arabic. shadcn upstream
 * pins it with physical offsets and a physical slide animation; both are
 * refused by scripts/check-rtl.sh, and both are wrong half the time.
 */
export const Sheet = DialogPrimitive.Root;
export const SheetTrigger = DialogPrimitive.Trigger;
export const SheetClose = DialogPrimitive.Close;

export function SheetContent({
  className,
  children,
  ...props
}: ComponentProps<typeof DialogPrimitive.Content>) {
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay
        className={cn(
          "fixed inset-0 z-50 bg-black/60",
          "data-[state=open]:animate-in data-[state=closed]:animate-out",
          "data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0",
        )}
      />
      <DialogPrimitive.Content
        className={cn(
          // inset-y + end-0 pins it to the trailing edge in either direction.
          "fixed inset-y-0 end-0 z-50 flex h-full w-full flex-col gap-4",
          "border-s border-border bg-card p-6 shadow-lg sm:max-w-md",
          "data-[state=open]:animate-in data-[state=closed]:animate-out",
          "data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0",
          "overflow-y-auto",
          className,
        )}
        {...props}
      >
        {children}
        <DialogPrimitive.Close
          className="absolute end-4 top-4 rounded-sm text-muted-foreground hover:text-foreground"
        >
          <X className="size-4" />
          <span className="sr-only">{t("close")}</span>
        </DialogPrimitive.Close>
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  );
}

export function SheetHeader({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("flex flex-col gap-1 text-start", className)} {...props} />;
}

export function SheetTitle({
  className,
  ...props
}: ComponentProps<typeof DialogPrimitive.Title>) {
  return (
    <DialogPrimitive.Title
      className={cn("text-base font-semibold", className)}
      {...props}
    />
  );
}

export function SheetDescription({
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
