import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { Subjects } from "../routes/Subjects";
import { setLocale } from "../i18n";

// The bulk bar is driven through the real Subjects screen rather than in
// isolation, because the thing that was broken was never the component: it was
// that nothing rendered it and it called nothing. A test that mounts
// <BulkActions> directly and passes it props would have passed for the whole
// time the feature did not exist.

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
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>{node}</MemoryRouter>
    </QueryClientProvider>,
  );
}

const twoSubjects = {
  subjects: [
    { id: 5, name: "alice", enabled: true, expires_at: null, expired_at: null, frozen_at: null, status: "active", on_hold_seconds: null, status_changed_at: null, created_at: 1, note: "" },
    { id: 9, name: "bob", enabled: true, expires_at: null, expired_at: null, frozen_at: null, status: "active", on_hold_seconds: null, status_changed_at: null, created_at: 2, note: "" },
  ],
  total: 2,
};

beforeEach(() => {
  setLocale("en");
  calls = [];
  routes = { "/api/v2/subjects": { body: twoSubjects } };
  stubFetch();
});
afterEach(() => vi.unstubAllGlobals());

/** Ticks the checkbox for one named subject. */
async function select(user: ReturnType<typeof userEvent.setup>, name: string) {
  await user.click(screen.getByRole("checkbox", { name }));
}

describe("bulk actions", () => {
  it("offers nothing until a row is selected", async () => {
    renderScreen(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");
    expect(screen.queryByRole("button", { name: "Bulk Actions" })).toBeNull();
  });

  it("disables exactly the selected subjects", async () => {
    routes["POST /api/v1/subjects/bulk/disable"] = { body: { disabled: 1, failed: 0 } };
    const user = userEvent.setup({ delay: null });
    renderScreen(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    await select(user, "alice");
    await user.click(screen.getByRole("button", { name: "Bulk Actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Disable" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.path === "/api/v1/subjects/bulk/disable");
      expect(sent?.body).toEqual({ subject_ids: [5] });
    });
  });

  // The menu offered Disable against a route that did not exist, so this asserts
  // the pair an operator actually needs.
  it("enables the selected subjects", async () => {
    routes["POST /api/v1/subjects/bulk/enable"] = { body: { enabled: 2, failed: 0 } };
    const user = userEvent.setup({ delay: null });
    renderScreen(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    await user.click(screen.getByRole("checkbox", { name: "Select all rows" }));
    await user.click(screen.getByRole("button", { name: "Bulk Actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Enable" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.path === "/api/v1/subjects/bulk/enable");
      expect(sent?.body).toEqual({ subject_ids: [5, 9] });
    });
  });

  it("asks before deleting and names how many are affected", async () => {
    routes["POST /api/v1/subjects/bulk/delete"] = { body: { deleted: 1, failed: 0 } };
    const user = userEvent.setup({ delay: null });
    renderScreen(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    await select(user, "bob");
    await user.click(screen.getByRole("button", { name: "Bulk Actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Delete" }));

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("This affects 1 subjects.");
    expect(calls.some((c) => c.path.includes("bulk/delete"))).toBe(false);

    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
    await waitFor(() => {
      const sent = calls.find((c) => c.path === "/api/v1/subjects/bulk/delete");
      expect(sent?.body).toEqual({ subject_ids: [9] });
    });
  });

  it("converts the quota field from GB to bytes", async () => {
    routes["POST /api/v1/subjects/bulk/set-quota"] = { body: { updated: 1, failed: 0 } };
    const user = userEvent.setup({ delay: null });
    renderScreen(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    await select(user, "alice");
    await user.click(screen.getByRole("button", { name: "Bulk Actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Set Quota" }));

    const dialog = await screen.findByRole("dialog");
    const field = within(dialog).getByLabelText(/Quota/);
    await user.clear(field);
    await user.type(field, "2");
    await user.click(within(dialog).getByRole("button", { name: "Set Quota" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.path === "/api/v1/subjects/bulk/set-quota");
      expect(sent?.body).toEqual({ subject_ids: [5], quota_bytes: 2 * 1024 * 1024 * 1024 });
    });
  });

  // These endpoints are partial-success by design. Reporting "done" over a
  // batch where most rows were skipped is how an operator comes to believe a
  // change landed that did not.
  it("reports how many actually changed, not just success", async () => {
    routes["POST /api/v1/subjects/bulk/enable"] = {
      body: { enabled: 1, failed: 1, errors: ["subject 9: forbidden"] },
    };
    const user = userEvent.setup({ delay: null });
    renderScreen(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    await user.click(screen.getByRole("checkbox", { name: "Select all rows" }));
    await user.click(screen.getByRole("button", { name: "Bulk Actions" }));
    await user.click(await screen.findByRole("menuitem", { name: "Enable" }));

    const status = await screen.findByRole("status");
    expect(status).toHaveTextContent("1 changed, 1 failed");
    expect(status).toHaveTextContent("subject 9: forbidden");
  });

  // The filter bar emits its initial state shortly after mount, which is not a
  // change. Clearing on every emission wiped the selection out from under an
  // operator who ticked rows before the debounce fired -- silently, and only
  // when they were quick, which is why it surfaced as an intermittent failure
  // rather than an obvious one.
  it("keeps the selection through the filter bar's initial emission", async () => {
    const user = userEvent.setup({ delay: null });
    renderScreen(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    await select(user, "alice");

    // Wait for the bar's own first emission to land: it refetches, so a second
    // GET is the signal that it happened.
    await waitFor(() =>
      expect(calls.filter((c) => c.path.startsWith("/api/v2/subjects")).length)
        .toBeGreaterThan(1),
    );

    expect(screen.getByRole("button", { name: "Bulk Actions" })).toBeInTheDocument();
    expect(screen.getByText("1 selected")).toBeInTheDocument();
  });

  // The invariant: never act on a row the operator cannot see. Filters run
  // server-side, so a narrowed list can hide something already ticked -- and
  // the ticked id must go with it.
  it("drops a selected subject that the filter hides", async () => {
    const user = userEvent.setup({ delay: null });
    renderScreen(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    await select(user, "alice");
    expect(screen.getByRole("button", { name: "Bulk Actions" })).toBeInTheDocument();

    // The next fetch no longer returns alice, which is what a real narrowing
    // filter does.
    routes["/api/v2/subjects"] = {
      body: {
        subjects: [
          { id: 9, name: "bob", enabled: true, expires_at: null, expired_at: null, frozen_at: null, status: "active", on_hold_seconds: null, status_changed_at: null, created_at: 2, note: "" },
        ],
        total: 1,
      },
    };
    await user.type(screen.getByPlaceholderText(/name or note/i), "bob");

    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Bulk Actions" })).toBeNull(),
    );
  });

});

// on_hold: a plan sold today whose 30 days start when the customer first uses
// it. Antimage had no way to express this -- expiry was absolute from creation,
// so whatever time passed before setup came out of what the customer paid for.
describe("selling a plan on hold", () => {
  it("sends no duration when the plan starts now", async () => {
    routes["POST /api/v1/subjects"] = { status: 201, body: { id: 12 } };
    const user = userEvent.setup({ delay: null });
    renderScreen(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    await user.click(screen.getByRole("button", { name: "Create User" }));
    const sheet = await screen.findByRole("dialog");
    await user.type(within(sheet).getByLabelText("Name"), "carol");
    await user.click(within(sheet).getByRole("button", { name: "Create" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "POST" && c.path === "/api/v1/subjects");
      // Absent, not null or 0: the server rejects a non-positive duration, so
      // sending one would turn an ordinary sale into an error.
      expect(sent?.body).toEqual({ name: "carol", note: "", service_ids: [] });
    });
  });

  it("converts the validity field from days to seconds", async () => {
    routes["POST /api/v1/subjects"] = { status: 201, body: { id: 13 } };
    const user = userEvent.setup({ delay: null });
    renderScreen(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    await user.click(screen.getByRole("button", { name: "Create User" }));
    const sheet = await screen.findByRole("dialog");
    await user.type(within(sheet).getByLabelText("Name"), "dave");
    await user.click(within(sheet).getByRole("radio", { name: "Starts on first use" }));
    const days = within(sheet).getByLabelText("Validity (days)");
    await user.clear(days);
    await user.type(days, "7");
    await user.click(within(sheet).getByRole("button", { name: "Create" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "POST" && c.path === "/api/v1/subjects");
      expect(sent?.body).toEqual({
        name: "dave", note: "", service_ids: [],
        on_hold_seconds: 7 * 24 * 60 * 60,
      });
    });
  });

  it("will not create an on-hold plan with no duration", async () => {
    const user = userEvent.setup({ delay: null });
    renderScreen(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    await user.click(screen.getByRole("button", { name: "Create User" }));
    const sheet = await screen.findByRole("dialog");
    await user.type(within(sheet).getByLabelText("Name"), "erin");
    await user.click(within(sheet).getByRole("radio", { name: "Starts on first use" }));
    await user.clear(within(sheet).getByLabelText("Validity (days)"));

    expect(within(sheet).getByRole("button", { name: "Create" })).toBeDisabled();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("shows an on-hold subject as waiting rather than active", async () => {
    routes["/api/v2/subjects"] = {
      body: {
        subjects: [
          {
            id: 5, name: "alice", enabled: true, expires_at: null, expired_at: null,
            frozen_at: null, status: "on_hold", on_hold_seconds: 2592000,
            status_changed_at: 1700000000, created_at: 1, note: "",
          },
        ],
        total: 1,
      },
    };
    renderScreen(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    // Scoped to the table: the filter bar has its own "Active" <option>, and a
    // bare queryByText would match that instead of the row's badge.
    const table = within(screen.getByRole("table"));
    expect(table.getByText("On hold")).toBeInTheDocument();
    expect(table.queryByText("Active")).toBeNull();
  });
});

// Plans: user_presets carried quota, validity and auto-assigned services with a
// management screen, and nothing had ever applied one to a subject.
describe("selling from a plan", () => {
  const plans = {
    presets: [
      { id: 3, name: "monthly", quota_bytes: null, validity_days: 30, on_hold: false },
      { id: 4, name: "trial", quota_bytes: null, validity_days: 7, on_hold: true },
    ],
  };

  beforeEach(() => {
    routes["/api/v1/presets/users"] = { body: plans };
    routes["POST /api/v1/subjects"] = { status: 201, body: { id: 20 } };
  });

  it("sends the chosen plan with the subject", async () => {
    const user = userEvent.setup({ delay: null });
    renderScreen(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    await user.click(screen.getByRole("button", { name: "Create User" }));
    const sheet = await screen.findByRole("dialog");
    await user.type(within(sheet).getByLabelText("Name"), "frank");
    await user.selectOptions(await within(sheet).findByLabelText("Plan"), "3");
    await user.click(within(sheet).getByRole("button", { name: "Create" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "POST" && c.path === "/api/v1/subjects");
      expect(sent?.body).toEqual({
        name: "frank", note: "", service_ids: [], preset_id: 3,
      });
    });
  });

  it("labels an on-hold plan differently from an ordinary one", async () => {
    const user = userEvent.setup({ delay: null });
    renderScreen(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    await user.click(screen.getByRole("button", { name: "Create User" }));
    const sheet = await screen.findByRole("dialog");
    const select = await within(sheet).findByLabelText("Plan");

    expect(within(select).getByText(/monthly — 30 days$/)).toBeInTheDocument();
    expect(within(select).getByText(/trial — 7 days from first use/)).toBeInTheDocument();
  });

  it("defaults to no plan, and then sends none", async () => {
    const user = userEvent.setup({ delay: null });
    renderScreen(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    await user.click(screen.getByRole("button", { name: "Create User" }));
    const sheet = await screen.findByRole("dialog");
    await user.type(within(sheet).getByLabelText("Name"), "grace");
    await user.click(within(sheet).getByRole("button", { name: "Create" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "POST" && c.path === "/api/v1/subjects");
      expect(sent?.body).not.toHaveProperty("preset_id");
    });
  });
});
