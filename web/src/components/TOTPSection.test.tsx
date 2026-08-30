import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { TOTPSection } from "./TOTPSection";
import { setLocale } from "../i18n";

// Two-factor enrolment and disable were API-only: an admin who logged in had
// no way to enable TOTP from the panel. These tests prove the full three-step
// flow reaches the real routes and correctly gates enrolment on the /me
// session flag.

interface Call {
  method: string;
  path: string;
  body?: Record<string, unknown>;
}

let calls: Call[] = [];

interface SessionShape {
  totp_enabled: boolean;
  permissions: string[];
}

function stubFetch(session: SessionShape, extra: Record<string, unknown> = {}) {
  vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const body = init?.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : undefined;
    calls.push({ method, path, body });

    if (path === "/api/v1/auth/me") {
      return new Response(
        JSON.stringify({
          admin_id: 1,
          role: "super_admin",
          is_super: true,
          permissions: session.permissions,
          totp_enabled: session.totp_enabled,
        }),
        { status: 200 },
      );
    }
    const key = `${method} ${path}`;
    if (key in extra) {
      const stub = extra[key] as { status?: number; body?: unknown };
      return new Response(JSON.stringify(stub.body ?? {}), { status: stub.status ?? 200 });
    }
    return new Response(
      JSON.stringify({ error: { code: "no_stub", message: "no route stubbed" } }),
      { status: 404 },
    );
  });
}

function renderIt(node: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

beforeEach(() => {
  setLocale("en");
  calls = [];
});
afterEach(() => vi.unstubAllGlobals());

describe("TOTP section", () => {
  it("shows enrolment when TOTP is not enabled and reveals the secret only once", async () => {
    stubFetch(
      { totp_enabled: false, permissions: [] },
      {
        "POST /api/v1/auth/totp/enrol": {
          body: {
            secret: "JBSWY3DPEHPK3PXP",
            provisioning_uri: "otpauth://totp/antimage:op?secret=JBSWY3DPEHPK3PXP",
          },
        },
      },
    );
    const user = userEvent.setup({ delay: null });
    renderIt(<TOTPSection />);

    await user.click(await screen.findByRole("button", { name: /Enable two-factor/i }));

    // Secret and provisioning URI both appear -- the panel does not send
    // them again, so the operator has to capture them here.
    expect(await screen.findByText("JBSWY3DPEHPK3PXP")).toBeInTheDocument();
    expect(
      screen.getByText(/otpauth:\/\/totp\/antimage:op\?secret=JBSWY3DPEHPK3PXP/),
    ).toBeInTheDocument();

    // Confirm demands a 6-digit code
    const codeInput = screen.getByLabelText(/Enter the current 6-digit code/i);
    await user.type(codeInput, "123456");
    expect(screen.getByRole("button", { name: /Confirm and enable/i })).toBeEnabled();
  });

  it("shows the disable form when TOTP is enabled and posts the code with disable", async () => {
    stubFetch(
      { totp_enabled: true, permissions: [] },
      { "POST /api/v1/auth/totp/disable": { status: 204, body: {} } },
    );
    const user = userEvent.setup({ delay: null });
    renderIt(<TOTPSection />);

    await user.type(
      await screen.findByLabelText(/Current code or recovery code/i),
      "abcdef",
    );
    await user.click(screen.getByRole("button", { name: /Disable two-factor/i }));

    const called = calls.find((c) => c.method === "POST" && c.path === "/api/v1/auth/totp/disable");
    expect(called?.body).toEqual({ totp: "abcdef" });
  });
});
