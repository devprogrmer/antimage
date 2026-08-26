import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { DeploymentPanel } from "./DeploymentPanel";
import { setLocale } from "../i18n";

// Four endpoints that existed and had no client. These cover the order the
// flow runs in, and the two places it must refuse.

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
    return new Response(JSON.stringify(route.body), { status: route.status ?? 200 });
  });
}

function renderPanel(node: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

function session(permissions: string[]) {
  routes["/api/v1/auth/me"] = {
    body: { admin_id: 1, role: "admin", is_super: false, permissions },
  };
}

beforeEach(() => {
  setLocale("en");
  calls = [];
  routes = { "/api/v1/deployments": { body: { deployments: [] } } };
  session(["node:read", "node:write"]);
  stubFetch();
});
afterEach(() => vi.unstubAllGlobals());

describe("the flow", () => {
  it("previews the node's desired revision, not a placeholder", async () => {
    routes["POST /api/v1/deployments/preview"] = {
      body: {
        node_id: 1, current_revision: 4, target_revision: 7,
        current_doc_sha256: "aaaa", target_doc_sha256: "bbbbbbbbbbbbbbbb",
      },
    };
    const user = userEvent.setup();
    renderPanel(<DeploymentPanel nodeId={1} targetRevision={7} />);

    await user.click(await screen.findByRole("button", { name: "Preview" }));

    const sent = calls.find((c) => c.path === "/api/v1/deployments/preview");
    // Revision 0 does not exist, so a placeholder would 404 on every click.
    expect(sent?.body).toEqual({ node_id: 1, revision: 7 });
    expect(await screen.findByText("7")).toBeInTheDocument();
  });

  it("will not validate until a preview has named a revision", async () => {
    renderPanel(<DeploymentPanel nodeId={1} targetRevision={7} />);
    expect(await screen.findByRole("button", { name: "Validate" })).toBeDisabled();
  });

  it("shows the conflicts a validation reports", async () => {
    routes["POST /api/v1/deployments/preview"] = {
      body: {
        node_id: 1, current_revision: 4, target_revision: 7,
        current_doc_sha256: "a", target_doc_sha256: "b",
      },
    };
    routes["POST /api/v1/deployments/validate"] = {
      body: { valid: false, conflicts: ["port 443 is already bound"], warnings: [] },
    };
    const user = userEvent.setup();
    renderPanel(<DeploymentPanel nodeId={1} targetRevision={7} />);

    await user.click(await screen.findByRole("button", { name: "Preview" }));
    await user.click(await screen.findByRole("button", { name: "Validate" }));

    expect(await screen.findByText("port 443 is already bound")).toBeInTheDocument();
    expect(screen.getByText("Conflicts")).toBeInTheDocument();
  });

  // §77: a restart-class change states its disruption before the click.
  it("asks before deploying, and sends nothing until confirmed", async () => {
    routes["POST /api/v1/deployments"] = { status: 201, body: { deployment_id: 3 } };
    const user = userEvent.setup();
    renderPanel(<DeploymentPanel nodeId={1} targetRevision={7} />);

    await user.click(await screen.findByRole("button", { name: "Deploy" }));

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(/sessions drop/i);
    expect(calls.some((c) => c.method === "POST" && c.path === "/api/v1/deployments")).toBe(false);

    await user.click(within(dialog).getByRole("button", { name: "Deploy" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && c.path === "/api/v1/deployments")).toBe(true),
    );
  });

  it("sends the chosen strategy", async () => {
    routes["POST /api/v1/deployments"] = { status: 201, body: { deployment_id: 3 } };
    const user = userEvent.setup();
    renderPanel(<DeploymentPanel nodeId={1} targetRevision={7} />);

    await user.selectOptions(await screen.findByLabelText("Strategy"), "canary");
    await user.click(screen.getByRole("button", { name: "Deploy" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Deploy" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "POST" && c.path === "/api/v1/deployments");
      expect(sent?.body).toEqual({ node_id: 1, strategy: "canary" });
    });
  });
});

describe("what it refuses to offer", () => {
  // The server checks node:write on every one of these. Offering a control
  // that can only 403 is worse than not offering it.
  it("offers no actions to a reader", async () => {
    session(["node:read"]);
    renderPanel(<DeploymentPanel nodeId={1} targetRevision={7} />);

    await screen.findByText("Deployments");
    expect(screen.queryByRole("button", { name: "Deploy" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Preview" })).toBeNull();
  });

  // The endpoints look a revision up by (node_id, revision); a node with none
  // would 404 on every button.
  it("says so when the node has no revision to deploy", async () => {
    renderPanel(<DeploymentPanel nodeId={1} targetRevision={0} />);
    expect(await screen.findByText(/no revisions to deploy/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Preview" })).toBeNull();
  });

  it("does not offer to roll back a deployment that is still running", async () => {
    routes["/api/v1/deployments"] = {
      body: {
        deployments: [
          { id: 1, node_id: 1, revision_id: 7, strategy: "canary", status: "in_progress",
            created_at: 1, started_at: 1, completed_at: null, error: "" },
        ],
      },
    };
    renderPanel(<DeploymentPanel nodeId={1} targetRevision={7} />);

    await screen.findByText("canary");
    // A deployment still in flight has no settled state to return to.
    expect(screen.queryByRole("button", { name: "Roll back" })).toBeNull();
  });
});

describe("the list", () => {
  it("shows only this node's deployments", async () => {
    routes["/api/v1/deployments"] = {
      body: {
        deployments: [
          { id: 1, node_id: 1, revision_id: 7, strategy: "rolling", status: "completed",
            created_at: 1, started_at: 1, completed_at: 2, error: "" },
          { id: 2, node_id: 99, revision_id: 3, strategy: "canary", status: "completed",
            created_at: 1, started_at: 1, completed_at: 2, error: "" },
        ],
      },
    };
    renderPanel(<DeploymentPanel nodeId={1} targetRevision={7} />);

    expect(await screen.findByText("rolling")).toBeInTheDocument();
    expect(screen.queryByText("canary")).toBeNull();
  });

  it("shows why a deployment failed", async () => {
    routes["/api/v1/deployments"] = {
      body: {
        deployments: [
          { id: 1, node_id: 1, revision_id: 7, strategy: "staged", status: "failed",
            created_at: 1, started_at: 1, completed_at: 2, error: "node 1 unreachable" },
        ],
      },
    };
    renderPanel(<DeploymentPanel nodeId={1} targetRevision={7} />);

    expect(await screen.findByText("node 1 unreachable")).toBeInTheDocument();
  });
});
