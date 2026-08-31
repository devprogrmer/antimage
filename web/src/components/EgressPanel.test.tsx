import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { EgressPanel } from "./EgressPanel";
import { setLocale } from "../i18n";

// The backend has held PUT /outbounds/{id} and PUT /routing/{id} since they
// were built; this panel only ever called create and delete. An operator
// changing a wrong port or re-prioritizing a rule had to delete and recreate
// under a new id, which also orphans anything that named the old outbound's
// tag. These tests prove the Edit buttons reach the routes that already exist.

interface Call {
  method: string;
  path: string;
  body: unknown;
}

let calls: Call[] = [];
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

function renderPanel(node: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

const caps = {
  supported: true,
  adapter_kind: "xray",
  outbound_kinds: ["freedom", "socks"],
  builtin_tags: ["direct", "block"],
};

const outbound = {
  id: 7,
  node_id: 1,
  tag: "upstream-a",
  kind: "socks",
  params: { server: "10.0.0.1", port: 1080 },
  enabled: true,
};

const rule = {
  id: 9,
  node_id: 1,
  priority: 5,
  domains: ["example.com"],
  ip_cidrs: null,
  geoip: null,
  geosite: null,
  ports: null,
  inbound_tags: null,
  subject_ids: null,
  network: "tcp",
  outbound_tag: "upstream-a",
  enabled: true,
};

function seed() {
  routes["/api/v1/nodes/1/egress/capabilities"] = { body: caps };
  routes["/api/v1/nodes/1/outbounds"] = { body: { outbounds: [outbound] } };
  routes["/api/v1/nodes/1/routing"] = { body: { rules: [rule] } };
}

beforeEach(() => {
  setLocale("en");
  calls = [];
  routes = {};
  stubFetch();
});
afterEach(() => vi.unstubAllGlobals());

describe("EgressPanel editing", () => {
  it("edits an outbound through the PUT route the backend already had", async () => {
    seed();
    routes["PUT /api/v1/nodes/1/outbounds/7"] = { status: 204, body: {} };
    const user = userEvent.setup({ delay: null });
    renderPanel(<EgressPanel nodeId={1} />);

    const editButtons = await screen.findAllByRole("button", { name: "Edit" });
    // The outbound row's Edit button is the first in document order.
    await user.click(editButtons[0]);
    const tagInput = screen.getByDisplayValue("upstream-a");
    await user.clear(tagInput);
    await user.type(tagInput, "upstream-b");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      const sent = calls.find(
        (c) => c.method === "PUT" && c.path === "/api/v1/nodes/1/outbounds/7",
      );
      expect(sent).toBeTruthy();
      expect((sent!.body as { tag: string }).tag).toBe("upstream-b");
    });
  });

  it("edits a routing rule through the PUT route the backend already had", async () => {
    seed();
    routes["PUT /api/v1/nodes/1/routing/9"] = { status: 204, body: {} };
    const user = userEvent.setup({ delay: null });
    renderPanel(<EgressPanel nodeId={1} />);

    // Two "Edit" buttons render (outbound row, rule row); the rule row's is
    // the second in document order.
    const editButtons = await screen.findAllByRole("button", { name: "Edit" });
    await user.click(editButtons[1]);

    const priorityInputs = screen.getAllByDisplayValue("5");
    await user.clear(priorityInputs[0]);
    await user.type(priorityInputs[0], "20");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      const sent = calls.find(
        (c) => c.method === "PUT" && c.path === "/api/v1/nodes/1/routing/9",
      );
      expect(sent).toBeTruthy();
      expect((sent!.body as { priority: number }).priority).toBe(20);
    });
  });
});
