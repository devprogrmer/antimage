import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { Subjects } from "./Subjects";
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
    // Match ignoring the query string, so a filtered request still resolves.
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

function renderWithQuery(node: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  // The name cell is a Link now, which needs router context. Rendering the
  // screen without one is not a smaller test -- it is a screen that throws.
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>{node}</MemoryRouter>
    </QueryClientProvider>,
  );
}

const oneSubject = {
  subjects: [
    {
      id: 5,
      name: "alice",
      enabled: true,
      expires_at: null,
      expired_at: null,
      created_at: 1,
      note: "",
    },
  ],
  total: 1,
};

beforeEach(() => {
  setLocale("en");
  calls = [];
  routes = { "/api/v2/subjects": { body: oneSubject } };
  stubFetch();
});
afterEach(() => vi.unstubAllGlobals());

describe("Subjects", () => {
  // The list used to call /api/v1/subjects while a finished filter bar and a
  // finished search endpoint sat unconnected on either side of it.
  it("reads the searchable v2 endpoint", async () => {
    renderWithQuery(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");
    expect(calls.some((c) => c.path.startsWith("/api/v2/subjects"))).toBe(true);
    expect(calls.some((c) => c.path === "/api/v1/subjects")).toBe(false);
  });

  it("sends a typed filter to the server", async () => {
    const user = userEvent.setup();
    renderWithQuery(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    await user.type(screen.getByPlaceholderText(/name or note/i), "bob");

    // The bar debounces, so the request arrives shortly after the keystrokes.
    await waitFor(
      () => expect(calls.some((c) => c.path.includes("search=bob"))).toBe(true),
      { timeout: 2000 },
    );
  });

  it("opens the create form in a sheet rather than pushing the list down", async () => {
    const user = userEvent.setup();
    renderWithQuery(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");
    expect(screen.queryByRole("dialog")).toBeNull();

    await user.click(screen.getByRole("button", { name: "Create User" }));

    const sheet = await screen.findByRole("dialog");
    // Focus moves INTO the panel. The inline form it replaced left the
    // keyboard on the button behind it.
    expect(sheet).toContainElement(document.activeElement as HTMLElement | null);
    expect(within(sheet).getByLabelText("Name")).toBeInTheDocument();
  });

  it("closes the create sheet on Escape", async () => {
    const user = userEvent.setup();
    renderWithQuery(<Subjects onSelect={() => {}} />);
    await screen.findByText("alice");

    await user.click(screen.getByRole("button", { name: "Create User" }));
    await screen.findByRole("dialog");
    await user.keyboard("{Escape}");

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
});
