import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

/** Surface primitives. Tokens only, so both themes come out right. */
export function Card({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      className={cn(
        "rounded-lg border border-border bg-card text-card-foreground shadow-sm",
        className,
      )}
      {...props}
    />
  );
}

export function CardHeader({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("flex flex-col gap-1.5 p-4", className)} {...props} />;
}

export function CardTitle({ className, ...props }: ComponentProps<"h3">) {
  return (
    <h3 className={cn("text-sm font-semibold leading-none", className)} {...props} />
  );
}

export function CardDescription({ className, ...props }: ComponentProps<"p">) {
  return <p className={cn("text-xs text-muted-foreground", className)} {...props} />;
}

export function CardContent({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("p-4 pt-0", className)} {...props} />;
}

export function CardFooter({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("flex items-center p-4 pt-0", className)} {...props} />;
}
