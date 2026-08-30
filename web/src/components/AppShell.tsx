import { NavLink, Outlet } from "react-router-dom";
import {
  Gauge,
  Server,
  Users,
  Building2,
  Activity,
  ScrollText,
  BookMarked,
  ShieldCheck,
  KeyRound,
  UserCircle,
  Monitor,
  Moon,
  Sun,
} from "lucide-react";
import type { ComponentType } from "react";

import { Button } from "@/components/ui/button";
import { CommandPalette } from "@/components/CommandPalette";
import { cn } from "@/lib/utils";
import { useTheme } from "@/lib/theme";
import type { Theme } from "@/lib/theme";
import { can } from "@/lib/session";
import type { Session } from "@/lib/session";
import { getLocale, locales, t } from "@/i18n";
import type { Locale } from "@/i18n";

interface NavItem {
  to: string;
  label: string;
  icon: ComponentType<{ className?: string }>;
  /** Held back unless the actor has this permission. */
  permission?: string;
}

/**
 * The navigation, as data.
 *
 * A list rather than a wall of buttons because every entry needs the same four
 * things -- a path, a label, an icon and a permission gate -- and the previous
 * shell repeated all of that per item, which is how the reseller tab ended up
 * being the only one with a gate.
 */
const NAV: NavItem[] = [
  { to: "/", label: "nav.dashboard", icon: Gauge },
  { to: "/nodes", label: "nav.nodes", icon: Server },
  // Fleet-wide topology, drift, PKI and SSH bootstrap. Everything inside the
  // fleet tab is per-fleet rather than per-node, so it doesn't fit under any
  // single node's detail page. Nav-linked rather than only reachable from a
  // deep tab; the certificate expiry warning is a homepage-level concern.
  { to: "/fleet", label: "fleet.title", icon: ShieldCheck, permission: "node:read" },
  { to: "/subjects", label: "nav.subjects", icon: Users },
  // Hidden without reseller:read, which the tenant role deliberately lacks:
  // every route behind it would 403, and offering a tab that can only fail is
  // worse than not offering it. The gate is a courtesy -- the server re-checks
  // regardless. A tenant reaches their own account through Profile instead.
  { to: "/resellers", label: "nav.resellers", icon: Building2, permission: "reseller:read" },
  { to: "/observability", label: "observability.title", icon: Activity },
  { to: "/audit", label: "nav.audit", icon: ScrollText, permission: "audit:read" },
  { to: "/access", label: "access.title", icon: KeyRound, permission: "admin:manage" },
  { to: "/templates", label: "templates.title", icon: BookMarked },
  { to: "/profile", label: "nav.profile", icon: UserCircle },
];

export function AppShell({
  session,
  onSignOut,
  onLocaleChange,
}: {
  session: Session | undefined;
  onSignOut: () => void;
  onLocaleChange: (next: Locale) => void;
}) {
  const visible = NAV.filter((item) => !item.permission || can(session, item.permission));

  return (
    <div className="flex min-h-screen bg-background text-foreground">
      <aside className="hidden w-56 shrink-0 border-e border-sidebar-border bg-sidebar md:flex md:flex-col">
        <div className="flex h-14 items-center gap-2 border-b border-sidebar-border px-4">
          <span className="font-mono text-sm font-semibold">{t("app.name")}</span>
        </div>
        <nav className="flex flex-1 flex-col gap-0.5 p-2" aria-label={t("nav.primary")}>
          {visible.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              // `end` on the root, or "/" would stay active on every child path
              // and the operator would see two tabs highlighted at once.
              end={item.to === "/"}
              className={({ isActive }) =>
                cn(
                  "flex items-center gap-2 rounded-md px-3 py-2 text-sm transition-colors",
                  isActive
                    ? "bg-accent font-medium text-accent-foreground"
                    : "text-muted-foreground hover:bg-accent/60 hover:text-accent-foreground",
                )
              }
            >
              <item.icon className="size-4 shrink-0" />
              {t(item.label as Parameters<typeof t>[0])}
            </NavLink>
          ))}
        </nav>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 items-center gap-2 border-b border-border px-4">
          {/* The same destinations on small screens, where the sidebar is gone.
              A horizontal scroller rather than a hamburger: six items fit, and
              a menu that hides six things behind a tap is worse than a swipe. */}
          <nav
            className="flex gap-1 overflow-x-auto md:hidden"
            aria-label={t("nav.primary")}
          >
            {visible.map((item) => (
              <NavLink
                key={item.to}
                to={item.to}
                end={item.to === "/"}
                className={({ isActive }) =>
                  cn(
                    "rounded-md px-2 py-1 text-xs whitespace-nowrap",
                    isActive
                      ? "bg-accent text-accent-foreground"
                      : "text-muted-foreground",
                  )
                }
              >
                {t(item.label as Parameters<typeof t>[0])}
              </NavLink>
            ))}
          </nav>

          <div className="ms-auto flex items-center gap-2">
            <ThemeToggle />
            <select
              value={getLocale()}
              onChange={(e) => onLocaleChange(e.target.value as Locale)}
              aria-label={t("nav.language")}
              className="h-8 rounded-md border border-input bg-background px-2 text-xs"
            >
              {locales.map((l) => (
                <option key={l.code} value={l.code}>
                  {l.label}
                </option>
              ))}
            </select>
            <Button variant="ghost" size="sm" onClick={onSignOut}>
              {t("nav.signOut")}
            </Button>
          </div>
        </header>

        <main className="min-w-0 flex-1 p-4">
          <Outlet />
        </main>
      </div>

      {/* One list, two surfaces. The palette takes the ALREADY permission-
          filtered destinations, so it cannot offer a screen the sidebar hides
          -- a palette with its own copy of the routes would drift, and the
          first thing to drift would be the gate. */}
      <CommandPalette
        targets={visible.map((item) => ({
          to: item.to,
          label: t(item.label as Parameters<typeof t>[0]),
          icon: item.icon,
        }))}
      />
    </div>
  );
}

/**
 * Theme toggle across the three real states.
 *
 * Three buttons rather than a two-way switch, because "system" is not a
 * midpoint between light and dark -- it is a different kind of answer, and a
 * toggle that cycles through it leaves an operator unable to tell which one
 * they are currently in.
 */
function ThemeToggle() {
  const { theme, setTheme } = useTheme();
  const options: Array<{ value: Theme; icon: ComponentType<{ className?: string }>; label: string }> = [
    { value: "light", icon: Sun, label: "theme.light" },
    { value: "dark", icon: Moon, label: "theme.dark" },
    { value: "system", icon: Monitor, label: "theme.system" },
  ];

  return (
    <div
      className="flex items-center rounded-md border border-input"
      role="group"
      aria-label={t("theme.label")}
    >
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          onClick={() => setTheme(option.value)}
          aria-pressed={theme === option.value}
          title={t(option.label as Parameters<typeof t>[0])}
          className={cn(
            "flex size-8 items-center justify-center rounded-[5px] transition-colors",
            theme === option.value
              ? "bg-accent text-accent-foreground"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          <option.icon className="size-4" />
          <span className="sr-only">{t(option.label as Parameters<typeof t>[0])}</span>
        </button>
      ))}
    </div>
  );
}
