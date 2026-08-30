import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { Subjects } from "../routes/Subjects";
import { setLocale } from "../i18n";

// Export and import are driven through the Subjects screen for the same reason
// the bulk bar is: both endpoints existed with no client at all, so a test that
// mounted the component directly would have proved nothing about whether an
// operator can reach them.

interface Call {
  method: string;
  path: string;
  body: unknown;
}

let calls: Call[] = [];
let routes: Record<string, { status?: number; body?: unknown; text?: string }> = {};

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
    // text wins over body so a CSV response is not JSON-encoded on its way out.
    if (route.text !== undefined) {
      return new Response(route.text, {
        status: route.status ?? 200,
        headers: { "Content-Type": "text/csv" },
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

const oneSubject = {
  subjects: [
    {
      id: 5, name: "alice", enabled: true, expires_at: null,
      expired_at: null, frozen_at: null, status: "active", on_hold_seconds: null, status_changed_at: null, created_at: 1, note: "",
    },
  ],
  total: 1,
};

let clicked: string[] = [];

beforeEach(() => {
  setLocale("en");
  calls = [];
  clicked = [];
  routes = { "/api/v2/subjects": { body: oneSubject } };
  stubFetch();

  // jsdom implements neither, and the export builds a blob URL and clicks a
  // detached anchor to start the download.
  vi.stubGlobal("URL", {
    ...URL,
    createObjectURL: () => "blob:stub",
    revokeObjectURL: () => {},
  });
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
    this: HTMLAnchorElement,
  ) {
    clicked.push(this.download);
  });
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("export", () => {
  it("downloads the CSV the server returns", async () => {
    routes["/api/v1/subjects/export"] = { text: "ID,Name\n5,alice\n" };
    const user = userEvent.setup({ delay: null });
    renderScreen(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    await user.click(screen.getByRole("button", { name: "Export CSV" }));

    await waitFor(() => expect(clicked).toEqual(["subjects.csv"]));
    expect(calls.some((c) => c.path === "/api/v1/subjects/export")).toBe(true);
  });

  // Export is scope-checked and can legitimately answer 403. Navigating to the
  // endpoint would have replaced the panel with a bare error page; fetching it
  // keeps the failure on the screen that asked for it.
  it("shows a refusal instead of downloading an error body", async () => {
    routes["/api/v1/subjects/export"] = {
      status: 403,
      body: { error: { code: "forbidden", message: "not permitted" } },
    };
    const user = userEvent.setup({ delay: null });
    renderScreen(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    await user.click(screen.getByRole("button", { name: "Export CSV" }));

    expect(await screen.findByText(/not permitted/)).toBeInTheDocument();
    expect(clicked).toEqual([]);
  });
});

describe("import", () => {
  it("sends the pasted CSV and reports both counts", async () => {
    routes["POST /api/v1/subjects/import"] = {
      body: { imported: 2, failed: 1, errors: ["row 3: name required"] },
    };
    const user = userEvent.setup({ delay: null });
    renderScreen(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    await user.click(screen.getByRole("button", { name: "Import CSV" }));
    const sheet = await screen.findByRole("dialog");
    await user.type(within(sheet).getByLabelText("CSV contents"), "Name{enter}bob");
    await user.click(within(sheet).getByRole("button", { name: "Import" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.path === "/api/v1/subjects/import");
      expect(sent?.body).toEqual({ csv: "Name\nbob" });
    });

    // A partial import is the normal case; "done" would hide the skipped row.
    const status = await within(sheet).findByRole("status");
    expect(status).toHaveTextContent("2 imported, 1 failed");
    expect(status).toHaveTextContent("row 3: name required");
  });

  it("will not post an empty import", async () => {
    const user = userEvent.setup({ delay: null });
    renderScreen(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    await user.click(screen.getByRole("button", { name: "Import CSV" }));
    const sheet = await screen.findByRole("dialog");

    expect(within(sheet).getByRole("button", { name: "Import" })).toBeDisabled();
    expect(calls.some((c) => c.path === "/api/v1/subjects/import")).toBe(false);
  });
});
