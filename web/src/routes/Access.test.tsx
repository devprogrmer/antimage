import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { Access } from "./Access";
import { setLocale } from "../i18n";

interface Call {
  method: string;
  path: string;
  body?: Record<string, unknown>;
}

let calls: Call[] = [];
let routes: Record<string, { status?: number; body?: unknown }> = {};

function stubFetch() {
  vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const body = init?.body
      ? (JSON.parse(String(init.body)) as Record<string, unknown>)
      : undefined;
    calls.push({ method, path, body });
    const key = `${method} ${path}`;
    const route = routes[key] ?? routes[path];
    if (route === undefined) {
      return new Response(
        JSON.stringify({ error: { code: "not_found", message: "no stub" } }),
        { status: 404 },
      );
    }
    return new Response(JSON.stringify(route.body ?? {}), { status: route.status ?? 200 });
  });
}

function renderIt(node: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

beforeEach(() => {
  setLocale("en");
  calls = [];
  routes = {
    "/api/v1/auth/me": {
      body: {
        admin_id: 1,
        role: "super",
        is_super: true,
        permissions: ["admin:manage"],
        totp_enabled: false,
      },
    },
    "/api/v1/admins": {
      body: {
        admins: [
          {
            id: 1,
            username: "root",
            role_id: 1,
            role_name: "super_admin",
            status: "active",
            totp_enabled: false,
            created_at: 1_700_000_000,
            scopes: 0,
          },
          {
            id: 2,
            username: "op",
            role_id: 2,
            role_name: "operator",
            status: "active",
            totp_enabled: true,
            created_at: 1_700_000_100,
            scopes: 3,
          },
        ],
      },
    },
    "/api/v1/roles": {
      body: {
        roles: [
          { id: 1, name: "super_admin", is_builtin: true, permissions: ["*"], assigned: 1 },
          { id: 2, name: "operator", is_builtin: false, permissions: ["node:read"], assigned: 1 },
        ],
      },
    },
  };
  stubFetch();
});
afterEach(() => vi.unstubAllGlobals());

describe("Access page", () => {
  it("lists admins and disables the suspend button for the signed-in user", async () => {
    renderIt(<Access />);
    // Both admins present.
    expect(await screen.findByText("root")).toBeInTheDocument();
    expect(screen.getByText("op")).toBeInTheDocument();
    // "you" chip is on root (admin_id 1 in the /me stub).
    expect(screen.getByText("you")).toBeInTheDocument();
    // Suspend appears for op but not for root: the tests uses count to guard
    // against the anti-lockout rule being removed.
    const suspendButtons = screen.getAllByRole("button", { name: "Suspend" });
    expect(suspendButtons.length).toBe(1);
  });

  it("creates an admin through the dialog and refreshes the roster", async () => {
    routes["POST /api/v1/admins"] = { status: 201, body: { id: 9, username: "new" } };
    const user = userEvent.setup({ delay: null });
    renderIt(<Access />);

    await user.click(await screen.findByRole("button", { name: /New admin/i }));
    await user.type(screen.getByLabelText(/^Username/i), "brand-new");
    await user.type(screen.getByLabelText(/^Password/i), "a strong one");
    await user.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "POST" && c.path === "/api/v1/admins");
      expect(sent?.body).toMatchObject({
        username: "brand-new",
        role_id: 1,
      });
      // Password is sent verbatim (the SERVER hashes it) but NOT logged
      // anywhere in the response chain. The test asserts it is present so
      // a regression that drops the field never ships.
      expect(sent?.body?.password).toBe("a strong one");
    });
  });

  it("suspend confirms and sends DELETE to the admins endpoint", async () => {
    routes["DELETE /api/v1/admins/2"] = { status: 204, body: {} };
    const user = userEvent.setup({ delay: null });
    renderIt(<Access />);
    await screen.findByText("op");

    await user.click(screen.getByRole("button", { name: "Suspend" }));
    // ConfirmDialog opens with the destructive button
    const dialog = await screen.findByRole("dialog");
    await user.click(
      Array.from(dialog.querySelectorAll("button")).find((b) => b.textContent === "Suspend")!,
    );

    await waitFor(() => {
      const sent = calls.find(
        (c) => c.method === "DELETE" && c.path === "/api/v1/admins/2",
      );
      expect(sent).toBeTruthy();
    });
  });
});
