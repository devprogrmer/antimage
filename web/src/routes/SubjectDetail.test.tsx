import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SubjectDetail } from "./SubjectDetail";
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

const active = {
  id: 5, name: "alice", enabled: true, expires_at: null, expired_at: null,
  frozen_at: null, status: "active", on_hold_seconds: null, status_changed_at: null, created_at: 1, note: "",
};

beforeEach(() => {
  setLocale("en");
  calls = [];
  routes = {
    "/api/v1/subjects/5": { body: active },
    "/api/v1/subjects/5/devices": { body: [] },
    "/api/v1/subjects/5/activity": { body: { activities: [], has_more: false } },
    "/api/v1/subjects/5/connections": { body: [] },
  };
  stubFetch();
});
afterEach(() => vi.unstubAllGlobals());

describe("credential rotation", () => {
  // The route existed with no button, so replacing a leaked credential meant
  // deleting the customer and recreating them.
  it("asks first, then rotates and shows the new value", async () => {
    routes["POST /api/v1/subjects/5/credentials/uuid/rotate"] = {
      body: { kind: "uuid", value: "11111111-2222-3333-4444-555555555555" },
    };
    const user = userEvent.setup({ delay: null });
    renderScreen(<SubjectDetail subjectId={5} />);
    await screen.findByText("alice");

    await user.click(screen.getByRole("button", { name: "Rotate UUID" }));
    const dialog = await screen.findByRole("dialog");
    // Rotation cuts off every client on the old value; that has to be said
    // before it happens, not after.
    expect(dialog).toHaveTextContent(/stops working/);
    expect(calls.some((c) => c.method === "POST")).toBe(false);

    await user.click(within(dialog).getByRole("button", { name: "Rotate" }));

    await waitFor(() =>
      expect(
        calls.some((c) => c.path === "/api/v1/subjects/5/credentials/uuid/rotate"),
      ).toBe(true),
    );
    // Shown straight away: the operator rotated it in order to hand the new
    // value over, and a second Reveal click is another disclosure for nothing.
    expect(
      await screen.findByText("11111111-2222-3333-4444-555555555555"),
    ).toBeInTheDocument();
  });

  it("rotates the password independently of the uuid", async () => {
    routes["POST /api/v1/subjects/5/credentials/password/rotate"] = {
      body: { kind: "password", value: "s3cret-value" },
    };
    const user = userEvent.setup({ delay: null });
    renderScreen(<SubjectDetail subjectId={5} />);
    await screen.findByText("alice");

    await user.click(screen.getByRole("button", { name: "Rotate password" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Rotate" }));

    await waitFor(() =>
      expect(
        calls.some((c) => c.path === "/api/v1/subjects/5/credentials/password/rotate"),
      ).toBe(true),
    );
    expect(calls.some((c) => c.path.includes("/uuid/rotate"))).toBe(false);
  });
});

describe("freeze and enable are separate controls", () => {
  // They were behind one ternary on `enabled`. Freezing does not change
  // `enabled`, so once frozen the Unfreeze button never appeared and the
  // operator could not undo their own revocation.
  it("offers Unfreeze once a subject is frozen", async () => {
    routes["/api/v1/subjects/5"] = {
      body: { ...active, frozen_at: 1700000000, frozen_reason: "quota_exceeded", status: "frozen" },
    };
    renderScreen(<SubjectDetail subjectId={5} />);
    await screen.findByText("alice");

    expect(screen.getByRole("button", { name: "Unfreeze" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Freeze" })).toBeNull();
    // Still enabled, so Disable is still the offer on that axis.
    expect(screen.getByRole("button", { name: "Disable" })).toBeInTheDocument();
  });

  it("says why a subject was frozen", async () => {
    routes["/api/v1/subjects/5"] = {
      body: { ...active, frozen_at: 1700000000, frozen_reason: "quota_exceeded", status: "frozen" },
    };
    renderScreen(<SubjectDetail subjectId={5} />);
    await screen.findByText("alice");

    expect(await screen.findByText("quota_exceeded")).toBeInTheDocument();
    expect(screen.getByText("Frozen")).toBeInTheDocument();
  });

  it("offers Freeze on a subject that is not frozen", async () => {
    renderScreen(<SubjectDetail subjectId={5} />);
    await screen.findByText("alice");

    expect(screen.getByRole("button", { name: "Freeze" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Unfreeze" })).toBeNull();
  });
});
