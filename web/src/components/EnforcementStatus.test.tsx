import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { EnforcementStatus } from "./EnforcementStatus";
import { setLocale } from "../i18n";

let routes: Record<string, { status?: number; body: unknown }> = {};

function stubFetch() {
  vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
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

/** Everything unset, i.e. every limit unlimited. */
const unlimited = {
  subject_id: 5,
  current_devices: 3,
  current_ips: 2,
  current_connections: 7,
};

function stub(body: Record<string, unknown>) {
  routes["/api/v1/subjects/5/enforcement"] = { body: { ...unlimited, ...body } };
}

beforeEach(() => {
  setLocale("en");
  routes = {};
  stubFetch();
});
afterEach(() => vi.unstubAllGlobals());

describe("EnforcementStatus", () => {
  // null means unlimited, not zero. Rendering an absent cap as 0 turns "no
  // restriction" into "no allowance", and would show a customer at 3/0 devices
  // as permanently over their limit.
  it("shows an absent limit as unlimited rather than zero", async () => {
    stub({});
    renderScreen(<EnforcementStatus subjectId={5} />);

    expect(await screen.findByText("3, unlimited")).toBeInTheDocument();
    expect(screen.getByText("2, unlimited")).toBeInTheDocument();
    expect(screen.getByText("7, unlimited")).toBeInTheDocument();
    // And no bar, because there is no ceiling to fill against.
    expect(screen.queryAllByRole("progressbar")).toHaveLength(0);
    expect(screen.queryByText(/At the limit/)).toBeNull();
  });

  it("shows current against the configured limit", async () => {
    stub({ max_devices: 5, max_ips: 4, max_connections: 10 });
    renderScreen(<EnforcementStatus subjectId={5} />);

    expect(await screen.findByText("3 of 5")).toBeInTheDocument();
    expect(screen.getByText("2 of 4")).toBeInTheDocument();
    expect(screen.getByText("7 of 10")).toBeInTheDocument();

    const bars = screen.getAllByRole("progressbar");
    expect(bars).toHaveLength(3);
    expect(bars[0]).toHaveAttribute("aria-valuenow", "3");
    expect(bars[0]).toHaveAttribute("aria-valuemax", "5");
  });

  // This is the card's whole purpose: the customer says it stopped working and
  // the answer is that the panel is refusing them, exactly as configured.
  it("says so when a limit is reached", async () => {
    stub({ max_devices: 3 });
    renderScreen(<EnforcementStatus subjectId={5} />);

    expect(await screen.findByText("3 of 3")).toBeInTheDocument();
    expect(screen.getByText(/At the limit/)).toBeInTheDocument();
  });

  it("treats being over the limit as at the limit, not as under it", async () => {
    // A cap lowered below current usage is an ordinary operator action, and the
    // bar must not overflow its track when it happens.
    stub({ max_devices: 2 });
    renderScreen(<EnforcementStatus subjectId={5} />);

    expect(await screen.findByText("3 of 2")).toBeInTheDocument();
    expect(screen.getByText(/At the limit/)).toBeInTheDocument();
  });

  // A limit of 0 is a real cap meaning "none allowed" -- distinct from null --
  // and dividing by it would give Infinity.
  it("handles a zero limit without dividing by it", async () => {
    stub({ max_connections: 0 });
    renderScreen(<EnforcementStatus subjectId={5} />);

    expect(await screen.findByText("7 of 0")).toBeInTheDocument();
    expect(screen.getByText(/At the limit/)).toBeInTheDocument();
  });

  describe("speed", () => {
    it("reads out in Mbps, which is how a plan is sold", async () => {
      stub({ speed_limit_up_kbps: 5000, speed_limit_down_kbps: 20480 });
      renderScreen(<EnforcementStatus subjectId={5} />);

      // 5000 kbps is exactly 5, so no misleading ".0".
      expect(await screen.findByText("5 Mbps")).toBeInTheDocument();
      expect(screen.getByText("20.5 Mbps")).toBeInTheDocument();
    });

    it("stays in kbps below a megabit", async () => {
      stub({ speed_limit_up_kbps: 512 });
      renderScreen(<EnforcementStatus subjectId={5} />);

      expect(await screen.findByText("512 kbps")).toBeInTheDocument();
    });

    it("shows an unset speed cap as unlimited", async () => {
      stub({});
      renderScreen(<EnforcementStatus subjectId={5} />);

      // Both directions.
      expect(await screen.findAllByText("Unlimited")).toHaveLength(2);
    });
  });

  it("surfaces a refusal rather than rendering an empty card", async () => {
    routes["/api/v1/subjects/5/enforcement"] = {
      status: 403,
      body: { error: { code: "forbidden", message: "insufficient permissions" } },
    };
    renderScreen(<EnforcementStatus subjectId={5} />);

    expect(await screen.findByText(/insufficient permissions/)).toBeInTheDocument();
  });
});

describe("localised digits", () => {
  // Every other number on the card goes through formatNumber; a speed built
  // with String()/toFixed() would print Latin digits next to Persian ones.
  it("uses Persian digits for a speed in Persian", async () => {
    setLocale("fa");
    stub({ speed_limit_up_kbps: 5000 });
    renderScreen(<EnforcementStatus subjectId={5} />);

    expect(await screen.findByText("۵ مگابیت بر ثانیه")).toBeInTheDocument();
  });
});
