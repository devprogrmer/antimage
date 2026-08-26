import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useParams } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { AppShell } from "./AppShell";
import { ThemeProvider } from "../lib/theme";
import type { Session } from "../lib/session";
import { setLocale } from "../i18n";

// Phase A's exit criteria are behavioural -- the shell renders every route, the
// navigation follows the actor's permissions, and keyboard navigation works --
// so they are asserted here rather than eyeballed.

const superAdmin: Session = {
  admin_id: 1,
  role: "super_admin",
  is_super: true,
  permissions: ["reseller:read", "node:read", "subject:read"],
};

const tenant: Session = {
  admin_id: 2,
  role: "reseller",
  is_super: false,
  // Deliberately without reseller:read, which is what the tenant role holds.
  permissions: ["node:read", "subject:read"],
};

function renderShell(session: Session | undefined, initialPath = "/") {
  return render(
    <ThemeProvider>
      <MemoryRouter initialEntries={[initialPath]}>
        <Routes>
          <Route
            element={
              <AppShell session={session} onSignOut={() => {}} onLocaleChange={() => {}} />
            }
          >
            <Route index element={<p>overview-screen</p>} />
            <Route path="nodes" element={<p>nodes-screen</p>} />
            <Route path="nodes/:nodeId" element={<NodeIdProbe />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </ThemeProvider>,
  );
}

// Reads the id back out of the URL, which is the whole point of real routing:
// the route, not component state, decides what is on screen. useParams is the
// same mechanism App.tsx uses to feed ids into the detail screens.
function NodeIdProbe() {
  const { nodeId } = useParams();
  return <p>node-detail:{nodeId}</p>;
}

beforeEach(() => {
  setLocale("en");
  localStorage.clear();
  document.documentElement.classList.remove("dark");
});

afterEach(() => {
  localStorage.clear();
});

describe("AppShell", () => {
  it("renders the screen for the current route", () => {
    renderShell(superAdmin, "/nodes");
    expect(screen.getByText("nodes-screen")).toBeInTheDocument();
  });

  it("routes by URL, so a deep link lands on the right record", () => {
    renderShell(superAdmin, "/nodes/42");
    expect(screen.getByText(/node-detail:42/)).toBeInTheDocument();
  });

  it("offers the tenants tab to an actor holding reseller:read", () => {
    renderShell(superAdmin);
    // Twice: once in the sidebar, once in the small-screen nav. Both are
    // rendered and CSS decides which is visible, so the count is the honest
    // assertion rather than getByRole.
    expect(screen.getAllByRole("link", { name: "Tenants" }).length).toBeGreaterThan(0);
  });

  it("withholds the tenants tab from an actor without reseller:read", () => {
    renderShell(tenant);
    expect(screen.queryByRole("link", { name: "Tenants" })).not.toBeInTheDocument();
    // And the ungated entries are still there, so this is a gate rather than a
    // navigation that failed to render.
    expect(screen.getAllByRole("link", { name: "Nodes" }).length).toBeGreaterThan(0);
  });

  it("withholds gated entries while the session is still unknown", () => {
    // `undefined` is the loading state. A tab that appears and is then withdrawn
    // is worse than one that arrives late.
    renderShell(undefined);
    expect(screen.queryByRole("link", { name: "Tenants" })).not.toBeInTheDocument();
  });

  it("marks the active route for assistive technology", () => {
    renderShell(superAdmin, "/nodes");
    const active = screen.getAllByRole("link", { name: "Nodes" })[0];
    expect(active).toHaveAttribute("aria-current", "page");
  });

  it("navigates by keyboard alone", async () => {
    const user = userEvent.setup();
    renderShell(superAdmin, "/");
    expect(screen.getByText("overview-screen")).toBeInTheDocument();

    const nodesLink = screen.getAllByRole("link", { name: "Nodes" })[0];
    nodesLink.focus();
    expect(nodesLink).toHaveFocus();
    await user.keyboard("{Enter}");

    expect(screen.getByText("nodes-screen")).toBeInTheDocument();
  });
});

describe("theme", () => {
  it("applies the operator's choice over the OS preference", async () => {
    const user = userEvent.setup();
    renderShell(superAdmin);

    await user.click(screen.getByRole("button", { name: "Dark" }));
    expect(document.documentElement.classList.contains("dark")).toBe(true);

    await user.click(screen.getByRole("button", { name: "Light" }));
    expect(document.documentElement.classList.contains("dark")).toBe(false);
  });

  it("remembers the choice across a reload", async () => {
    const user = userEvent.setup();
    const first = renderShell(superAdmin);
    await user.click(screen.getByRole("button", { name: "Dark" }));
    first.unmount();

    renderShell(superAdmin);
    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });

  it("reports which theme is selected", async () => {
    const user = userEvent.setup();
    renderShell(superAdmin);
    await user.click(screen.getByRole("button", { name: "Dark" }));
    expect(screen.getByRole("button", { name: "Dark" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByRole("button", { name: "Light" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );
  });
});

describe("localisation", () => {
  it("mirrors the shell for an RTL locale", () => {
    setLocale("fa");
    renderShell(superAdmin);
    expect(document.documentElement.dir).toBe("rtl");
    // The label came from the Persian catalogue, so the shell is translated
    // rather than merely flipped.
    expect(screen.getAllByRole("link", { name: "گره‌ها" }).length).toBeGreaterThan(0);
    setLocale("en");
  });
});
