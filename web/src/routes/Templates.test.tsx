import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { Templates } from "./Templates";
import { setLocale } from "../i18n";

let calls: Array<{ method: string; path: string; body: unknown }> = [];
let routes: Record<string, { status?: number; body: unknown }> = {};

function stubFetch() {
  vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    calls.push({
      method,
      path,
      body: init?.body === undefined ? undefined : JSON.parse(String(init.body)),
    });
    const route = routes[`${method} ${path}`] ?? routes[path];
    if (route === undefined) {
      return new Response(JSON.stringify({ error: { code: "not_found", message: "no stub" } }), {
        status: 404,
      });
    }
    return new Response(JSON.stringify(route.body), { status: route.status ?? 200 });
  });
}

function renderScreen(node: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

beforeEach(() => {
  setLocale("en");
  calls = [];
  routes = {
    "/api/v1/templates/services": {
      body: {
        templates: [
          {
            id: 1, name: "vless-443", adapter_kind: "xray",
            params_json: '{"protocol":"vless","port":443}',
            description: "standard", tags: ["prod"], is_public: true,
            created_by: 1, created_at: 1,
          },
        ],
      },
    },
    "/api/v1/presets/users": {
      body: {
        presets: [
          {
            id: 1, name: "monthly-50g", description: "",
            quota_bytes: 53687091200, validity_days: 30,
            is_public: false, created_by: 1, created_at: 1,
          },
          {
            id: 2, name: "staff", description: "", quota_bytes: null,
            validity_days: null, is_public: false, created_by: 1, created_at: 1,
          },
        ],
      },
    },
  };
  stubFetch();
});
afterEach(() => vi.unstubAllGlobals());

describe("service templates", () => {
  // These endpoints marshalled with Go field names until this change; the UI
  // reads snake_case, so a regression to PascalCase renders a blank card.
  it("reads the snake_case fields the API now returns", async () => {
    renderScreen(<Templates />);
    expect(await screen.findByText("vless-443")).toBeInTheDocument();
    expect(screen.getByText("xray")).toBeInTheDocument();
    expect(screen.getByText(/"protocol":"vless"/)).toBeInTheDocument();
  });

  it("refuses to submit params that are not JSON", async () => {
    const user = userEvent.setup();
    renderScreen(<Templates />);
    await screen.findByText("vless-443");

    await user.click(screen.getByRole("button", { name: "New template" }));
    const sheet = await screen.findByRole("dialog");
    await user.type(within(sheet).getByLabelText("Name"), "broken");
    const params = within(sheet).getByLabelText("Parameters document");
    await user.clear(params);
    await user.type(params, "not json at all");
    await user.click(within(sheet).getByRole("button", { name: "Create" }));

    expect(await within(sheet).findByRole("alert")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("sends the template the operator filled in", async () => {
    routes["POST /api/v1/templates/services"] = { status: 201, body: { id: 2 } };
    const user = userEvent.setup();
    renderScreen(<Templates />);
    await screen.findByText("vless-443");

    await user.click(screen.getByRole("button", { name: "New template" }));
    const sheet = await screen.findByRole("dialog");
    await user.type(within(sheet).getByLabelText("Name"), "trojan-8443");
    await user.click(within(sheet).getByRole("button", { name: "Create" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "POST");
      expect(sent?.body).toMatchObject({ name: "trojan-8443", adapter_kind: "xray" });
    });
  });

  it("asks before deleting and names the template", async () => {
    routes["DELETE /api/v1/templates/services/1"] = { status: 204, body: {} };
    const user = userEvent.setup();
    renderScreen(<Templates />);
    await screen.findByText("vless-443");

    await user.click(screen.getByRole("button", { name: "Delete" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("vless-443");
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);

    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
    await waitFor(() =>
      expect(calls.some((c) => c.path === "/api/v1/templates/services/1")).toBe(true),
    );
  });
});

describe("user presets", () => {
  // null is unlimited, not zero. Collapsing the two turns a preset meaning
  // "no cap" into one meaning "no traffic".
  it("shows an absent quota as unlimited rather than zero", async () => {
    const user = userEvent.setup();
    renderScreen(<Templates />);
    await screen.findByText("vless-443");

    await user.click(screen.getByRole("tab", { name: "User presets" }));

    expect(await screen.findByText("staff")).toBeInTheDocument();
    // Two unlimited values on the staff preset: quota and validity.
    expect(screen.getAllByText("Unlimited").length).toBeGreaterThanOrEqual(2);
    // And a real quota is still rendered as a number.
    expect(screen.getByText("monthly-50g")).toBeInTheDocument();
  });
});
