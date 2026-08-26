import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { Audit } from "./Audit";
import { SessionList } from "../components/SessionList";
import { setLocale } from "../i18n";

let calls: Array<{ method: string; path: string }> = [];
let routes: Record<string, { status?: number; body: unknown }> = {};

function stubFetch() {
  vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    calls.push({ method, path });
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

function renderScreen(node: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

const changed = {
  id: 1, at: 1000, actor_type: "admin", actor_name: "root", actor_label: "",
  actor_ip: "10.0.0.1", request_id: "req-abc", action: "subject.update",
  target_type: "subject", target_id: 5, result: "ok",
  before: { quota_bytes: 100 }, after: { quota_bytes: 200 },
};

const unchanged = {
  id: 2, at: 1001, actor_type: "admin", actor_name: "root", actor_label: "",
  actor_ip: "10.0.0.1", request_id: "req-def", action: "auth.login",
  target_type: "", target_id: 0, result: "ok",
};

beforeEach(() => {
  setLocale("en");
  calls = [];
  routes = { "/api/v1/audit": { body: { entries: [changed, unchanged] } } };
  stubFetch();
});
afterEach(() => vi.unstubAllGlobals());

describe("Audit", () => {
  // The table has carried before_json and after_json since SP1 and the query
  // never selected them, so the log could say an action happened and not what
  // it did.
  it("shows what an action changed", async () => {
    const user = userEvent.setup();
    renderScreen(<Audit />);

    await user.click(await screen.findByText("subject.update"));
    expect(await screen.findByText(/"quota_bytes": 100/)).toBeInTheDocument();
    expect(screen.getByText(/"quota_bytes": 200/)).toBeInTheDocument();
  });

  // A disclosure triangle that opens onto nothing teaches an operator to stop
  // clicking it.
  it("does not offer to expand an entry with nothing recorded", async () => {
    renderScreen(<Audit />);
    await screen.findByText("auth.login");

    const rows = screen.getAllByText(/auth.login|subject.update/);
    const loginRow = rows.find((r) => r.textContent === "auth.login")!;
    expect(loginRow.closest("details")).toBeNull();
    const updateRow = rows.find((r) => r.textContent === "subject.update")!;
    expect(updateRow.closest("details")).not.toBeNull();
  });

  it("searches on submit rather than on every keystroke", async () => {
    const user = userEvent.setup();
    renderScreen(<Audit />);
    await screen.findByText("subject.update");
    const before = calls.length;

    await user.type(screen.getByLabelText("Action"), "node.delete");
    // The audit log is the largest table in the panel; refetching per
    // character would be a query storm.
    expect(calls.length).toBe(before);

    await user.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() =>
      expect(calls.some((c) => c.path.includes("action=node.delete"))).toBe(true),
    );
  });

  // The reason WriteError returns an id at all: it is quoted off a failure
  // screen and resolves to the row.
  it("searches by the request id from a failure screen", async () => {
    const user = userEvent.setup();
    renderScreen(<Audit />);
    await screen.findByText("subject.update");

    await user.type(screen.getByLabelText("Reference"), "req-abc");
    await user.click(screen.getByRole("button", { name: "Search" }));

    await waitFor(() =>
      expect(calls.some((c) => c.path.includes("request_id=req-abc"))).toBe(true),
    );
  });
});

describe("SessionList", () => {
  const mine = {
    id: 1, ip: "10.0.0.1", user_agent: "Firefox",
    created_at: 1, last_used_at: 2, expires_at: 3, current: true,
  };
  const other = {
    id: 2, ip: "203.0.113.9", user_agent: "Chrome",
    created_at: 1, last_used_at: 2, expires_at: 3, current: false,
  };

  beforeEach(() => {
    routes["/api/v1/sessions"] = { body: { sessions: [mine, other] } };
  });

  it("marks the session the operator is using", async () => {
    renderScreen(<SessionList />);
    expect(await screen.findByText("This device")).toBeInTheDocument();
  });

  // Revoking the current session is signing out, and there is a button for
  // that which also clears the cached session. Doing it here would leave the
  // UI holding a dead cookie.
  it("does not offer to revoke the current session", async () => {
    renderScreen(<SessionList />);
    await screen.findByText("This device");
    expect(screen.getAllByRole("button", { name: "Revoke" })).toHaveLength(1);
  });

  it("asks before revoking, and names the session", async () => {
    routes["DELETE /api/v1/sessions/2"] = { status: 204, body: {} };
    const user = userEvent.setup();
    renderScreen(<SessionList />);

    await user.click(await screen.findByRole("button", { name: "Revoke" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("203.0.113.9");
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);

    await user.click(within(dialog).getByRole("button", { name: "Revoke" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "DELETE" && c.path === "/api/v1/sessions/2")).toBe(true),
    );
  });
});
