import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SSHBootstrapWizard } from "./SSHBootstrapWizard";
import { setLocale } from "../i18n";

// The earlier wizard polled /bootstrap-ssh/status/{jobId} for a job that did
// not exist -- the backend has no job store. The real protocol is two
// synchronous POSTs against the same route: the first returns the host key
// for the admin to confirm, the second returns the install script's output.

interface Call {
  method: string;
  path: string;
  body: Record<string, unknown>;
}

let calls: Call[] = [];
// Bootstrap POST responses come in phases: the same URL returns different
// payloads on the first call (host key) and the second (installer output).
// A queue per URL keeps that explicit; a single top-level queue would race
// against the session fetch that fires from useSession.
let bootstrapQueue: Array<{ status: number; body: unknown }> = [];

function stubFetch() {
  vi.stubGlobal("fetch", async (path: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const body =
      init?.body === undefined
        ? {}
        : (JSON.parse(String(init.body)) as Record<string, unknown>);
    calls.push({ method, path, body });

    if (path === "/api/v1/auth/me") {
      return new Response(
        JSON.stringify({
          admin_id: 1,
          role: "super_admin",
          is_super: true,
          permissions: ["node:write"],
        }),
        { status: 200 },
      );
    }

    if (method === "POST" && path.endsWith("/bootstrap-ssh")) {
      const next = bootstrapQueue.shift();
      if (next === undefined) {
        return new Response(
          JSON.stringify({ error: { code: "no_stub", message: "no bootstrap response queued" } }),
          { status: 500 },
        );
      }
      return new Response(JSON.stringify(next.body), { status: next.status });
    }

    return new Response(
      JSON.stringify({ error: { code: "not_found", message: "no route stubbed" } }),
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
  bootstrapQueue = [];
  stubFetch();
});
afterEach(() => vi.unstubAllGlobals());

async function fillForm(user: ReturnType<typeof userEvent.setup>) {
  // useSession() resolves asynchronously; without waiting for it the gate
  // component renders the "no permission" fallback and the form is not in
  // the tree at all.
  await screen.findByLabelText(/Host/i);
  await user.type(screen.getByLabelText(/Host/i), "10.0.0.7");
  await user.type(screen.getByLabelText(/Private key/i), "-----BEGIN OPENSSH PRIVATE KEY-----");
}

describe("SSH bootstrap wizard", () => {
  it("shows the host fingerprint for the admin to confirm before running anything", async () => {
    // Phase one: the panel reads the host key and refuses to execute yet.
    bootstrapQueue.push({
      status: 200,
      body: {
        host_key_fingerprint: "SHA256:abc123",
        confirm_required: true,
      },
    });
    const user = userEvent.setup({ delay: null });
    renderIt(<SSHBootstrapWizard nodeId={7} />);

    await fillForm(user);
    await user.click(screen.getByRole("button", { name: /Read host key/i }));

    // The fingerprint has to appear so a human decides whether it matches
    // the host they meant. Silently proceeding is what the two-phase design
    // exists to prevent.
    expect(await screen.findByText("SHA256:abc123")).toBeInTheDocument();

    // The first POST MUST NOT carry a fingerprint. If it does, the panel
    // treats it as phase two and will actually run the install script
    // against a host nobody confirmed.
    expect(calls[calls.length - 1].body.host_key_fingerprint).toBeUndefined();
  });

  it("runs the installer with the confirmed fingerprint and shows the output", async () => {
    bootstrapQueue.push({
      status: 200,
      body: { host_key_fingerprint: "SHA256:abc123", confirm_required: true },
    });
    bootstrapQueue.push({ status: 200, body: { output: "+ curl … installed" } });

    const user = userEvent.setup({ delay: null });
    renderIt(<SSHBootstrapWizard nodeId={7} />);

    await fillForm(user);
    await user.click(screen.getByRole("button", { name: /Read host key/i }));
    await screen.findByText("SHA256:abc123");
    await user.click(screen.getByRole("button", { name: /Confirm and install/i }));

    // The completed screen is what the operator needs to see to know the
    // node is enrolled -- and the install script's own output has to be
    // rendered, not summarised.
    expect(await screen.findByText(/Bootstrap Complete/i)).toBeInTheDocument();
    expect(screen.getByText(/\+ curl … installed/)).toBeInTheDocument();

    // Phase two request has to carry the fingerprint the admin confirmed;
    // an empty one would restart phase one and never execute anything.
    expect(calls[calls.length - 1].body.host_key_fingerprint).toBe("SHA256:abc123");
  });

  it("shows the installer's stderr when the run fails, not just the error line", async () => {
    bootstrapQueue.push({
      status: 200,
      body: { host_key_fingerprint: "SHA256:xyz", confirm_required: true },
    });
    // The 502 body carries `output` alongside the error envelope. An
    // operator fixing a failed install needs to read what the install
    // script actually said -- the error line alone is a header without
    // the receipt.
    bootstrapQueue.push({
      status: 502,
      body: {
        error: { code: "bootstrap_failed", message: "systemctl start antimage-agent: exit 1" },
        output: "+ apt-get install -y curl\n+ curl …\nFailed to start antimage-agent.service",
      },
    });

    const user = userEvent.setup({ delay: null });
    renderIt(<SSHBootstrapWizard nodeId={7} />);

    await fillForm(user);
    await user.click(screen.getByRole("button", { name: /Read host key/i }));
    await screen.findByText("SHA256:xyz");
    await user.click(screen.getByRole("button", { name: /Confirm and install/i }));

    // The message line
    expect(await screen.findByText(/systemctl start antimage-agent/)).toBeInTheDocument();
    // AND the installer's own output
    expect(
      await screen.findByText(/Failed to start antimage-agent\.service/),
    ).toBeInTheDocument();
  });
});
