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

// ------------------------------------------- the workflow, end to end

// The studio could list, create and delete. An operator could not change an
// inbound's port without deleting it and retyping every field, could not turn
// one off for maintenance, and could not act on more than one at a time.

const wgAdapter = {
  kind: "wireguard", version: "1", schema: wgSchema, offerable: true,
  hot_user_add: true, requires_pki: false,
};

function twoInbounds() {
  return {
    body: {
      services: [
        {
          id: 11, node_id: 1, adapter_kind: "wireguard", enabled: true,
          params: { port: 51820, private_key: "k1" }, created_at: 1,
        },
        {
          id: 12, node_id: 1, adapter_kind: "wireguard", enabled: false,
          params: { port: 51821, private_key: "k2" }, created_at: 2,
        },
      ],
    },
  };
}

function seedStudio() {
  routes["/api/v1/nodes/1/service-schemas"] = schemas(wgAdapter);
  routes["/api/v1/nodes/1/services"] = twoInbounds();
}

describe("editing an inbound", () => {
  it("loads the existing params into the form and PUTs the change", async () => {
    seedStudio();
    routes["PUT /api/v1/services/11"] = { body: {} };
    const user = userEvent.setup({ delay: null });
    renderWithQuery(<InboundStudio nodeId={1} />);
    await screen.findAllByText("wireguard");

    await user.click(screen.getAllByRole("button", { name: "Edit" })[0]);

    // Pre-filled from the inbound being edited, not blank: retyping every
    // field to change a port is how an operator loses a private key.
    const port = await screen.findByLabelText(/port/i);
    expect(port).toHaveValue(51820);
    await user.clear(port);
    await user.type(port, "51999");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "PUT" && c.path === "/api/v1/services/11");
      expect(sent?.body).toMatchObject({
        adapter_kind: "wireguard",
        params: { port: 51999, private_key: "k1" },
        // Carried through: the handler rewrites the whole row, so omitting it
        // would silently re-enable an inbound the operator had turned off.
        enabled: true,
      });
    });
  });

  // Changing the protocol would keep the id and swap the adapter under it,
  // which is a different inbound wearing the old one's identity.
  it("will not let the protocol be changed on an existing inbound", async () => {
    seedStudio();
    const user = userEvent.setup({ delay: null });
    renderWithQuery(<InboundStudio nodeId={1} />);
    await screen.findAllByText("wireguard");

    await user.click(screen.getAllByRole("button", { name: "Edit" })[0]);
    expect(await screen.findByLabelText("Protocol")).toBeDisabled();
  });

  it("keeps a disabled inbound disabled when it is edited", async () => {
    seedStudio();
    routes["PUT /api/v1/services/12"] = { body: {} };
    const user = userEvent.setup({ delay: null });
    renderWithQuery(<InboundStudio nodeId={1} />);
    await screen.findAllByText("wireguard");

    await user.click(screen.getAllByRole("button", { name: "Edit" })[1]);
    await user.click(await screen.findByRole("button", { name: "Save" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "PUT" && c.path === "/api/v1/services/12");
      expect(sent?.body).toMatchObject({ enabled: false });
    });
  });
});

describe("cloning an inbound", () => {
  // A blind clone would POST a copy that binds the same port and be refused
  // nearly every time. Pre-filling lets the operator change what must differ.
  it("opens a create form pre-filled from the source", async () => {
    seedStudio();
    const user = userEvent.setup({ delay: null });
    renderWithQuery(<InboundStudio nodeId={1} />);
    await screen.findAllByText("wireguard");

    await user.click(screen.getAllByRole("button", { name: "Clone" })[0]);

    expect(await screen.findByLabelText(/port/i)).toHaveValue(51820);
    // A create, not an edit: nothing has been sent yet.
    expect(calls.some((c) => c.method === "PUT" || c.method === "POST")).toBe(false);
    expect(screen.getByRole("button", { name: "Create" })).toBeInTheDocument();
  });

  it("POSTs a new inbound rather than overwriting the original", async () => {
    seedStudio();
    routes["POST /api/v1/nodes/1/services"] = { status: 201, body: { id: 13 } };
    const user = userEvent.setup({ delay: null });
    renderWithQuery(<InboundStudio nodeId={1} />);
    await screen.findAllByText("wireguard");

    await user.click(screen.getAllByRole("button", { name: "Clone" })[0]);
    const port = await screen.findByLabelText(/port/i);
    await user.clear(port);
    await user.type(port, "51888");
    await user.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "POST");
      expect(sent?.path).toBe("/api/v1/nodes/1/services");
      expect(sent?.body).toMatchObject({ params: { port: 51888, private_key: "k1" } });
    });
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
  });
});

describe("enable and disable", () => {
  // Disabling goes through the same PUT, which republishes the node. A row
  // that changed without republishing would leave the inbound serving.
  it("turns a running inbound off", async () => {
    seedStudio();
    routes["PUT /api/v1/services/11"] = { body: {} };
    const user = userEvent.setup({ delay: null });
    renderWithQuery(<InboundStudio nodeId={1} />);
    await screen.findAllByText("wireguard");

    await user.click(screen.getAllByRole("button", { name: "Disable" })[0]);

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "PUT" && c.path === "/api/v1/services/11");
      expect(sent?.body).toMatchObject({ enabled: false, params: { port: 51820 } });
    });
  });

  it("turns a stopped inbound back on", async () => {
    seedStudio();
    routes["PUT /api/v1/services/12"] = { body: {} };
    const user = userEvent.setup({ delay: null });
    renderWithQuery(<InboundStudio nodeId={1} />);
    await screen.findAllByText("wireguard");

    await user.click(screen.getByRole("button", { name: "Enable" }));

    await waitFor(() => {
      const sent = calls.find((c) => c.method === "PUT" && c.path === "/api/v1/services/12");
      expect(sent?.body).toMatchObject({ enabled: true });
    });
  });
});

describe("bulk actions", () => {
  it("offers no bulk bar until an inbound is selected", async () => {
    seedStudio();
    renderWithQuery(<InboundStudio nodeId={1} />);
    await screen.findAllByText("wireguard");
    expect(screen.queryByText(/selected/)).toBeNull();
  });

  it("applies to exactly the selected inbounds", async () => {
    seedStudio();
    routes["PUT /api/v1/services/11"] = { body: {} };
    routes["PUT /api/v1/services/12"] = { body: {} };
    const user = userEvent.setup({ delay: null });
    renderWithQuery(<InboundStudio nodeId={1} />);
    await screen.findAllByText("wireguard");

    await user.click(screen.getByRole("checkbox", { name: "wireguard 11" }));
    const bar = (await screen.findByText("1 selected")).closest("div")!;

    // The bulk bar's Disable, not the row's.
    await user.click(within(bar).getByRole("button", { name: "Disable" }));

    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT" && c.path === "/api/v1/services/11")).toBe(true),
    );
    expect(calls.some((c) => c.path === "/api/v1/services/12")).toBe(false);
  });

  it("selects every inbound at once", async () => {
    seedStudio();
    routes["PUT /api/v1/services/11"] = { body: {} };
    routes["PUT /api/v1/services/12"] = { body: {} };
    const user = userEvent.setup({ delay: null });
    renderWithQuery(<InboundStudio nodeId={1} />);
    await screen.findAllByText("wireguard");

    await user.click(screen.getByRole("checkbox", { name: "Select all rows" }));
    const bar = (await screen.findByText("2 selected")).closest("div")!;
    await user.click(within(bar).getByRole("button", { name: "Enable" }));

    await waitFor(() => {
      expect(calls.filter((c) => c.method === "PUT").length).toBe(2);
    });
  });

  // These run one request per inbound, and a batch where some failed is the
  // normal outcome when one has a stale port. "Done" would hide it.
  it("reports how many actually changed", async () => {
    seedStudio();
    routes["PUT /api/v1/services/11"] = { body: {} };
    routes["PUT /api/v1/services/12"] = {
      status: 422, body: { error: { code: "validation", message: "port already bound" } },
    };
    const user = userEvent.setup({ delay: null });
    renderWithQuery(<InboundStudio nodeId={1} />);
    await screen.findAllByText("wireguard");

    await user.click(screen.getByRole("checkbox", { name: "Select all rows" }));
    const bar = (await screen.findByText("2 selected")).closest("div")!;
    await user.click(within(bar).getByRole("button", { name: "Enable" }));

    const status = await screen.findByRole("status");
    expect(status).toHaveTextContent("1 changed, 1 failed");
    expect(status).toHaveTextContent("port already bound");
  });

  it("asks before deleting a batch and says how many", async () => {
    seedStudio();
    routes["DELETE /api/v1/services/11"] = { body: {} };
    const user = userEvent.setup({ delay: null });
    renderWithQuery(<InboundStudio nodeId={1} />);
    await screen.findAllByText("wireguard");

    await user.click(screen.getByRole("checkbox", { name: "wireguard 11" }));
    const bar = (await screen.findByText("1 selected")).closest("div")!;
    await user.click(within(bar).getByRole("button", { name: "Delete" }));

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("This affects 1 inbounds.");
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);

    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "DELETE" && c.path === "/api/v1/services/11")).toBe(true),
    );
  });
});
