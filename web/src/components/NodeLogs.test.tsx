import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { NodeLogs } from "./NodeLogs";
import { setLocale } from "../i18n";

// The route this component reads existed but nothing rendered it, so an
// operator investigating a node during an incident had no browser-side view
// of the panel's own timeline for it. These tests prove the tab now renders
// what the backend returns, and does not fabricate a syslog stream from
// nothing when the backend has nothing to show.

let routes: Record<string, { status?: number; body?: unknown }> = {};

function stubFetch() {
  vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const bare = path.split("?")[0];
    const route = routes[`${method} ${bare}`] ?? routes[bare];
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
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

beforeEach(() => {
  setLocale("en");
  routes = {};
  stubFetch();
});
afterEach(() => vi.unstubAllGlobals());

describe("NodeLogs", () => {
  it("renders each source with the panel's own labels and preserves stderr text", async () => {
    routes["/api/v1/nodes/7/logs"] = {
      body: {
        logs: [
          {
            timestamp: 1_700_000_100,
            level: "error",
            source: "apply",
            message: "apply r5 · reload (reload): systemctl reload xray: exit 1",
          },
          {
            timestamp: 1_700_000_050,
            level: "info",
            source: "audit",
            message: "op · node.disable · ok — planned maintenance",
          },
          {
            timestamp: 1_700_000_000,
            level: "error",
            source: "agent",
            message: "panel-side: adapter refused",
          },
        ],
      },
    };
    renderIt(<NodeLogs nodeId={7} />);

    // Every source's line is rendered verbatim.
    expect(await screen.findByText(/systemctl reload xray/)).toBeInTheDocument();
    expect(screen.getByText(/planned maintenance/)).toBeInTheDocument();
    expect(screen.getByText(/adapter refused/)).toBeInTheDocument();

    // The source badges are present, so an operator can see which of the
    // three sources a line came from without decoding the wording.
    expect(screen.getAllByText(/apply|audit|agent/i).length).toBeGreaterThanOrEqual(3);
  });

  it("says there are no entries rather than pretending to stream syslog", async () => {
    routes["/api/v1/nodes/7/logs"] = { body: { logs: [] } };
    renderIt(<NodeLogs nodeId={7} />);
    // The alternative -- an empty log viewer that looks like a live tail
    // that just hasn't caught anything -- is what would let an operator sit
    // waiting for output that can never arrive.
    expect(await screen.findByText(/No recent log entries/i)).toBeInTheDocument();
  });
});
