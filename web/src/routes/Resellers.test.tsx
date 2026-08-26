import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { can } from "../lib/session";
import type { Session } from "../lib/session";
import { Resellers } from "./Resellers";
import { ResellerDetail } from "./ResellerDetail";
import { MyTenancy } from "../components/MyTenancy";
import { setLocale } from "../i18n";

// Routes are recorded in the order they were requested so a test can assert
// what the UI actually sent, not merely what it rendered.
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
    const status = route.status ?? 200;
    return new Response(JSON.stringify(route.body), { status });
  });
}

function renderWithQuery(node: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  // The name cell is a Link now, which needs router context. Without one the
  // screen throws rather than rendering a smaller version of itself.
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>{node}</MemoryRouter>
    </QueryClientProvider>,
  );
}

const reseller = {
  id: 7,
  admin_id: 3,
  display_name: "vendor-vpn",
  enabled: true,
  max_subjects: null,
  max_quota_bytes: null,
  credit_floor: 0,
  created_at: 1_700_000_000,
  updated_at: 1_700_000_000,
};

function session(permissions: string[], isSuper = false): Session {
  return { admin_id: 1, role: "admin", is_super: isSuper, permissions };
}

beforeEach(() => {
  setLocale("en");
  calls = [];
  routes = {};
  stubFetch();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("permission gating", () => {
  // THE guard the whole client-side gate rests on. rbac.Check gives a super
  // admin a bypass on SCOPE and none on permission -- "a custom role stripped
  // of a permission is honoured even for supers" -- so a client that treated
  // is_super as a blanket yes would offer controls the server then refuses.
  it("does not treat is_super as a blanket permission", () => {
    expect(can(session([], true), "reseller:write")).toBe(false);
    expect(can(session(["reseller:read"], true), "reseller:write")).toBe(false);
    expect(can(session(["reseller:write"], true), "reseller:write")).toBe(true);
  });

  it("answers false for an unresolved session rather than assuming a grant", () => {
    expect(can(undefined, "reseller:read")).toBe(false);
  });

  // The separation that matters: credit:grant is not implied by reseller:write,
  // because minting credit is the only operation that creates value from
  // nothing. An admin holds the second and not the first.
  it("hides the credit form from an actor holding only reseller:write", async () => {
    routes["/api/v1/auth/me"] = { body: session(["reseller:read", "reseller:write"]) };
    routes["/api/v1/resellers/7"] = { body: reseller };
    routes["/api/v1/resellers/7/balance"] = { body: { reseller_id: 7, balance: 0 } };
    routes["/api/v1/resellers/7/ledger"] = { body: { movements: [] } };

    renderWithQuery(<ResellerDetail resellerID={7} />);

    // The settings form proves the tree rendered, so the absence below is a
    // real absence and not an unresolved query.
    await screen.findByText("Settings");
    expect(screen.queryByText("Grant credit")).toBeNull();
  });

  it("shows the credit form to an actor holding credit:grant", async () => {
    routes["/api/v1/auth/me"] = { body: session(["reseller:read", "credit:grant"]) };
    routes["/api/v1/resellers/7"] = { body: reseller };
    routes["/api/v1/resellers/7/balance"] = { body: { reseller_id: 7, balance: 0 } };
    routes["/api/v1/resellers/7/ledger"] = { body: { movements: [] } };

    renderWithQuery(<ResellerDetail resellerID={7} />);
    await screen.findByText("Grant credit");
  });

  it("hides the create button from a reader", async () => {
    routes["/api/v1/auth/me"] = { body: session(["reseller:read"]) };
    routes["/api/v1/resellers"] = { body: { resellers: [reseller] } };

    renderWithQuery(<Resellers onSelect={() => {}} />);
    await screen.findByText("vendor-vpn");
    expect(screen.queryByRole("button", { name: "New tenant" })).toBeNull();
  });
});

describe("limits", () => {
  // null is unlimited and 0 is "may create nothing". Collapsing them would
  // silently turn an unlimited tenant into a frozen one, which is why the
  // column is nullable in the first place.
  it("distinguishes an unlimited ceiling from a ceiling of zero", async () => {
    routes["/api/v1/auth/me"] = { body: session(["reseller:read"]) };
    routes["/api/v1/resellers"] = {
      body: {
        resellers: [
          { ...reseller, id: 1, display_name: "unlimited-tenant", max_subjects: null },
          { ...reseller, id: 2, display_name: "frozen-tenant", max_subjects: 0 },
        ],
      },
    };

    renderWithQuery(<Resellers onSelect={() => {}} />);
    const unlimited = (await screen.findByText("unlimited-tenant")).closest("tr");
    const frozen = screen.getByText("frozen-tenant").closest("tr");

    expect(unlimited?.textContent).toContain("Unlimited");
    expect(frozen?.textContent).not.toContain("Unlimited");
    expect(frozen?.textContent).toContain("0");
  });

  // The API distinguishes absent (leave alone) from null (set to unlimited)
  // precisely so unlimited stays reachable once a limit has been set. A form
  // that omitted a cleared field could never clear one, so the cleared limit
  // must go out as an explicit null rather than as a missing key or a zero.
  it("sends an explicit null when a ceiling is cleared to unlimited", async () => {
    routes["/api/v1/auth/me"] = { body: session(["reseller:read", "reseller:write"]) };
    routes["/api/v1/resellers/7"] = { body: { ...reseller, max_subjects: 10 } };
    routes["/api/v1/resellers/7/balance"] = { body: { reseller_id: 7, balance: 0 } };
    routes["/api/v1/resellers/7/ledger"] = { body: { movements: [] } };
    routes["PUT /api/v1/resellers/7"] = { status: 204, body: null };

    renderWithQuery(<ResellerDetail resellerID={7} />);
    await screen.findByText("Settings");

    const user = userEvent.setup();
    const unlimitedBoxes = screen.getAllByRole("checkbox", { name: "Unlimited" });
    await user.click(unlimitedBoxes[0]);
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      const put = calls.find((c) => c.method === "PUT");
      expect(put).toBeDefined();
      const body = put?.body as Record<string, unknown>;
      expect(body).toHaveProperty("max_subjects");
      expect(body.max_subjects).toBeNull();
    });
  });
});

describe("idempotency", () => {
  // A retry of the SAME attempt must reuse the key: rotating it on failure
  // would turn the retry into a second grant, which is the exact case the key
  // exists to prevent. A timeout is the common way to reach this -- the grant
  // landed, the response did not.
  it("reuses the idempotency key when a grant fails", async () => {
    routes["/api/v1/auth/me"] = { body: session(["reseller:read", "credit:grant"]) };
    routes["/api/v1/resellers/7"] = { body: reseller };
    routes["/api/v1/resellers/7/balance"] = { body: { reseller_id: 7, balance: 0 } };
    routes["/api/v1/resellers/7/ledger"] = { body: { movements: [] } };
    routes["POST /api/v1/resellers/7/credit"] = {
      status: 500,
      body: { error: { code: "internal", message: "request failed" } },
    };

    renderWithQuery(<ResellerDetail resellerID={7} />);
    await screen.findByText("Grant credit");

    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Amount"), "500");
    await user.click(screen.getByRole("button", { name: "Grant" }));
    await screen.findByRole("alert");
    await user.click(screen.getByRole("button", { name: "Grant" }));

    await waitFor(() => {
      const grants = calls.filter((c) => c.path === "/api/v1/resellers/7/credit");
      expect(grants.length).toBe(2);
      const first = grants[0].body as { idempotency_key: string };
      const second = grants[1].body as { idempotency_key: string };
      expect(second.idempotency_key).toBe(first.idempotency_key);
    });
  });

  it("always sends an idempotency key when provisioning", async () => {
    routes["/api/v1/auth/me"] = { body: session(["reseller:read", "subject:write"]) };
    routes["/api/v1/resellers/7"] = { body: reseller };
    routes["/api/v1/resellers/7/balance"] = { body: { reseller_id: 7, balance: 0 } };
    routes["/api/v1/resellers/7/ledger"] = { body: { movements: [] } };
    routes["POST /api/v1/resellers/7/subjects"] = {
      status: 201,
      body: { subject_id: 1, ledger_id: 1, balance: 0 },
    };

    renderWithQuery(<ResellerDetail resellerID={7} />);
    await screen.findByText("Provision a customer");

    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Name"), "customer");
    await user.click(screen.getByRole("button", { name: "Provision" }));

    await waitFor(() => {
      const post = calls.find((c) => c.path === "/api/v1/resellers/7/subjects");
      const body = post?.body as { idempotency_key?: string };
      // Without one, a retry both double-charges and duplicates the customer,
      // which is why the server refuses a request that omits it.
      expect(body?.idempotency_key).toBeTruthy();
    });
  });
});

describe("server refusals", () => {
  // The engine's refusals name the ceiling and how much of it is used, which is
  // more use to an operator than a generic failure. Mapping them onto a local
  // string would throw that away.
  it("shows the server's refusal verbatim", async () => {
    routes["/api/v1/auth/me"] = { body: session(["reseller:read", "subject:write"]) };
    routes["/api/v1/resellers/7"] = { body: reseller };
    routes["/api/v1/resellers/7/balance"] = { body: { reseller_id: 7, balance: 0 } };
    routes["/api/v1/resellers/7/ledger"] = { body: { movements: [] } };
    routes["POST /api/v1/resellers/7/subjects"] = {
      status: 422,
      body: {
        error: { code: "refused", message: "quota ceiling exceeded: 900 of 1000 allocated" },
      },
    };

    renderWithQuery(<ResellerDetail resellerID={7} />);
    await screen.findByText("Provision a customer");

    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Name"), "customer");
    await user.click(screen.getByRole("button", { name: "Provision" }));

    expect((await screen.findByRole("alert")).textContent).toContain("900 of 1000");
  });
});

describe("tenant self-service", () => {
  // An admin who operates no tenancy gets 404 rather than an empty record, so
  // "no tenancy" -- the normal state for most accounts -- renders as nothing at
  // all rather than as an error on the Profile page.
  it("renders nothing when the account is not a tenant", async () => {
    routes["/api/v1/me/reseller"] = {
      status: 404,
      body: { error: { code: "not_found", message: "reseller not found" } },
    };

    const { container } = renderWithQuery(<MyTenancy />);
    await waitFor(() => {
      expect(calls.some((c) => c.path === "/api/v1/me/reseller")).toBe(true);
    });
    await waitFor(() => expect(container.textContent).toBe(""));
  });

  it("shows a tenant their own balance and floor", async () => {
    routes["/api/v1/me/reseller"] = {
      body: {
        reseller_id: 7,
        display_name: "vendor-vpn",
        enabled: true,
        balance: 750,
        credit_floor: 0,
      },
    };

    renderWithQuery(<MyTenancy />);
    await screen.findByText("vendor-vpn");
    expect(screen.getByText("750")).toBeInTheDocument();
  });

  // The ledger tab fetches only when opened: most visits to Profile are not
  // about billing, and a tenant's history is the more expensive of the two.
  it("does not fetch the ledger until the tab is opened", async () => {
    routes["/api/v1/me/reseller"] = {
      body: {
        reseller_id: 7,
        display_name: "vendor-vpn",
        enabled: true,
        balance: 750,
        credit_floor: 0,
      },
    };
    routes["/api/v1/me/reseller/ledger"] = {
      body: { movements: [{ id: 1, delta: 750, reason: "topup", subject_id: null, note: "", at: 1_700_000_000 }] },
    };

    renderWithQuery(<MyTenancy />);
    await screen.findByText("vendor-vpn");
    expect(calls.some((c) => c.path === "/api/v1/me/reseller/ledger")).toBe(false);

    await userEvent.setup().click(screen.getByRole("tab", { name: "Ledger" }));
    await screen.findByText("topup");
    expect(calls.some((c) => c.path === "/api/v1/me/reseller/ledger")).toBe(true);
  });

  // The self-service route carries no reseller id -- the tenancy is resolved
  // from the session. A UI that appended one would be asking a question the
  // route cannot answer, and would suggest the id is the caller's to choose.
  it("requests the ledger without naming a reseller", async () => {
    routes["/api/v1/me/reseller"] = {
      body: {
        reseller_id: 7,
        display_name: "vendor-vpn",
        enabled: true,
        balance: 0,
        credit_floor: 0,
      },
    };
    routes["/api/v1/me/reseller/ledger"] = { body: { movements: [] } };

    renderWithQuery(<MyTenancy />);
    await screen.findByText("vendor-vpn");
    await userEvent.setup().click(screen.getByRole("tab", { name: "Ledger" }));

    await waitFor(() => {
      const request = calls.find((c) => c.path.includes("/me/reseller/ledger"));
      expect(request).toBeDefined();
      expect(request?.path).toBe("/api/v1/me/reseller/ledger");
      expect(request?.path).not.toContain("7");
    });
  });

  // A tenant at their floor cannot provision, and the reason is not obvious
  // from a number alone -- the balance is not zero, it is merely not above the
  // floor -- so it is stated.
  it("warns a tenant who is at their credit floor", async () => {
    routes["/api/v1/me/reseller"] = {
      body: {
        reseller_id: 7,
        display_name: "vendor-vpn",
        enabled: true,
        balance: -500,
        credit_floor: -500,
      },
    };

    renderWithQuery(<MyTenancy />);
    expect((await screen.findByRole("status")).textContent).toContain("credit floor");
  });
});
