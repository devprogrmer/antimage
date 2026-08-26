import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { InboundStudio } from "./InboundStudio";
import { groupOf, groupsOf } from "./SchemaForm";
import { setLocale } from "../i18n";

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

function renderWithQuery(node: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

const wgSchema = {
  type: "object",
  additionalProperties: false,
  required: ["port", "private_key"],
  properties: {
    port: { type: "integer", minimum: 1, maximum: 65535 },
    private_key: { type: "string" },
    subnet: { type: "string" },
    dns: { type: "array", items: { type: "string" } },
    obfs: { type: "string", enum: ["", "salamander"] },
  },
};

function schemas(...adapters: unknown[]) {
  return { body: { node_id: 1, adapters } };
}

beforeEach(() => {
  setLocale("en");
  calls = [];
  routes = {};
  stubFetch();
});
afterEach(() => vi.unstubAllGlobals());

// ------------------------------------------------------------ the guarantee

// THE guarantee this whole phase exists for: the editor offers what the NODE
// reports, never what the panel happens to know about.
it("offers only the protocols the node reported", async () => {
  routes["/api/v1/nodes/1/service-schemas"] = schemas({
    kind: "wireguard", version: "1", schema: wgSchema, offerable: true,
    hot_user_add: false, requires_pki: false,
  });
  routes["/api/v1/nodes/1/services"] = { body: { services: [] } };

  renderWithQuery(<InboundStudio nodeId={1} />);
  await userEvent.setup().click(await screen.findByRole("button", { name: "Add inbound" }));

  const select = screen.getByLabelText("Protocol") as HTMLSelectElement;
  const offered = Array.from(select.options).map((o) => o.value);
  expect(offered).toEqual(["wireguard"]);
  // xray and singbox are protocols the panel knows; the node does not run
  // them, so they must be ABSENT rather than present and disabled.
  expect(offered).not.toContain("xray");
});

// A node that has never connected reports nothing. Saying so beats an empty
// form the operator cannot explain.
it("explains a node that has reported nothing instead of showing an empty picker", async () => {
  routes["/api/v1/nodes/1/service-schemas"] = schemas();
  routes["/api/v1/nodes/1/services"] = { body: { services: [] } };

  renderWithQuery(<InboundStudio nodeId={1} />);
  await screen.findByText(/has not reported which protocols/i);
  expect(screen.queryByRole("button", { name: "Add inbound" })).toBeNull();
});

// A protocol the node runs but cannot describe is NAMED, not silently dropped:
// otherwise an operator sees it running and no way to add one, with nothing to
// explain the gap.
it("names a protocol the node runs but cannot describe", async () => {
  routes["/api/v1/nodes/1/service-schemas"] = schemas({
    kind: "wireguard", version: "1", schema: {}, offerable: false,
    reason: "this node reported no service schema for wireguard; upgrade the agent",
    hot_user_add: false, requires_pki: false,
  });
  routes["/api/v1/nodes/1/services"] = { body: { services: [] } };

  renderWithQuery(<InboundStudio nodeId={1} />);
  expect((await screen.findByRole("status")).textContent).toContain("upgrade the agent");
  expect(screen.queryByRole("button", { name: "Add inbound" })).toBeNull();
});

// ------------------------------------------------------------ the form

it("builds the form from the schema and submits typed values", async () => {
  routes["/api/v1/nodes/1/service-schemas"] = schemas({
    kind: "wireguard", version: "1", schema: wgSchema, offerable: true,
    hot_user_add: false, requires_pki: false,
  });
  routes["/api/v1/nodes/1/services"] = { body: { services: [] } };
  routes["POST /api/v1/nodes/1/services"] = { status: 201, body: { id: 1 } };

  const user = userEvent.setup();
  renderWithQuery(<InboundStudio nodeId={1} />);
  await user.click(await screen.findByRole("button", { name: "Add inbound" }));

  await user.type(screen.getByLabelText(/^port/), "51820");
  await user.type(screen.getByLabelText(/^private_key/), "abc=");
  await user.click(screen.getByRole("button", { name: "Create" }));

  await waitFor(() => {
    const post = calls.find((c) => c.method === "POST");
    expect(post).toBeDefined();
    const body = post?.body as { adapter_kind: string; params: Record<string, unknown> };
    expect(body.adapter_kind).toBe("wireguard");
    // A number field must submit a NUMBER. Sending "51820" would be refused by
    // the schema while the field looked correct.
    expect(body.params.port).toBe(51820);
    expect(typeof body.params.port).toBe("number");
    expect(body.params.private_key).toBe("abc=");
    // An untouched optional field is absent, not an empty string the schema
    // would refuse.
    expect("subnet" in body.params).toBe(false);
  });
});

// An enum is a closed set, which is where "unsupported options are absent"
// comes for free: an option the adapter did not publish cannot be chosen.
it("renders an enum as a closed choice", async () => {
  routes["/api/v1/nodes/1/service-schemas"] = schemas({
    kind: "wireguard", version: "1", schema: wgSchema, offerable: true,
    hot_user_add: false, requires_pki: false,
  });
  routes["/api/v1/nodes/1/services"] = { body: { services: [] } };

  const user = userEvent.setup();
  renderWithQuery(<InboundStudio nodeId={1} />);
  await user.click(await screen.findByRole("button", { name: "Add inbound" }));

  const obfs = screen.getByLabelText(/^obfs/) as HTMLSelectElement;
  const values = Array.from(obfs.options).map((o) => o.value);
  expect(values).toContain("salamander");
  expect(values).not.toContain("xhttp"); // never published, so never offerable
});

// ------------------------------------------------------------ JSON mode

// JSON mode is not a way round validation. It carries the document across, and
// what it submits goes down the same path as the form.
it("carries the document between the form and JSON mode", async () => {
  routes["/api/v1/nodes/1/service-schemas"] = schemas({
    kind: "wireguard", version: "1", schema: wgSchema, offerable: true,
    hot_user_add: false, requires_pki: false,
  });
  routes["/api/v1/nodes/1/services"] = { body: { services: [] } };

  const user = userEvent.setup();
  renderWithQuery(<InboundStudio nodeId={1} />);
  await user.click(await screen.findByRole("button", { name: "Add inbound" }));
  await user.type(screen.getByLabelText(/^port/), "51820");
  await user.click(screen.getByRole("button", { name: "Edit as JSON" }));

  const box = screen.getByLabelText("Parameters document") as HTMLTextAreaElement;
  expect(JSON.parse(box.value).port).toBe(51820);
});

it("refuses to submit unparseable JSON and says so", async () => {
  routes["/api/v1/nodes/1/service-schemas"] = schemas({
    kind: "wireguard", version: "1", schema: wgSchema, offerable: true,
    hot_user_add: false, requires_pki: false,
  });
  routes["/api/v1/nodes/1/services"] = { body: { services: [] } };

  const user = userEvent.setup();
  renderWithQuery(<InboundStudio nodeId={1} />);
  await user.click(await screen.findByRole("button", { name: "Add inbound" }));
  await user.click(screen.getByRole("button", { name: "Edit as JSON" }));

  const box = screen.getByLabelText("Parameters document");
  await user.clear(box);
  await user.type(box, "not json at all");
  await user.click(screen.getByRole("button", { name: "Create" }));

  await screen.findByRole("alert");
  expect(calls.some((c) => c.method === "POST")).toBe(false);
});

// The control plane's refusal is shown verbatim. There is deliberately no
// second validator in the browser, so the panel's answer is the only one -- and
// hiding or rewording it would leave the operator guessing.
it("shows the panel's schema refusal verbatim", async () => {
  routes["/api/v1/nodes/1/service-schemas"] = schemas({
    kind: "wireguard", version: "1", schema: wgSchema, offerable: true,
    hot_user_add: false, requires_pki: false,
  });
  routes["/api/v1/nodes/1/services"] = { body: { services: [] } };
  routes["POST /api/v1/nodes/1/services"] = {
    status: 422,
    body: { error: { code: "validation", message: "missing property 'private_key'" } },
  };

  const user = userEvent.setup();
  renderWithQuery(<InboundStudio nodeId={1} />);
  await user.click(await screen.findByRole("button", { name: "Add inbound" }));
  await user.click(screen.getByRole("button", { name: "Edit as JSON" }));
  await user.click(screen.getByRole("button", { name: "Create" }));

  expect((await screen.findByRole("alert")).textContent).toContain("private_key");
});

// Switching protocol must not carry one adapter's fields into another's
// submission: the schema refuses them and it reads as a bug rather than a
// mistake.
it("clears params when the protocol changes", async () => {
  routes["/api/v1/nodes/1/service-schemas"] = schemas(
    { kind: "wireguard", version: "1", schema: wgSchema, offerable: true, hot_user_add: false, requires_pki: false },
    { kind: "hysteria2", version: "1", offerable: true, hot_user_add: false, requires_pki: true,
      schema: { type: "object", required: ["password"], properties: { password: { type: "string" } } } },
  );
  routes["/api/v1/nodes/1/services"] = { body: { services: [] } };
  routes["POST /api/v1/nodes/1/services"] = { status: 201, body: { id: 1 } };

  const user = userEvent.setup();
  renderWithQuery(<InboundStudio nodeId={1} />);
  await user.click(await screen.findByRole("button", { name: "Add inbound" }));
  await user.type(screen.getByLabelText(/^port/), "51820");

  await user.selectOptions(screen.getByLabelText("Protocol"), "hysteria2");
  await user.type(screen.getByLabelText(/^password/), "supersecret");
  await user.click(screen.getByRole("button", { name: "Create" }));

  await waitFor(() => {
    const post = calls.find((c) => c.method === "POST");
    const body = post?.body as { adapter_kind: string; params: Record<string, unknown> };
    expect(body.adapter_kind).toBe("hysteria2");
    expect("port" in body.params).toBe(false);
  });
});

// ------------------------------------------------------------ grouping

describe("progressive disclosure", () => {
  // Required fields are basic whatever they are called: burying one behind a
  // disclosure produces a form that cannot be submitted and does not say why.
  it("puts required fields in the basics regardless of name", () => {
    expect(groupOf("private_key", true)).toBe("basic");
    expect(groupOf("port", true)).toBe("basic");
  });

  it("classifies optional fields by what they configure", () => {
    expect(groupOf("tls_cert", false)).toBe("security");
    expect(groupOf("reality_key", false)).toBe("security");
    expect(groupOf("mtu", false)).toBe("transport");
    expect(groupOf("ws_path", false)).toBe("transport");
  });

  // A field the heuristic does not recognise must still be REACHABLE. A
  // misgrouped field is an annoyance; a field the operator cannot reach is a
  // protocol they cannot configure.
  it("keeps an unrecognised field visible in its own group", () => {
    expect(groupOf("some_future_option", false)).toBe("other");
    const groups = groupsOf({
      type: "object",
      required: ["port"],
      properties: { port: { type: "integer" }, some_future_option: { type: "string" } },
    });
    expect(groups).toContain("other");
  });

  it("lists groups in disclosure order and omits empty ones", () => {
    const groups = groupsOf({
      type: "object",
      required: ["port"],
      properties: {
        port: { type: "integer" },
        tls_cert: { type: "string" },
        mtu: { type: "integer" },
      },
    });
    expect(groups).toEqual(["basic", "transport", "security"]);
  });
});

// ------------------------------------------------ deleting an inbound (A2)

// window.confirm could not be tested at all without stubbing a global, and its
// buttons were the browser's -- so an Arabic panel showed an English "OK".
// These cover what replaced it.

function oneService() {
  routes["/api/v1/nodes/1/service-schemas"] = schemas({
    kind: "wireguard", version: "1", schema: wgSchema, offerable: true,
    hot_user_add: false, requires_pki: false,
  });
  routes["/api/v1/nodes/1/services"] = {
    body: {
      services: [
        { id: 7, node_id: 1, adapter_kind: "wireguard", params: {}, enabled: true, created_at: 1 },
      ],
    },
  };
  routes["DELETE /api/v1/services/7"] = { status: 204, body: {} };
}

it("asks before deleting an inbound, and sends nothing until confirmed", async () => {
  oneService();
  const user = userEvent.setup();
  renderWithQuery(<InboundStudio nodeId={1} />);

  await user.click(await screen.findByRole("button", { name: "Delete" }));

  // The question is a dialog, and it names what is about to be removed rather
  // than asking a bare "are you sure" next to a table.
  const dialog = await screen.findByRole("dialog");
  expect(dialog).toHaveTextContent("wireguard");
  // §77: the disruption is stated BEFORE the click, not discovered after it.
  expect(dialog).toHaveTextContent(/is disconnected/i);
  expect(calls.some((c) => c.method === "DELETE")).toBe(false);
});

it("deletes only after the operator confirms", async () => {
  oneService();
  const user = userEvent.setup();
  renderWithQuery(<InboundStudio nodeId={1} />);

  await user.click(await screen.findByRole("button", { name: "Delete" }));
  const dialog = await screen.findByRole("dialog");
  await user.click(within(dialog).getByRole("button", { name: "Delete" }));

  await waitFor(() =>
    expect(calls.some((c) => c.method === "DELETE" && c.path === "/api/v1/services/7")).toBe(true),
  );
});

it("sends nothing when the operator cancels", async () => {
  oneService();
  const user = userEvent.setup();
  renderWithQuery(<InboundStudio nodeId={1} />);

  await user.click(await screen.findByRole("button", { name: "Delete" }));
  const dialog = await screen.findByRole("dialog");
  await user.click(within(dialog).getByRole("button", { name: "Cancel" }));

  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(calls.some((c) => c.method === "DELETE")).toBe(false);
});

// Escape must dismiss it. A modal that traps an operator with no keyboard way
// out is the accessibility failure §65 is about, and it is the one thing the
// browser dialog did get right.
it("closes on Escape without deleting", async () => {
  oneService();
  const user = userEvent.setup();
  renderWithQuery(<InboundStudio nodeId={1} />);

  await user.click(await screen.findByRole("button", { name: "Delete" }));
  await screen.findByRole("dialog");
  await user.keyboard("{Escape}");

  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(calls.some((c) => c.method === "DELETE")).toBe(false);
});

// The property window.confirm could never have: the buttons are in the
// operator's language.
it("asks in the operator's language", async () => {
  setLocale("fa");
  oneService();
  const user = userEvent.setup();
  renderWithQuery(<InboundStudio nodeId={1} />);

  await user.click(await screen.findByRole("button", { name: "حذف" }));
  const dialog = await screen.findByRole("dialog");
  expect(within(dialog).getByRole("button", { name: "لغو" })).toBeInTheDocument();
  setLocale("en");
});
