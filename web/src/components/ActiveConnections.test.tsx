import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ActiveConnections } from "./ActiveConnections";
import { setLocale } from "../i18n";

let routes: Record<string, { status?: number; body?: unknown }> = {};

function stubFetch() {
  vi.stubGlobal("fetch", async (path: string) => {
    const bare = path.split("?")[0];
    const route = routes[bare];
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

describe("ActiveConnections", () => {
  // The backend returns a bare array, not `{connections: [...]}`. This test
  // guards against the same shape regression that had the devices table
  // crashing on first fetch.
  it("renders the rows returned as a bare array", async () => {
    routes["/api/v1/subjects/5/connections"] = {
      body: [
        {
          subject_id: 5,
          node_id: 3,
          connection_id: "c1",
          source_ip: "203.0.113.9",
          connected_at: 1_700_000_000,
          last_seen_at: 1_700_000_100,
          protocol_info: "vless-reality",
        },
      ],
    };
    renderIt(<ActiveConnections subjectId={5} />);

    expect(await screen.findByText("203.0.113.9")).toBeInTheDocument();
    expect(screen.getByText("vless-reality")).toBeInTheDocument();
  });

  it("says nobody is connected rather than a blank card", async () => {
    routes["/api/v1/subjects/5/connections"] = { body: [] };
    renderIt(<ActiveConnections subjectId={5} />);
    // Silence here reads as "still loading". Saying so tells an operator
    // that the customer whose service they are investigating is not, in
    // fact, connected right now.
    expect(await screen.findByText(/No open transports/i)).toBeInTheDocument();
  });
});
