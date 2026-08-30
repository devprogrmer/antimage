import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { Nodes } from "../routes/Nodes";
import { NodeActions } from "./NodeActions";
import { setLocale } from "../i18n";

// Adding a node and enrolling it were both API-only: the row could not be
// created from the browser, and even once created there was no way to get the
// token that makes the host join. Taking a node out of service was API-only
// too, which is the one thing an operator needs during an incident.

interface Call {
  method: string;
  path: string;
  body: unknown;
}

let calls: Call[] = [];
let routes: Record<string, { status?: number; body?: unknown }> = {};

function stubFetch() {
  vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    calls.push({
      method,
      path,
      body: init?.body === undefined ? undefined : JSON.parse(String(init.body)),
    });
    const bare = path.split("?")[0];
    const route =
      routes[`${method} ${path}`] ?? routes[path] ?? routes[`${method} ${bare}`] ?? routes[bare];
    if (route === undefined) {
      return new Response(JSON.stringify({ error: { code: "not_found", message: "no stub" } }), {
        status: 404,
      });
    }
    return new Response(JSON.stringify(route.body), { status: route.status ?? 200 });
  });
}

function renderIt(node: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>{node}</MemoryRouter>
    </QueryClientProvider>,
  );
}

const twoNodes = {
  nodes: [
    {
      id: 1, name: "fra-1", address: "203.0.113.10", status: "online",
      desired_revision: 4, applied_revision: 4, last_seen_at: 100, online: true,
    },
    {
      id: 2, name: "ams-1", address: "198.51.100.7", status: "disabled",
      desired_revision: 4, applied_revision: 3, last_seen_at: 50, online: false,
    },
  ],
};

beforeEach(() => {
  setLocale("en");
  calls = [];
  routes = { "/api/v1/nodes": { body: twoNodes } };
  stubFetch();
  // The node list opens an SSE stream; jsdom has no EventSource.
  vi.stubGlobal(
    "EventSource",
    class {
      close() {}
      addEventListener() {}
      removeEventListener() {}
    },
  );
});
afterEach(() => vi.unstubAllGlobals());

describe("adding a node", () => {
  // Create and enrol are ONE flow: a node without a token is inert, and the
  // list gives the operator nothing to come back to it for.
  it("creates the node and immediately issues an enrolment token", async () => {
    routes["POST /api/v1/nodes"] = { status: 201, body: { id: 7 } };
    routes["POST /api/v1/nodes/7/enroll-token"] = {
      status: 201,
      body: {
        token: "tok-xyz",
        command: "curl -fsSL https://panel/install.sh | sudo bash -s -- --token tok-xyz",
        expires_at: 1700000000,
      },
    };
    const user = userEvent.setup({ delay: null });
    renderIt(<Nodes onSelect={() => {}} />);
    await screen.findByText("fra-1");

    await user.click(screen.getByRole("button", { name: "Add node" }));
    const sheet = await screen.findByRole("dialog");
    await user.type(within(sheet).getByLabelText("Name"), "sgp-1");
    await user.type(within(sheet).getByLabelText("Address"), "203.0.113.99");
    await user.click(within(sheet).getByRole("button", { name: "Add and enrol" }));

    await waitFor(() => {
      const created = calls.find((c) => c.method === "POST" && c.path === "/api/v1/nodes");
      expect(created?.body).toEqual({ name: "sgp-1", address: "203.0.113.99" });
    });
    // The token request follows without the operator asking.
    await waitFor(() =>
      expect(calls.some((c) => c.path === "/api/v1/nodes/7/enroll-token")).toBe(true),
    );

    // The command is the artefact they actually need.
    expect(await screen.findByText(/curl -fsSL/)).toBeInTheDocument();
  });

  // Only the hash is stored, so there is no second chance to read it. The
  // operator has to be told BEFORE they close the sheet.
  it("warns that the token is shown only once", async () => {
    routes["POST /api/v1/nodes"] = { status: 201, body: { id: 7 } };
    routes["POST /api/v1/nodes/7/enroll-token"] = {
      status: 201,
      body: { token: "tok-xyz", command: "install...", expires_at: 1700000000 },
    };
    const user = userEvent.setup({ delay: null });
    renderIt(<Nodes onSelect={() => {}} />);
    await screen.findByText("fra-1");

    await user.click(screen.getByRole("button", { name: "Add node" }));
    const sheet = await screen.findByRole("dialog");
    await user.type(within(sheet).getByLabelText("Name"), "sgp-1");
    await user.type(within(sheet).getByLabelText("Address"), "203.0.113.99");
    await user.click(within(sheet).getByRole("button", { name: "Add and enrol" }));

    expect(await screen.findByRole("status")).toHaveTextContent(/shown once/);
  });

  it("will not submit without a name and an address", async () => {
    const user = userEvent.setup({ delay: null });
    renderIt(<Nodes onSelect={() => {}} />);
    await screen.findByText("fra-1");

    await user.click(screen.getByRole("button", { name: "Add node" }));
    const sheet = await screen.findByRole("dialog");
    expect(within(sheet).getByRole("button", { name: "Add and enrol" })).toBeDisabled();

    await user.type(within(sheet).getByLabelText("Name"), "sgp-1");
    // Still disabled: an address is what clients connect to, and a node
    // without one is unreachable.
    expect(within(sheet).getByRole("button", { name: "Add and enrol" })).toBeDisabled();
  });

  it("shows a refusal rather than a half-created node", async () => {
    routes["POST /api/v1/nodes"] = {
      status: 409,
      body: { error: { code: "conflict", message: "a node with that name exists" } },
    };
    const user = userEvent.setup({ delay: null });
    renderIt(<Nodes onSelect={() => {}} />);
    await screen.findByText("fra-1");

    await user.click(screen.getByRole("button", { name: "Add node" }));
    const sheet = await screen.findByRole("dialog");
    await user.type(within(sheet).getByLabelText("Name"), "fra-1");
    await user.type(within(sheet).getByLabelText("Address"), "203.0.113.99");
    await user.click(within(sheet).getByRole("button", { name: "Add and enrol" }));

    expect(await screen.findByText(/a node with that name exists/)).toBeInTheDocument();
    // No token was requested for a node that was never created.
    expect(calls.some((c) => c.path.includes("enroll-token"))).toBe(false);
  });
});

describe("node lifecycle actions", () => {
  const online = { id: 1, name: "fra-1", status: "online" };
  const off = { id: 2, name: "ams-1", status: "disabled" };

  // Disabling drops every connected user, so it asks and names the node.
  it("asks before taking a node out of service", async () => {
    routes["POST /api/v1/nodes/1/disable"] = { body: {} };
    const user = userEvent.setup({ delay: null });
    renderIt(<NodeActions node={online} />);

    await user.click(screen.getByRole("button", { name: "Take out of service" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(/Everyone connected through fra-1 is disconnected/);
    expect(calls.some((c) => c.method === "POST")).toBe(false);

    await user.click(within(dialog).getByRole("button", { name: "Take out of service" }));
    await waitFor(() =>
      expect(calls.some((c) => c.path === "/api/v1/nodes/1/disable")).toBe(true),
    );
  });

  it("offers to bring a disabled node back", async () => {
    routes["POST /api/v1/nodes/2/enable"] = { body: {} };
    const user = userEvent.setup({ delay: null });
    renderIt(<NodeActions node={off} />);

    // The pair toggles on status: two buttons where only one applies is a
    // control an operator has to read twice.
    expect(screen.queryByRole("button", { name: "Take out of service" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Bring into service" }));

    // Bringing a node back disturbs nothing, so it does not ask.
    await waitFor(() =>
      expect(calls.some((c) => c.path === "/api/v1/nodes/2/enable")).toBe(true),
    );
  });

  // Maintenance DRAINS: existing sessions continue, so it is not disruptive
  // and does not need a confirmation.
  it("enters maintenance without a confirmation", async () => {
    routes["POST /api/v1/nodes/1/maintenance"] = { body: {} };
    const user = userEvent.setup({ delay: null });
    renderIt(<NodeActions node={online} />);

    await user.click(screen.getByRole("button", { name: "Enter Maintenance" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.path === "/api/v1/nodes/1/maintenance");
      expect(sent?.body).toMatchObject({ enable: true });
    });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("exits maintenance with the same control", async () => {
    routes["POST /api/v1/nodes/1/maintenance"] = { body: {} };
    const user = userEvent.setup({ delay: null });
    renderIt(<NodeActions node={{ ...online, status: "maintenance" }} />);

    await user.click(screen.getByRole("button", { name: "Exit Maintenance" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.path === "/api/v1/nodes/1/maintenance");
      expect(sent?.body).toMatchObject({ enable: false });
    });
  });

  // Restarting drops sessions; syncing does not.
  it("asks before restarting but not before syncing", async () => {
    routes["POST /api/v1/nodes/1/sync"] = { body: {} };
    routes["POST /api/v1/nodes/1/restart"] = { body: {} };
    const user = userEvent.setup({ delay: null });
    renderIt(<NodeActions node={online} />);

    await user.click(screen.getByRole("button", { name: "Sync now" }));
    await waitFor(() =>
      expect(calls.some((c) => c.path === "/api/v1/nodes/1/sync")).toBe(true),
    );
    expect(screen.queryByRole("dialog")).toBeNull();

    await user.click(screen.getByRole("button", { name: "Restart" }));
    expect(await screen.findByRole("dialog")).toHaveTextContent(/disconnected while it restarts/);
  });
});

describe("fleet actions", () => {
  it("offers nothing until a node is selected", async () => {
    renderIt(<Nodes onSelect={() => {}} />);
    await screen.findByText("fra-1");
    expect(screen.queryByText(/selected/)).toBeNull();
  });

  it("acts on exactly the selected nodes", async () => {
    routes["POST /api/v1/nodes/bulk/action"] = {
      body: { total_nodes: 1, success_count: 1, failure_count: 0, results: [] },
    };
    const user = userEvent.setup({ delay: null });
    renderIt(<Nodes onSelect={() => {}} />);
    await screen.findByText("fra-1");

    await user.click(screen.getByRole("checkbox", { name: "fra-1" }));
    const bar = (await screen.findByText("1 selected")).closest("div")!;
    await user.click(within(bar).getByRole("button", { name: "Sync now" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.path === "/api/v1/nodes/bulk/action");
      expect(sent?.body).toMatchObject({ node_ids: [1], action: "sync" });
    });
  });

  // A fleet action where two of nine nodes failed is the normal case. "Done"
  // would hide it, and a count alone is not actionable -- the operator needs
  // to know WHICH.
  it("names the nodes that failed", async () => {
    routes["POST /api/v1/nodes/bulk/action"] = {
      body: {
        total_nodes: 2, success_count: 1, failure_count: 1,
        results: [
          { node_id: 1, node_name: "fra-1", success: true },
          { node_id: 2, node_name: "ams-1", success: false, error: "node is unreachable" },
        ],
      },
    };
    const user = userEvent.setup({ delay: null });
    renderIt(<Nodes onSelect={() => {}} />);
    await screen.findByText("fra-1");

    await user.click(screen.getByRole("checkbox", { name: "Select all rows" }));
    const bar = (await screen.findByText("2 selected")).closest("div")!;
    await user.click(within(bar).getByRole("button", { name: "Sync now" }));

    // Scoped to the bulk bar: the node list has its own status elements.
    const status = await within(bar).findByRole("status");
    expect(status).toHaveTextContent("1 changed, 1 failed");
    expect(status).toHaveTextContent("ams-1");
    expect(status).toHaveTextContent("node is unreachable");
  });

  it("asks before restarting a whole selection", async () => {
    routes["POST /api/v1/nodes/bulk/action"] = {
      body: { total_nodes: 2, success_count: 2, failure_count: 0, results: [] },
    };
    const user = userEvent.setup({ delay: null });
    renderIt(<Nodes onSelect={() => {}} />);
    await screen.findByText("fra-1");

    await user.click(screen.getByRole("checkbox", { name: "Select all rows" }));
    const bar = (await screen.findByText("2 selected")).closest("div")!;
    await user.click(within(bar).getByRole("button", { name: "Restart" }));

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("This affects 2 nodes.");
    expect(calls.some((c) => c.path.includes("bulk/action"))).toBe(false);
  });

  // Over a mixed selection "toggle maintenance" has no single meaning, so the
  // bulk path sends an explicit flag rather than guessing per node.
  it("sends maintenance as an explicit flag, not a toggle", async () => {
    routes["POST /api/v1/nodes/bulk/action"] = {
      body: { total_nodes: 2, success_count: 2, failure_count: 0, results: [] },
    };
    const user = userEvent.setup({ delay: null });
    renderIt(<Nodes onSelect={() => {}} />);
    await screen.findByText("fra-1");

    await user.click(screen.getByRole("checkbox", { name: "Select all rows" }));
    const bar = (await screen.findByText("2 selected")).closest("div")!;
    await user.click(within(bar).getByRole("button", { name: "Enter Maintenance" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.path === "/api/v1/nodes/bulk/action");
      expect(sent?.body).toMatchObject({ action: "maintenance", maintenance_enable: true });
    });
  });
});
