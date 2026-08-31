import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { NodeAdapters } from "./NodeAdapters";
import { setLocale } from "../i18n";

// GET /nodes/{id}/adapters and GET /nodes/{id}/capabilities existed with no
// client at all before this component; POST /nodes/{id}/geo-update existed
// with no client until the geo-update work. These tests cover both: the
// read side rendering what the node actually reported, and the button
// reaching the real command-delivery route and showing its honest result.

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
  routes = {
    "/api/v1/nodes/1/capabilities": { body: { capabilities: [] } },
    "/api/v1/xray-core-versions": { body: { versions: [] } },
  };
  stubFetch();
});
afterEach(() => vi.unstubAllGlobals());

describe("NodeAdapters", () => {
  it("shows an adapter's last geo update only when it has one", async () => {
    routes["/api/v1/nodes/1/adapters"] = {
      body: {
        adapters: [
          {
            kind: "xray", version: "1.8.0", capabilities: ["tls"], reported_at: 1700000000,
            geo_updated_at: 1700005000, geoip_sha256: "abcdef0123456789", geosite_sha256: "0123456789abcdef",
            core_upgraded_at: null,
          },
          {
            kind: "wireguard", version: "1.0.0", capabilities: [], reported_at: 1700000000,
            geo_updated_at: null, core_upgraded_at: null,
          },
        ],
      },
    };
    renderPanel(<NodeAdapters nodeId={1} />);

    await screen.findByText("xray");
    // xray shows its stamp, truncated, not the raw 64-char hex.
    expect(await screen.findByText(/geoip abcdef012345/)).toBeInTheDocument();
    // wireguard never had one, and must not show a "never" row -- silence
    // is the correct rendering for a protocol with no geo concept.
    const wireguardRow = screen.getByText("wireguard").closest("div")!;
    expect(wireguardRow.textContent).not.toContain("Geo data updated");
  });

  it("posts to the real geo-update route and shows per-adapter outcomes", async () => {
    routes["/api/v1/nodes/1/adapters"] = {
      body: {
        adapters: [
          {
            kind: "xray", version: "1.8.0", capabilities: [], reported_at: 1700000000,
            geo_updated_at: null, core_upgraded_at: null,
          },
        ],
      },
    };
    routes["POST /api/v1/nodes/1/geo-update"] = {
      body: {
        delivered: true,
        outcomes: [{ kind: "xray", ok: true, error: "", geoip_sha256: "aaa", geosite_sha256: "bbb" }],
        message: "geo data update delivered",
      },
    };
    const user = userEvent.setup({ delay: null });
    renderPanel(<NodeAdapters nodeId={1} />);

    await user.click(await screen.findByRole("button", { name: "Update geo data" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "POST" && c.path === "/api/v1/nodes/1/geo-update");
      expect(sent).toBeTruthy();
    });
    expect(await screen.findByText(/xray: updated/)).toBeInTheDocument();
  });

  it("shows the offline message rather than a fake success when nothing was delivered", async () => {
    routes["/api/v1/nodes/1/adapters"] = { body: { adapters: [] } };
    routes["POST /api/v1/nodes/1/geo-update"] = {
      body: { delivered: false, outcomes: null, message: "the node is offline; nothing was updated" },
    };
    const user = userEvent.setup({ delay: null });
    renderPanel(<NodeAdapters nodeId={1} />);

    await user.click(await screen.findByRole("button", { name: "Update geo data" }));

    expect(await screen.findByText("the node is offline; nothing was updated")).toBeInTheDocument();
  });
});

describe("NodeAdapters core version upgrade", () => {
  it("lists real versions and posts the chosen one's exact URL and checksum", async () => {
    routes["/api/v1/nodes/1/adapters"] = {
      body: {
        adapters: [
          {
            kind: "xray", version: "1.8.0", capabilities: [], reported_at: 1700000000,
            geo_updated_at: null, core_upgraded_at: null,
          },
        ],
      },
    };
    routes["/api/v1/xray-core-versions"] = {
      body: {
        versions: [
          { version: "1.9.0", binary_url: "https://example.com/Xray-linux-64.zip", binary_sha256: "aaa111" },
        ],
      },
    };
    routes["POST /api/v1/nodes/1/core-upgrade"] = {
      body: { delivered: true, ok: true, installed_version: "1.9.0", rolled_back: false, error: "", message: "core upgraded" },
    };
    const user = userEvent.setup({ delay: null });
    renderPanel(<NodeAdapters nodeId={1} />);

    // The select renders disabled before the version list loads; wait for
    // the actual option, not just the element, to avoid racing react-query.
    await screen.findByRole("option", { name: "1.9.0" });
    await user.selectOptions(screen.getByLabelText("Core version"), "1.9.0");
    await user.click(screen.getByRole("button", { name: "Upgrade core" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "POST" && c.path === "/api/v1/nodes/1/core-upgrade");
      expect(sent).toBeTruthy();
      expect(sent!.body).toEqual({
        kind: "xray",
        binary_url: "https://example.com/Xray-linux-64.zip",
        binary_sha256: "aaa111",
        expected_version: "1.9.0",
      });
    });
    expect(await screen.findByText(/Upgraded: 1.9.0/)).toBeInTheDocument();
  });

  it("shows a rolled-back upgrade as a warning, not as a bare failure or a success", async () => {
    routes["/api/v1/nodes/1/adapters"] = {
      body: {
        adapters: [
          {
            kind: "xray", version: "1.8.0", capabilities: [], reported_at: 1700000000,
            geo_updated_at: null, core_upgraded_at: null,
          },
        ],
      },
    };
    routes["/api/v1/xray-core-versions"] = {
      body: { versions: [{ version: "1.9.0", binary_url: "https://x", binary_sha256: "aaa" }] },
    };
    routes["POST /api/v1/nodes/1/core-upgrade"] = {
      body: {
        delivered: true, ok: false, installed_version: "1.8.0", rolled_back: true,
        error: "the new binary did not become healthy",
        message: "the upgrade failed and was rolled back to the previous version: the new binary did not become healthy",
      },
    };
    const user = userEvent.setup({ delay: null });
    renderPanel(<NodeAdapters nodeId={1} />);

    await screen.findByRole("option", { name: "1.9.0" });
    await user.selectOptions(screen.getByLabelText("Core version"), "1.9.0");
    await user.click(screen.getByRole("button", { name: "Upgrade core" }));

    expect(await screen.findByText(/rolled back to the previous version/)).toBeInTheDocument();
  });

  it("does not offer the version picker for a non-xray adapter", async () => {
    routes["/api/v1/nodes/1/adapters"] = {
      body: {
        adapters: [
          {
            kind: "wireguard", version: "1.0.0", capabilities: [], reported_at: 1700000000,
            geo_updated_at: null, core_upgraded_at: null,
          },
        ],
      },
    };
    renderPanel(<NodeAdapters nodeId={1} />);

    await screen.findByText("wireguard");
    expect(screen.queryByRole("button", { name: "Upgrade core" })).toBeNull();
    // And the version-list endpoint must not even be called for a node
    // with no xray adapter -- there is no reason to touch GitHub for it.
    expect(calls.some((c) => c.path === "/api/v1/xray-core-versions")).toBe(false);
  });
});
