import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { NodeReconciliation } from "./NodeReconciliation";
import { NodeAdapters } from "./NodeAdapters";
import { NodeHealth } from "./NodeHealth";
import { setLocale } from "../i18n";

let calls: Array<{ method: string; path: string }> = [];
let routes: Record<string, { status?: number; body: unknown }> = {};

function stubFetch() {
  vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    calls.push({ method, path });
    // Falls back to the bare path so a request carrying a query string still
    // resolves; NodeHealth asks for ?limit=100.
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

function renderPanel(node: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

function session(permissions: string[]) {
  routes["/api/v1/auth/me"] = {
    body: { admin_id: 1, role: "admin", is_super: false, permissions },
  };
}

const converged = {
  node_id: 1, node_name: "edge-1", status: "online",
  desired_revision: 7, applied_revision: 7,
  drift_detected: false, needs_sync: false, recent_runs: [],
};

beforeEach(() => {
  setLocale("en");
  calls = [];
  routes = { "/api/v1/nodes/1/reconciliation": { body: converged } };
  session(["node:read", "node:write"]);
  stubFetch();
});
afterEach(() => vi.unstubAllGlobals());

describe("NodeReconciliation", () => {
  // §3.3: "why is this node not converged" answered from state the panel
  // already holds. The endpoint answered it and had no client.
  it("reports drift when the revisions disagree", async () => {
    routes["/api/v1/nodes/1/reconciliation"] = {
      body: { ...converged, applied_revision: 5, drift_detected: true },
    };
    renderPanel(<NodeReconciliation nodeId={1} />);
    expect(await screen.findByText("Drift")).toBeInTheDocument();
  });

  it("reports convergence when they agree", async () => {
    renderPanel(<NodeReconciliation nodeId={1} />);
    expect(await screen.findByText("Converged")).toBeInTheDocument();
  });

  // The node's own reason, verbatim: more use to an operator than a sentence
  // written here about failures in general.
  it("shows the node's last sync error", async () => {
    routes["/api/v1/nodes/1/reconciliation"] = {
      body: { ...converged, last_sync_error: "dial tcp 10.0.0.5:9443: refused" },
    };
    renderPanel(<NodeReconciliation nodeId={1} />);
    expect(await screen.findByText(/dial tcp 10.0.0.5:9443: refused/)).toBeInTheDocument();
  });

  it("offers no actions to a reader", async () => {
    session(["node:read"]);
    renderPanel(<NodeReconciliation nodeId={1} />);
    await screen.findByText("Converged");
    expect(screen.queryByRole("button", { name: "Restart" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Sync now" })).toBeNull();
  });

  it("asks before restarting, and sends nothing until confirmed", async () => {
    routes["POST /api/v1/nodes/1/restart"] = { status: 204, body: {} };
    const user = userEvent.setup();
    renderPanel(<NodeReconciliation nodeId={1} />);

    await user.click(await screen.findByRole("button", { name: "Restart" }));
    const dialog = await screen.findByRole("dialog");
    expect(calls.some((c) => c.method === "POST")).toBe(false);

    await user.click(within(dialog).getByRole("button", { name: "Confirm" }));
    await waitFor(() =>
      expect(calls.some((c) => c.path === "/api/v1/nodes/1/restart")).toBe(true),
    );
  });

  // Saying "sessions drop" for an action that drops nothing trains an operator
  // to stop reading the sentence.
  it("does not claim a sync drops sessions", async () => {
    const user = userEvent.setup();
    renderPanel(<NodeReconciliation nodeId={1} />);

    await user.click(await screen.findByRole("button", { name: "Sync now" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(/Sessions are not dropped/i);

    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    await user.click(screen.getByRole("button", { name: "Restart" }));
    expect(await screen.findByRole("dialog")).toHaveTextContent(/sessions drop/i);
  });
});

describe("NodeAdapters", () => {
  beforeEach(() => {
    routes["/api/v1/nodes/1/adapters"] = {
      body: {
        adapters: [
          { kind: "xray", version: "1.8.4", capabilities: ["hot_user_add"], reported_at: 1 },
        ],
      },
    };
    routes["/api/v1/nodes/1/capabilities"] = {
      body: {
        capabilities: [
          { protocol: "vless", available: true, version: "1.8.4", detected_at: 1, last_check_at: 2 },
          { protocol: "hysteria2", available: false, detected_at: 1, last_check_at: 2 },
        ],
      },
    };
  });

  it("shows what the node reported and what the probe found", async () => {
    renderPanel(<NodeAdapters nodeId={1} />);
    expect(await screen.findByText("xray")).toBeInTheDocument();
    expect(screen.getByText("hot_user_add")).toBeInTheDocument();
    // The interesting case is the disagreement: a protocol the probe could not
    // find is a node that will accept an inbound and fail to apply it.
    expect(screen.getByText("Available")).toBeInTheDocument();
    expect(screen.getByText("Unavailable")).toBeInTheDocument();
  });

  it("explains a node that has reported nothing", async () => {
    routes["/api/v1/nodes/1/adapters"] = { body: { adapters: [] } };
    routes["/api/v1/nodes/1/capabilities"] = { body: { capabilities: [] } };
    renderPanel(<NodeAdapters nodeId={1} />);
    expect(await screen.findByText(/has not reported any adapters/i)).toBeInTheDocument();
  });
});

describe("NodeHealth", () => {
  beforeEach(() => {
    routes["/api/v1/nodes/1/metrics"] = {
      body: {
        reconnect_count: 3, last_reconcile_duration_ms: 420,
        failed_reconcile_streak: 0, avg_rtt_ms: 18,
      },
    };
    routes["/api/v1/nodes/1/health/history"] = {
      body: {
        count: 2,
        metrics: [
          { timestamp: 1, cpu_percent: 10.5, memory_used_bytes: 500, memory_total_bytes: 1000,
            disk_used_bytes: 1, disk_total_bytes: 4, network_rx_bytes: 0, network_tx_bytes: 0,
            active_connections: 7, latency_ms: 20 },
          { timestamp: 2, cpu_percent: 30.5, memory_used_bytes: 750, memory_total_bytes: 1000,
            disk_used_bytes: 2, disk_total_bytes: 4, network_rx_bytes: 0, network_tx_bytes: 0,
            active_connections: 9, latency_ms: 40 },
        ],
      },
    };
  });

  it("shows the control-plane counters and the latest host sample", async () => {
    renderPanel(<NodeHealth nodeId={1} />);
    await screen.findByText("30.5%");
    // Memory as a percentage of the total the node reported.
    expect(screen.getByText("75.0%")).toBeInTheDocument();
    expect(screen.getByText("9")).toBeInTheDocument();
  });

  // A node that reconnects happily while failing every reconcile looks healthy
  // from every other angle, so the streak is called out when it is non-zero.
  it("calls out a failing reconcile streak", async () => {
    routes["/api/v1/nodes/1/metrics"] = {
      body: {
        reconnect_count: 3, last_reconcile_duration_ms: 420,
        failed_reconcile_streak: 5, avg_rtt_ms: 18,
      },
    };
    renderPanel(<NodeHealth nodeId={1} />);
    const streak = await screen.findByText("5");
    expect(streak.className).toMatch(/text-destructive/);
  });

  // Dividing by a total of zero renders NaN%, which reads as a broken panel
  // rather than as a node that has not reported its memory size.
  it("renders a dash rather than NaN when a total is unknown", async () => {
    routes["/api/v1/nodes/1/health/history"] = {
      body: {
        count: 1,
        metrics: [
          { timestamp: 1, cpu_percent: 5, memory_used_bytes: 0, memory_total_bytes: 0,
            disk_used_bytes: 0, disk_total_bytes: 0, network_rx_bytes: 0, network_tx_bytes: 0,
            active_connections: 0, latency_ms: 1 },
        ],
      },
    };
    renderPanel(<NodeHealth nodeId={1} />);
    await screen.findByText("5.0%");
    expect(screen.queryByText(/NaN/)).toBeNull();
    expect(screen.getAllByText("—").length).toBeGreaterThan(0);
  });

  it("explains a node with no samples instead of drawing an empty chart", async () => {
    routes["/api/v1/nodes/1/health/history"] = { body: { count: 0, metrics: [] } };
    renderPanel(<NodeHealth nodeId={1} />);
    expect(await screen.findByText(/No health samples/i)).toBeInTheDocument();
  });
});
