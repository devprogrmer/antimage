import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SubscriptionGroups, SubjectGroupPicker } from "./SubscriptionGroups";
import { setLocale } from "../i18n";

interface Call {
  method: string;
  path: string;
  body: unknown;
}

let calls: Call[] = [];
let routes: Record<string, { status?: number; body?: unknown }> = {};

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

function renderIt(node: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

const cheapTier = {
  id: 3, name: "cheap", description: "one protocol",
  protocols: ["vless"], is_public: true,
  created_by: 1, created_at: 1, updated_at: 1,
};

const everything = {
  id: 4, name: "everything", description: "",
  protocols: [], is_public: false,
  created_by: 1, created_at: 1, updated_at: 1,
};

function seedGroups(groups: unknown[] = [cheapTier, everything]) {
  routes["/api/v1/subscription-groups"] = {
    body: {
      groups,
      available_protocols: [
        "hysteria2", "l2tp", "ocserv", "openvpn", "shadowsocks",
        "trojan", "vless", "vmess", "wireguard",
      ],
    },
  };
}

beforeEach(() => {
  setLocale("en");
  calls = [];
  routes = {};
  stubFetch();
});
afterEach(() => vi.unstubAllGlobals());

describe("managing groups", () => {
  // Empty and "everything" are OPPOSITE readings of the same empty list. An
  // operator who reads it the wrong way cuts their customers off.
  it("says an empty selection carries every protocol", async () => {
    seedGroups();
    renderIt(<SubscriptionGroups />);
    await screen.findByText("everything");

    expect(screen.getByText("Carries every protocol")).toBeInTheDocument();
    // And a group that DOES name protocols shows them instead.
    expect(screen.getByText("vless")).toBeInTheDocument();
  });

  it("offers only the protocols the panel can produce", async () => {
    seedGroups([]);
    const user = userEvent.setup({ delay: null });
    renderIt(<SubscriptionGroups />);
    await screen.findByText(/No subscription groups yet/);

    await user.click(screen.getByRole("button", { name: "New group" }));

    // The list comes from the server, not from a copy the UI keeps in step.
    for (const p of ["vless", "hysteria2", "wireguard", "openvpn"]) {
      expect(await screen.findByRole("checkbox", { name: p })).toBeInTheDocument();
    }
    expect(screen.queryByRole("checkbox", { name: "quic" })).toBeNull();
  });

  it("creates a group with the protocols that were ticked", async () => {
    seedGroups([]);
    routes["POST /api/v1/subscription-groups"] = { status: 201, body: cheapTier };
    const user = userEvent.setup({ delay: null });
    renderIt(<SubscriptionGroups />);
    await screen.findByText(/No subscription groups yet/);

    await user.click(screen.getByRole("button", { name: "New group" }));
    await user.type(await screen.findByLabelText("Name"), "starter");
    await user.click(screen.getByRole("checkbox", { name: "vless" }));
    await user.click(screen.getByRole("checkbox", { name: "trojan" }));
    await user.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "POST");
      expect(sent?.body).toMatchObject({
        name: "starter",
        protocols: ["vless", "trojan"],
      });
    });
  });

  // The form has to state the rule where the decision is made, not in a doc.
  it("warns that ticking nothing means everything", async () => {
    seedGroups([]);
    const user = userEvent.setup({ delay: null });
    renderIt(<SubscriptionGroups />);
    await screen.findByText(/No subscription groups yet/);

    await user.click(screen.getByRole("button", { name: "New group" }));
    expect(
      await screen.findByText(/Nothing selected means every protocol, not none/),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("checkbox", { name: "vless" }));
    expect(
      screen.getByText(/Only these protocols appear in the subscription/),
    ).toBeInTheDocument();
  });

  it("loads an existing group into the form and PUTs the change", async () => {
    seedGroups();
    routes["PUT /api/v1/subscription-groups/3"] = { body: cheapTier };
    const user = userEvent.setup({ delay: null });
    renderIt(<SubscriptionGroups />);
    await screen.findByText("cheap");

    await user.click(screen.getAllByRole("button", { name: "Edit" })[0]);

    expect(await screen.findByLabelText("Name")).toHaveValue("cheap");
    expect(screen.getByRole("checkbox", { name: "vless" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "trojan" })).not.toBeChecked();

    await user.click(screen.getByRole("checkbox", { name: "trojan" }));
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "PUT");
      expect(sent?.body).toMatchObject({ protocols: ["vless", "trojan"] });
    });
  });

  // Deleting a tier must not read as deleting its customers.
  it("says what happens to users on a group before deleting it", async () => {
    seedGroups();
    routes["DELETE /api/v1/subscription-groups/3"] = { status: 204, body: {} };
    const user = userEvent.setup({ delay: null });
    renderIt(<SubscriptionGroups />);
    await screen.findByText("cheap");

    await user.click(screen.getAllByRole("button", { name: "Delete" })[0]);
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(/keep their access/);
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);

    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
    await waitFor(() =>
      expect(calls.some((c) => c.path === "/api/v1/subscription-groups/3")).toBe(true),
    );
  });

  it("surfaces a refusal for an unknown protocol rather than swallowing it", async () => {
    seedGroups([]);
    routes["POST /api/v1/subscription-groups"] = {
      status: 422,
      body: { error: { code: "validation", message: `unknown protocol "quic"` } },
    };
    const user = userEvent.setup({ delay: null });
    renderIt(<SubscriptionGroups />);
    await screen.findByText(/No subscription groups yet/);

    await user.click(screen.getByRole("button", { name: "New group" }));
    await user.type(await screen.findByLabelText("Name"), "bad");
    await user.click(screen.getByRole("button", { name: "Create" }));

    expect(await screen.findByText(/unknown protocol/)).toBeInTheDocument();
  });
});

describe("putting a subject on a group", () => {
  it("assigns the chosen group", async () => {
    seedGroups();
    routes["PUT /api/v1/subjects/5/subscription-group"] = { status: 204, body: {} };
    const user = userEvent.setup({ delay: null });
    renderIt(<SubjectGroupPicker subjectId={5} current={[]} />);
    // The select renders before the groups load, so waiting for the select
    // itself races the option into existence.
    await screen.findByRole("option", { name: "cheap" });

    await user.selectOptions(screen.getByRole("combobox"), "3");

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "PUT");
      expect(sent?.path).toBe("/api/v1/subjects/5/subscription-group");
      expect(sent?.body).toEqual({ group_id: 3 });
    });
  });

  // Clearing sends null, not 0 or "": the server distinguishes "no group" from
  // "group zero", and an empty string would be a bad request.
  it("clears the group with null", async () => {
    seedGroups();
    routes["PUT /api/v1/subjects/5/subscription-group"] = { status: 204, body: {} };
    const user = userEvent.setup({ delay: null });
    renderIt(<SubjectGroupPicker subjectId={5} current={["vless"]} />);
    await screen.findByRole("option", { name: "cheap" });

    await user.selectOptions(screen.getByRole("combobox"), "");

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "PUT");
      expect(sent?.body).toEqual({ group_id: null });
    });
  });

  it("shows which protocols the current group carries", async () => {
    seedGroups();
    renderIt(<SubjectGroupPicker subjectId={5} current={["vless", "trojan"]} />);
    expect(await screen.findByText("vless, trojan")).toBeInTheDocument();
  });

  // "No group" is not an absence of configuration -- it means the customer
  // receives everything they are assigned, which is worth saying.
  it("spells out what no group means", async () => {
    seedGroups();
    renderIt(<SubjectGroupPicker subjectId={5} current={[]} />);
    expect(
      await screen.findByRole("option", { name: /everything they are assigned/ }),
    ).toBeInTheDocument();
  });
});
