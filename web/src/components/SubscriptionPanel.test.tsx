import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SubscriptionPanel } from "./SubscriptionPanel";
import { setLocale } from "../i18n";

// The panel exists because "generate a subscription" is not one operation. A
// VLESS inbound produces a link, a WireGuard inbound produces a file with no
// link form, and an L2TP inbound produces neither. The engine used to collapse
// all three into a V2Ray-style subscription, which handed a WireGuard tunnel
// out as a vless:// URI no client could use.

interface Call {
  method: string;
  path: string;
  body: unknown;
}

let calls: Call[] = [];
let routes: Record<string, { status?: number; body?: unknown; blob?: boolean }> = {};
let downloaded: string[] = [];

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
    if (route.blob) {
      return new Response(new Blob(["png"], { type: "image/png" }), {
        status: route.status ?? 200,
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

const vlessConfig = {
  service_id: 1, node_id: 1, node_name: "fra-1", adapter_kind: "xray",
  protocol: "vless", delivery: "uri",
  uri: "vless://uuid-1@203.0.113.10:443?type=ws&security=tls#fra-1",
};

const wireguardConfig = {
  service_id: 2, node_id: 1, node_name: "fra-1", adapter_kind: "wireguard",
  protocol: "wireguard", delivery: "file",
  file_name: "fra-1-wg.conf",
  file_body: "[Interface]\nPrivateKey = <your client private key>\n\n[Peer]\nPublicKey = SRV=\n",
};

const l2tpConfig = {
  service_id: 3, node_id: 2, node_name: "ams-1", adapter_kind: "l2tp",
  protocol: "l2tp", delivery: "manual",
  manual: { server: "198.51.100.7", port: 1701, username: "alice", password: "pw", psk: "psk-1" },
  note: "notInAggregatedFormats",
};

function seed(over: Record<string, unknown> = {}) {
  routes["/api/v1/subjects/5/configs"] = {
    body: {
      subject_id: 5, name: "alice",
      subscription_url: "/subscribe/tok-abc",
      status: "active", expires_at: null,
      quota_bytes: null, quota_used_bytes: 0,
      configs: [vlessConfig, wireguardConfig, l2tpConfig],
      skipped: [],
      ...over,
    },
  };
}

beforeEach(() => {
  setLocale("en");
  calls = [];
  routes = {};
  downloaded = [];
  stubFetch();

  vi.stubGlobal("URL", {
    ...URL,
    createObjectURL: () => "blob:stub",
    revokeObjectURL: () => {},
  });
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
    this: HTMLAnchorElement,
  ) {
    downloaded.push(this.download);
  });
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

/** The card for one protocol: the element holding its label and its actions. */
function cardFor(protocol: string): HTMLElement {
  let el: HTMLElement | null = screen.getByText(protocol);
  while (el && !el.className.includes("rounded border border-border bg-background")) {
    el = el.parentElement;
  }
  if (!el) throw new Error(`no card found for ${protocol}`);
  return el;
}

describe("per-protocol delivery", () => {
  // THE thing this exists to prevent: a WireGuard tunnel handed out as a
  // proxy link that no client can import.
  it("never renders a proxy link for a protocol that has none", async () => {
    seed();
    renderPanel(<SubscriptionPanel subjectId={5} />);
    await screen.findByText("vless");

    const body = document.body.textContent ?? "";
    // The vless inbound's link is legitimately present exactly once.
    expect(body.match(/vless:\/\//g)?.length).toBe(1);
    // And nothing wearing a wireguard or l2tp label carries one.
    for (const proto of ["wireguard", "l2tp"]) {
      const card = screen.getByText(proto).closest("div")!.parentElement!;
      expect(card.textContent).not.toMatch(/vless:\/\/|vmess:\/\/|trojan:\/\/|ss:\/\//);
    }
  });

  it("shows a link protocol as a copyable link", async () => {
    seed();
    const user = userEvent.setup({ delay: null });
    renderPanel(<SubscriptionPanel subjectId={5} />);
    await screen.findByText("vless");

    expect(screen.getByText(vlessConfig.uri)).toBeInTheDocument();

    const card = cardFor("vless");
    await user.click(within(card).getByRole("button", { name: "Copy" }));
    // Read back through the clipboard API rather than a local array:
    // userEvent.setup() installs its own navigator.clipboard, so a stub
    // defined in beforeEach is replaced before the click ever happens.
    await waitFor(async () =>
      expect(await navigator.clipboard.readText()).toBe(vlessConfig.uri),
    );
  });

  it("shows a file protocol as a downloadable file, not a link", async () => {
    seed();
    const user = userEvent.setup({ delay: null });
    renderPanel(<SubscriptionPanel subjectId={5} />);
    await screen.findByText("wireguard");

    // The actual conf body is on screen.
    expect(screen.getByText(/\[Interface\]/)).toBeInTheDocument();

    const card = cardFor("wireguard");
    await user.click(within(card).getByRole("button", { name: "Download" }));
    await waitFor(() => expect(downloaded).toContain("fra-1-wg.conf"));
  });

  // A WireGuard profile is far past what a QR code can hold, and one that
  // scans as garbage is worse than no button at all.
  it("offers no QR for a file protocol, and says why", async () => {
    seed();
    renderPanel(<SubscriptionPanel subjectId={5} />);
    await screen.findByText("wireguard");

    const card = cardFor("wireguard");
    expect(within(card).queryByRole("button", { name: "Show QR" })).toBeNull();
    expect(card.textContent).toMatch(/Too long for a QR code/);
  });

  it("shows a manual protocol as the fields the user must type", async () => {
    seed();
    renderPanel(<SubscriptionPanel subjectId={5} />);
    await screen.findByText("l2tp");

    expect(screen.getByText("198.51.100.7")).toBeInTheDocument();
    expect(screen.getByText("1701")).toBeInTheDocument();
    // The IPsec pre-shared key: without it the phase-1 handshake cannot
    // complete, and the user has no way to guess it.
    expect(screen.getByText("psk-1")).toBeInTheDocument();
  });

  // The operator has to be able to explain the gap to a customer rather than
  // discover it from a support ticket.
  it("says which protocols the subscription link cannot carry", async () => {
    seed();
    renderPanel(<SubscriptionPanel subjectId={5} />);
    await screen.findByText("l2tp");

    expect(
      screen.getByText(/cannot appear in a V2Ray, Clash or sing-box subscription/),
    ).toBeInTheDocument();
  });
});

describe("QR", () => {
  it("encodes the actual URI through the panel, not a placeholder", async () => {
    seed();
    routes["POST /api/v1/qr"] = { blob: true };
    const user = userEvent.setup({ delay: null });
    renderPanel(<SubscriptionPanel subjectId={5} />);
    await screen.findByText("vless");

    const card = cardFor("vless");
    await user.click(within(card).getByRole("button", { name: "Show QR" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.path === "/api/v1/qr");
      expect(sent?.body).toEqual({ text: vlessConfig.uri });
    });
    expect(await within(card).findByRole("img")).toBeInTheDocument();
  });

  it("surfaces a refusal rather than a broken image", async () => {
    seed();
    routes["POST /api/v1/qr"] = {
      status: 422,
      body: { error: { code: "too_long", message: "this configuration is too long" } },
    };
    const user = userEvent.setup({ delay: null });
    renderPanel(<SubscriptionPanel subjectId={5} />);
    await screen.findByText("vless");

    const card = cardFor("vless");
    await user.click(within(card).getByRole("button", { name: "Show QR" }));

    expect(await screen.findByText(/too long/)).toBeInTheDocument();
    expect(within(card).queryByRole("img")).toBeNull();
  });
});

describe("the subscription link", () => {
  it("issues one when the subject has none", async () => {
    seed({ subscription_url: undefined });
    routes["POST /api/v1/subjects/5/subscription"] = {
      body: { subscription_url: "/subscribe/new", regenerated: false },
    };
    const user = userEvent.setup({ delay: null });
    renderPanel(<SubscriptionPanel subjectId={5} />);
    await screen.findByText(/No subscription link has been issued/);

    await user.click(screen.getByRole("button", { name: "Issue link" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && c.path === "/api/v1/subjects/5/subscription"))
        .toBe(true),
    );
  });

  it("regenerates an existing one", async () => {
    seed();
    routes["POST /api/v1/subjects/5/subscription"] = {
      body: { subscription_url: "/subscribe/rotated", regenerated: true },
    };
    const user = userEvent.setup({ delay: null });
    renderPanel(<SubscriptionPanel subjectId={5} />);
    await screen.findByText("vless");

    await user.click(screen.getByRole("button", { name: "Regenerate" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && c.path === "/api/v1/subjects/5/subscription"))
        .toBe(true),
    );
  });

  // Revoking withdraws access. It asks first and says what happens.
  it("asks before revoking", async () => {
    seed();
    routes["DELETE /api/v1/subjects/5/subscription"] = { status: 204, body: {} };
    const user = userEvent.setup({ delay: null });
    renderPanel(<SubscriptionPanel subjectId={5} />);
    await screen.findByText("vless");

    await user.click(screen.getByRole("button", { name: "Revoke" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(/stops working immediately/);
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);

    await user.click(within(dialog).getByRole("button", { name: "Revoke" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "DELETE")).toBe(true),
    );
  });

  it("makes the link absolute so it can be pasted", async () => {
    seed();
    renderPanel(<SubscriptionPanel subjectId={5} />);
    await screen.findByText("vless");

    expect(
      screen.getByText(`${window.location.origin}/api/v1/subscribe/tok-abc`),
    ).toBeInTheDocument();
  });
});

describe("what the operator needs to know before handing it over", () => {
  // A link for a frozen or expired subject is a support ticket.
  it("warns when the subject will not connect", async () => {
    seed({ status: "frozen" });
    renderPanel(<SubscriptionPanel subjectId={5} />);
    const statuses = await screen.findAllByRole("status");
    expect(statuses.map((n) => n.textContent).join(" ")).toMatch(
      /frozen; the configurations below will not connect/,
    );
  });

  it("does not warn for an active subject", async () => {
    seed();
    renderPanel(<SubscriptionPanel subjectId={5} />);
    await screen.findByText("vless");
    expect(screen.queryByText(/will not connect/)).toBeNull();
  });

  // A disabled inbound is why a customer's config stopped working. Dropping it
  // silently is how that becomes unexplainable.
  it("names the inbounds that produced nothing, with the reason", async () => {
    seed({
      configs: [vlessConfig],
      skipped: [
        { service_id: 9, node_name: "ams-1", adapter_kind: "openvpn", reason: "inboundDisabled" },
      ],
    });
    renderPanel(<SubscriptionPanel subjectId={5} />);
    await screen.findByText("Not included");

    expect(screen.getByText(/the inbound is disabled/)).toBeInTheDocument();
    expect(screen.getByText("openvpn")).toBeInTheDocument();
  });

  it("shows expiry and quota alongside the link", async () => {
    seed({ expires_at: 1700000000, quota_bytes: 1000, quota_used_bytes: 250 });
    renderPanel(<SubscriptionPanel subjectId={5} />);
    await screen.findByText("vless");

    expect(screen.getByText("250 / 1,000")).toBeInTheDocument();
  });
});
