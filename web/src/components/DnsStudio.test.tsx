import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { DnsStudio } from "./DnsStudio";
import { setLocale } from "../i18n";

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

beforeEach(() => {
  setLocale("en");
  calls = [];
  routes = {};
  stubFetch();
});
afterEach(() => vi.unstubAllGlobals());

describe("DnsStudio", () => {
  it("hides itself with the server's own reason on an unsupported node", async () => {
    routes["GET /api/v1/nodes/1/dns"] = {
      body: { supported: false, reason: "no adapter on this node can apply DNS config (node runs wireguard)" },
    };
    renderPanel(<DnsStudio nodeId={1} />);

    expect(
      await screen.findByText(/no adapter on this node can apply DNS config/),
    ).toBeInTheDocument();
  });

  it("loads the stored config into editable rows", async () => {
    routes["GET /api/v1/nodes/1/dns"] = {
      body: {
        supported: true,
        adapter_kind: "xray",
        servers: [{ address: "1.1.1.1" }, { address: "10.0.0.1", domains: ["corp.internal"], skip_fallback: true }],
        hosts: { "internal.corp": ["10.0.0.5"] },
        fakedns: [{ ip_pool: "198.18.0.0/15", pool_size: 65535 }],
        query_strategy: "UseIPv4",
        disable_cache: true,
      },
    };
    renderPanel(<DnsStudio nodeId={1} />);

    expect(await screen.findByDisplayValue("1.1.1.1")).toBeInTheDocument();
    expect(await screen.findByDisplayValue("10.0.0.1")).toBeInTheDocument();
    expect(await screen.findByDisplayValue("corp.internal")).toBeInTheDocument();
    expect(await screen.findByDisplayValue("internal.corp")).toBeInTheDocument();
    expect(await screen.findByDisplayValue("10.0.0.5")).toBeInTheDocument();
    expect(await screen.findByDisplayValue("198.18.0.0/15")).toBeInTheDocument();
    expect(await screen.findByDisplayValue("65535")).toBeInTheDocument();
  });

  it("saves an added server and posts the exact PUT body", async () => {
    routes["GET /api/v1/nodes/1/dns"] = {
      body: { supported: true, adapter_kind: "xray" },
    };
    routes["PUT /api/v1/nodes/1/dns"] = {
      body: {
        supported: true,
        servers: [{ address: "8.8.8.8" }],
        query_strategy: "",
        disable_cache: false,
      },
    };
    const user = userEvent.setup({ delay: null });
    renderPanel(<DnsStudio nodeId={1} />);

    await user.click(await screen.findByRole("button", { name: "Add server" }));
    await user.type(screen.getByLabelText("Address"), "8.8.8.8");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await vi.waitFor(() => {
      const sent = calls.find((c) => c.method === "PUT" && c.path === "/api/v1/nodes/1/dns");
      expect(sent).toBeTruthy();
      expect(sent!.body).toMatchObject({
        servers: [{ address: "8.8.8.8", domains: [], skip_fallback: false }],
      });
    });
  });

  it("does not send a row whose address was never filled in", async () => {
    routes["GET /api/v1/nodes/1/dns"] = {
      body: { supported: true, adapter_kind: "xray" },
    };
    routes["PUT /api/v1/nodes/1/dns"] = { body: { supported: true } };
    const user = userEvent.setup({ delay: null });
    renderPanel(<DnsStudio nodeId={1} />);

    await user.click(await screen.findByRole("button", { name: "Add server" }));
    await user.click(screen.getByRole("button", { name: "Save" }));

    await vi.waitFor(() => {
      const sent = calls.find((c) => c.method === "PUT" && c.path === "/api/v1/nodes/1/dns");
      expect(sent).toBeTruthy();
      expect(sent!.body).toMatchObject({ servers: [] });
    });
  });

  it("removes a row when its delete button is clicked", async () => {
    routes["GET /api/v1/nodes/1/dns"] = {
      body: { supported: true, adapter_kind: "xray", servers: [{ address: "1.1.1.1" }] },
    };
    const user = userEvent.setup({ delay: null });
    renderPanel(<DnsStudio nodeId={1} />);

    expect(await screen.findByDisplayValue("1.1.1.1")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Delete" }));

    expect(screen.queryByDisplayValue("1.1.1.1")).not.toBeInTheDocument();
  });
});
