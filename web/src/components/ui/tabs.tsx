import * as TabsPrimitive from "@radix-ui/react-tabs";
import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

/**
 * Tabs, on Radix.
 *
 * Radix implements the WAI-ARIA tab pattern, which is more than styling a row
 * of buttons: arrow keys move between tabs, Home and End jump to the ends, only
 * the active tab is in the tab order, and the panel is associated with its tab
 * so a screen reader announces what it is reading. A div with onClick has none
 * of that, and §65 asks for keyboard navigation end to end.
 *
 * Arrow keys follow the document direction automatically, so Left moves to the
 * NEXT tab under Persian and Arabic rather than the previous one.
 */
export const Tabs = TabsPrimitive.Root;

export function TabsList({
  className,
  ...props
}: ComponentProps<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      className={cn(
        // Scrolls rather than wraps: a node with six tabs on a narrow screen
        // should not reflow the panel below it as the operator resizes.
        "flex items-center gap-1 overflow-x-auto border-b border-border",
        className,
      )}
      {...props}
    />
  );
}

export function TabsTrigger({
  className,
  ...props
}: ComponentProps<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      className={cn(
        "whitespace-nowrap border-b-2 border-transparent px-3 py-2 text-sm text-muted-foreground",
        "transition-colors hover:text-foreground",
        "data-[state=active]:border-primary data-[state=active]:text-foreground",
        className,
      )}
      {...props}
    />
  );
}

export function TabsContent({
  className,
  ...props
}: ComponentProps<typeof TabsPrimitive.Content>) {
  return <TabsPrimitive.Content className={cn("pt-4", className)} {...props} />;
}
