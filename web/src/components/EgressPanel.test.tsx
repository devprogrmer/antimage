import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
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
  supports_balancer: true,
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
  balancer_tag: "",
  enabled: true,
};

function seed() {
  routes["/api/v1/nodes/1/egress/capabilities"] = { body: caps };
  routes["/api/v1/nodes/1/outbounds"] = { body: { outbounds: [outbound] } };
  routes["/api/v1/nodes/1/routing"] = { body: { rules: [rule] } };
  routes["/api/v1/nodes/1/balancers"] = { body: { balancers: [] } };
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

describe("EgressPanel outbound params", () => {
  const schemaCaps = {
    ...caps,
    outbound_schema: {
      type: "object",
      properties: {
        address: { type: "string" },
        port: { type: "integer" },
      },
      required: ["address"],
    },
  };

  it("creates an outbound through the schema-driven form when the node reports one", async () => {
    routes["/api/v1/nodes/1/egress/capabilities"] = { body: schemaCaps };
    routes["/api/v1/nodes/1/outbounds"] = { body: { outbounds: [] } };
    routes["/api/v1/nodes/1/routing"] = { body: { rules: [] } };
    routes["/api/v1/nodes/1/balancers"] = { body: { balancers: [] } };
    routes["POST /api/v1/nodes/1/outbounds"] = {
      status: 201,
      body: { id: 1, node_id: 1, tag: "up", kind: "freedom", params: {}, enabled: true },
    };
    const user = userEvent.setup({ delay: null });
    renderPanel(<EgressPanel nodeId={1} />);

    // "Tag" also labels the balancer form's field; the outbound form's is
    // first in document order, same convention the balancer test below uses.
    const tagInputs = await screen.findAllByLabelText("Tag");
    // Waited for real data first, or this could pass for the wrong reason --
    // nothing rendered yet, not because JSON mode was deliberately not the
    // default.
    expect(screen.queryByLabelText("Parameters")).not.toBeInTheDocument();
    await user.type(tagInputs[0], "up");
    // A required field's label also carries a "required" marker span, so its
    // accessible text is "addressrequired" rather than "address" alone.
    await user.type(screen.getByLabelText("address", { exact: false }), "10.0.0.9");
    await user.type(screen.getByLabelText("port"), "1080");
    await user.click(screen.getAllByRole("button", { name: "Create" })[0]);

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "POST" && c.path === "/api/v1/nodes/1/outbounds");
      expect(sent).toBeTruthy();
      expect(sent!.body).toMatchObject({
        tag: "up",
        params: { address: "10.0.0.9", port: 1080 },
      });
    });
  });

  it("falls back to a JSON textarea when the node reports no outbound schema", async () => {
    // The fixture used throughout this file (`caps`) carries no
    // outbound_schema -- exactly a node whose adapter predates schema
    // reporting. There is nothing to build a typed form from, but creating
    // an outbound must still work.
    seed();
    routes["POST /api/v1/nodes/1/outbounds"] = {
      status: 201,
      body: { id: 2, node_id: 1, tag: "raw", kind: "freedom", params: {}, enabled: true },
    };
    const user = userEvent.setup({ delay: null });
    renderPanel(<EgressPanel nodeId={1} />);

    const tagInputs = await screen.findAllByLabelText("Tag");
    await user.type(tagInputs[0], "raw");
    const jsonField = await screen.findByLabelText("Parameters");
    await user.clear(jsonField);
    // userEvent.type reads { as its own escape syntax ({{ types one literal
    // brace); a closing } needs no such escaping.
    await user.type(jsonField, '{{"server":"10.0.0.2"}');
    await user.click(screen.getAllByRole("button", { name: "Create" })[0]);

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "POST" && c.path === "/api/v1/nodes/1/outbounds");
      expect(sent).toBeTruthy();
      expect(sent!.body).toMatchObject({ tag: "raw", params: { server: "10.0.0.2" } });
    });
  });
});

describe("EgressPanel balancers", () => {
  it("hides the balancers section on a node whose adapter has no balancer concept", async () => {
    routes["/api/v1/nodes/1/egress/capabilities"] = {
      body: { ...caps, supports_balancer: false },
    };
    routes["/api/v1/nodes/1/outbounds"] = { body: { outbounds: [] } };
    routes["/api/v1/nodes/1/routing"] = { body: { rules: [] } };
    renderPanel(<EgressPanel nodeId={1} />);

    // Wait for real data before asserting an absence, or the assertion
    // could pass for the wrong reason -- nothing rendered yet, not because
    // the balancers section was deliberately omitted.
    await screen.findByText("Outbounds");
    expect(screen.queryByText("Balancers")).not.toBeInTheDocument();
    // And the balancers endpoint must not even be called -- there is no
    // reason to ask a node that cannot answer.
    expect(calls.some((c) => c.path === "/api/v1/nodes/1/balancers")).toBe(false);
  });

  it("creates a balancer and posts the exact request body", async () => {
    seed();
    routes["POST /api/v1/nodes/1/balancers"] = {
      status: 201,
      body: { id: 3, node_id: 1, tag: "b1", selector: ["warp-"], strategy: "least_ping", enabled: true },
    };
    const user = userEvent.setup({ delay: null });
    renderPanel(<EgressPanel nodeId={1} />);

    const heading = await screen.findByText("Balancers");
    // "Tag" is ambiguous page-wide -- the outbounds form has its own -- but
    // unambiguous within this section, the same way a sighted operator
    // reads it under the "Balancers" heading rather than by label alone.
    const section = within(heading.closest("div")!);
    await user.type(section.getByLabelText("Tag"), "b1");
    await user.type(section.getByLabelText("Selector"), "warp-");
    await user.selectOptions(section.getByLabelText("Strategy"), "least_ping");
    await user.click(section.getByRole("button", { name: "Create" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "POST" && c.path === "/api/v1/nodes/1/balancers");
      expect(sent).toBeTruthy();
      expect(sent!.body).toMatchObject({ tag: "b1", selector: ["warp-"], strategy: "least_ping" });
    });
  });

  it("offers a created balancer as a routing rule target alongside outbounds", async () => {
    routes["/api/v1/nodes/1/egress/capabilities"] = { body: caps };
    routes["/api/v1/nodes/1/outbounds"] = { body: { outbounds: [outbound] } };
    routes["/api/v1/nodes/1/routing"] = { body: { rules: [] } };
    routes["/api/v1/nodes/1/balancers"] = {
      body: { balancers: [{ id: 3, node_id: 1, tag: "b1", selector: ["warp-"], strategy: "random", enabled: true }] },
    };
    routes["POST /api/v1/nodes/1/routing"] = {
      status: 201,
      body: { id: 10, node_id: 1, priority: 0, domains: ["x.com"], outbound_tag: "", balancer_tag: "b1", enabled: true },
    };
    const user = userEvent.setup({ delay: null });
    renderPanel(<EgressPanel nodeId={1} />);

    // Confirms the balancer actually loaded into the picker's data, not
    // just that some element somewhere says "b1" -- the option and the
    // balancer table row both would, once balancers load.
    await screen.findByRole("option", { name: "b1" });
    await user.type(screen.getByLabelText("Domains"), "x.com");
    // "Send to" picker: the balancer option must be selectable, prefixed to
    // disambiguate it from an outbound sharing the same tag namespace.
    await user.selectOptions(screen.getByLabelText("Send to"), "balancer:b1");
    // Three "Create" buttons render (outbound, rule, balancer forms); the
    // rule form's is the second in document order.
    await user.click(screen.getAllByRole("button", { name: "Create" })[1]);

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "POST" && c.path === "/api/v1/nodes/1/routing");
      expect(sent).toBeTruthy();
      expect(sent!.body).toMatchObject({ outbound_tag: "", balancer_tag: "b1" });
    });
  });

  it("shows a balancer-targeting rule's target distinctly from an outbound-targeting one", async () => {
    routes["/api/v1/nodes/1/egress/capabilities"] = { body: caps };
    routes["/api/v1/nodes/1/outbounds"] = { body: { outbounds: [outbound] } };
    routes["/api/v1/nodes/1/routing"] = {
      body: {
        rules: [
          { ...rule, id: 11, outbound_tag: "", balancer_tag: "b1" },
        ],
      },
    };
    routes["/api/v1/nodes/1/balancers"] = {
      body: { balancers: [{ id: 3, node_id: 1, tag: "b1", selector: ["warp-"], strategy: "random", enabled: true }] },
    };
    renderPanel(<EgressPanel nodeId={1} />);

    expect(await screen.findByText("Balancers: b1")).toBeInTheDocument();
  });
});
