import { Command } from "cmdk";
import { useEffect, useState } from "react";
import type { ComponentType } from "react";
import { useNavigate } from "react-router-dom";
import { Monitor, Moon, Sun } from "lucide-react";

import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog";
import { useTheme } from "@/lib/theme";
import type { Theme } from "@/lib/theme";
import { t } from "@/i18n";

export interface CommandTarget {
  to: string;
  label: string;
  icon: ComponentType<{ className?: string }>;
}

/**
 * Command palette, on Ctrl+K / Cmd+K.
 *
 * The destinations are passed in already filtered by permission, by the same
 * NAV list the sidebar renders. That is deliberate: a palette with its own copy
 * of the routes would drift from the navigation, and the first thing to drift
 * would be the permission gate -- offering a tenant a jump to a screen that can
 * only 403 is worse than not offering the palette at all.
 *
 * Filtering and arrow-key movement come from cmdk, which keeps the active
 * option under aria-activedescendant rather than moving real focus, so the
 * search box stays typed-into while the list moves underneath.
 */
export function CommandPalette({ targets }: { targets: CommandTarget[] }) {
  const [open, setOpen] = useState(false);
  const navigate = useNavigate();
  const { setTheme } = useTheme();

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      // metaKey for macOS, ctrlKey elsewhere. Checking both rather than
      // sniffing the platform: a Mac with an external PC keyboard sends ctrl,
      // and an operator should not have to know which one we guessed.
      if (e.key.toLowerCase() === "k" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        setOpen((prev) => !prev);
      }
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);

  function run(action: () => void) {
    // Closed BEFORE the action, so a navigation does not leave the palette
    // sitting over the screen it just opened.
    setOpen(false);
    action();
  }

  const themes: Array<{ value: Theme; label: string; icon: ComponentType<{ className?: string }> }> = [
    { value: "light", label: "theme.light", icon: Sun },
    { value: "dark", label: "theme.dark", icon: Moon },
    { value: "system", label: "theme.system", icon: Monitor },
  ];

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="max-w-lg p-0">
        {/* Radix requires a title for the accessible name; the palette shows
            its search field instead, so the title is for screen readers. */}
        <DialogTitle className="sr-only">{t("command.title")}</DialogTitle>
        <Command
          // Escaping is handled by the Dialog, so cmdk must not also swallow it.
          loop
          className="overflow-hidden rounded-lg"
        >
          <Command.Input
            placeholder={t("command.placeholder")}
            className="w-full border-b border-border bg-transparent px-4 py-3 text-sm outline-none placeholder:text-muted-foreground"
          />
          <Command.List className="max-h-80 overflow-y-auto p-2">
            <Command.Empty className="py-6 text-center text-sm text-muted-foreground">
              {t("command.empty")}
            </Command.Empty>

            <Command.Group
              heading={t("command.navigation")}
              className="text-xs text-muted-foreground [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1.5"
            >
              {targets.map((target) => (
                <Command.Item
                  key={target.to}
                  value={target.label}
                  onSelect={() => run(() => navigate(target.to))}
                  className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-2 text-sm text-foreground data-[selected=true]:bg-accent data-[selected=true]:text-accent-foreground"
                >
                  <target.icon className="size-4 shrink-0" />
                  {target.label}
                </Command.Item>
              ))}
            </Command.Group>

            <Command.Group
              heading={t("theme.label")}
              className="text-xs text-muted-foreground [&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1.5"
            >
              {themes.map((option) => (
                <Command.Item
                  key={option.value}
                  value={t(option.label as Parameters<typeof t>[0])}
                  onSelect={() => run(() => setTheme(option.value))}
                  className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-2 text-sm text-foreground data-[selected=true]:bg-accent data-[selected=true]:text-accent-foreground"
                >
                  <option.icon className="size-4 shrink-0" />
                  {t(option.label as Parameters<typeof t>[0])}
                </Command.Item>
              ))}
            </Command.Group>
          </Command.List>
        </Command>
      </DialogContent>
    </Dialog>
  );
}
