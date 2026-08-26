import { clsx } from "clsx";
import type { ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

/**
 * cn joins class names and resolves Tailwind conflicts.
 *
 * clsx handles the conditional shape (`cn("a", cond && "b")`); tailwind-merge
 * then keeps the LAST of any conflicting pair, so a caller passing
 * `className="p-0"` to a component whose default is `p-4` actually gets p-0.
 * Plain concatenation emits both and leaves the winner to CSS source order,
 * which is a property of the build rather than of the call.
 */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
