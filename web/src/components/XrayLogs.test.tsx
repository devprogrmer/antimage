import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { XrayLogs } from "./XrayLogs";
import { setLocale } from "../i18n";

interface Call {
  method: string;
  path: string;
}

let calls: Call[] = [];
let routes: Record<string, { status?: number; body: unknown }> = {};

function stubFetch() {
  vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    calls.push({ method, path });
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

beforeEach(() => {
  setLocale("en");
  calls = [];
  routes = {};
  stubFetch();
});
afterEach(() => vi.unstubAllGlobals());

describe("XrayLogs", () => {
  it("fetches the default line count and shows the raw log text", async () => {
    routes["GET /api/v1/nodes/1/xray-logs?lines=200"] = {
      body: { delivered: true, ok: true, logs: "Aug 31 12:00:00 xray[1]: started\n", error: "", message: "logs fetched" },
    };
    const user = userEvent.setup({ delay: null });
    renderPanel(<XrayLogs nodeId={1} />);

    await user.click(screen.getByRole("button", { name: "Fetch logs" }));

    expect(await screen.findByText(/xray\[1\]: started/)).toBeInTheDocument();
    expect(calls.some((c) => c.method === "GET" && c.path === "/api/v1/nodes/1/xray-logs?lines=200")).toBe(
      true,
    );
  });

  it("sends the selected line count", async () => {
    routes["GET /api/v1/nodes/1/xray-logs?lines=500"] = {
      body: { delivered: true, ok: true, logs: "log text", error: "", message: "logs fetched" },
    };
    const user = userEvent.setup({ delay: null });
    renderPanel(<XrayLogs nodeId={1} />);

    await user.selectOptions(screen.getByLabelText("Lines"), "500");
    await user.click(screen.getByRole("button", { name: "Fetch logs" }));

    expect(await screen.findByText("log text")).toBeInTheDocument();
  });

  it("shows the offline message rather than a fake success when nothing was delivered", async () => {
    routes["GET /api/v1/nodes/1/xray-logs?lines=200"] = {
      body: { delivered: false, ok: false, logs: "", error: "", message: "the node is offline; no logs were fetched" },
    };
    const user = userEvent.setup({ delay: null });
    renderPanel(<XrayLogs nodeId={1} />);

    await user.click(screen.getByRole("button", { name: "Fetch logs" }));

    expect(
      await screen.findByText("the node is offline; no logs were fetched"),
    ).toBeInTheDocument();
  });

  it("shows the agent's error rather than log text when the node has no xray adapter", async () => {
    routes["GET /api/v1/nodes/1/xray-logs?lines=200"] = {
      body: {
        delivered: true,
        ok: false,
        logs: "",
        error: 'this node runs no "xray" adapter',
        message: 'this node runs no "xray" adapter',
      },
    };
    const user = userEvent.setup({ delay: null });
    renderPanel(<XrayLogs nodeId={1} />);

    await user.click(screen.getByRole("button", { name: "Fetch logs" }));

    expect(await screen.findByText(/runs no "xray" adapter/)).toBeInTheDocument();
  });
});
