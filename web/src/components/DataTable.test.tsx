import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { DataTable } from "./DataTable";
import type { Column } from "./DataTable";
import { setLocale } from "../i18n";

interface Row {
  id: number;
  name: string;
  size: number;
  seen: number | null;
}

const rows: Row[] = [
  { id: 1, name: "charlie", size: 30, seen: 300 },
  { id: 2, name: "alpha", size: 10, seen: null },
  { id: 3, name: "bravo", size: 20, seen: 100 },
];

const columns: Column<Row>[] = [
  { id: "name", header: "Name", sortValue: (r) => r.name, hideable: false, cell: (r) => r.name },
  { id: "size", header: "Size", sortValue: (r) => r.size, cell: (r) => String(r.size) },
  { id: "seen", header: "Seen", sortValue: (r) => r.seen, cell: (r) => String(r.seen) },
  { id: "note", header: "Note", cell: () => "-" },
];

function bodyNames() {
  const body = screen.getAllByRole("rowgroup")[1];
  return within(body)
    .getAllByRole("row")
    .map((r) => within(r).getAllByRole("cell")[0].textContent);
}

beforeEach(() => {
  setLocale("en");
  localStorage.clear();
});
afterEach(() => localStorage.clear());

describe("sorting", () => {
  it("leaves the server's order alone until a column is chosen", () => {
    render(<DataTable rows={rows} columns={columns} rowKey={(r) => r.id} empty="none" />);
    expect(bodyNames()).toEqual(["charlie", "alpha", "bravo"]);
  });

  it("sorts ascending, then descending, then back to the original order", async () => {
    const user = userEvent.setup();
    render(<DataTable rows={rows} columns={columns} rowKey={(r) => r.id} empty="none" />);
    const header = screen.getByRole("button", { name: /Name/ });

    await user.click(header);
    expect(bodyNames()).toEqual(["alpha", "bravo", "charlie"]);

    await user.click(header);
    expect(bodyNames()).toEqual(["charlie", "bravo", "alpha"]);

    // Third click returns to what the server sent, so an operator can undo a
    // sort without reloading.
    await user.click(header);
    expect(bodyNames()).toEqual(["charlie", "alpha", "bravo"]);
  });

  it("sorts numbers numerically rather than as text", async () => {
    const user = userEvent.setup();
    const wide: Row[] = [
      { id: 1, name: "a", size: 9, seen: null },
      { id: 2, name: "b", size: 100, seen: null },
      { id: 3, name: "c", size: 20, seen: null },
    ];
    render(<DataTable rows={wide} columns={columns} rowKey={(r) => r.id} empty="none" />);
    await user.click(screen.getByRole("button", { name: /Size/ }));
    // Lexicographically this would be 100, 20, 9.
    expect(bodyNames()).toEqual(["a", "c", "b"]);
  });

  it("keeps unknown values last in both directions", async () => {
    const user = userEvent.setup();
    render(<DataTable rows={rows} columns={columns} rowKey={(r) => r.id} empty="none" />);
    const header = screen.getByRole("button", { name: /Seen/ });

    await user.click(header);
    expect(bodyNames()[2]).toBe("alpha");

    await user.click(header);
    // "Never seen" is not the smallest value -- it is not a value, so it does
    // not lead the list when the order flips.
    expect(bodyNames()[2]).toBe("alpha");
  });

  it("announces the sort state to assistive technology", async () => {
    const user = userEvent.setup();
    render(<DataTable rows={rows} columns={columns} rowKey={(r) => r.id} empty="none" />);
    const name = screen.getByRole("columnheader", { name: /Name/ });
    expect(name).not.toHaveAttribute("aria-sort");

    await user.click(within(name).getByRole("button"));
    expect(name).toHaveAttribute("aria-sort", "ascending");
    await user.click(within(name).getByRole("button"));
    expect(name).toHaveAttribute("aria-sort", "descending");
  });

  it("does not reorder the array it was given", async () => {
    const user = userEvent.setup();
    const original = [...rows];
    render(<DataTable rows={rows} columns={columns} rowKey={(r) => r.id} empty="none" />);
    await user.click(screen.getByRole("button", { name: /Name/ }));
    expect(rows).toEqual(original);
  });
});

describe("column visibility", () => {
  it("hides a column and remembers it", async () => {
    const user = userEvent.setup();
    const view = render(
      <DataTable
        rows={rows}
        columns={columns}
        rowKey={(r) => r.id}
        storageKey="test"
        empty="none"
      />,
    );
    expect(screen.getByRole("columnheader", { name: /Size/ })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Columns" }));
    await user.click(await screen.findByRole("menuitemcheckbox", { name: "Size" }));
    expect(screen.queryByRole("columnheader", { name: /Size/ })).toBeNull();

    await user.keyboard("{Escape}");
    view.unmount();

    render(
      <DataTable
        rows={rows}
        columns={columns}
        rowKey={(r) => r.id}
        storageKey="test"
        empty="none"
      />,
    );
    expect(screen.queryByRole("columnheader", { name: /Size/ })).toBeNull();
  });

  it("will not offer to hide a column the row cannot be identified without", async () => {
    const user = userEvent.setup();
    render(
      <DataTable rows={rows} columns={columns} rowKey={(r) => r.id} storageKey="t" empty="none" />,
    );
    await user.click(screen.getByRole("button", { name: "Columns" }));
    expect(screen.queryByRole("menuitemcheckbox", { name: "Name" })).toBeNull();
    expect(await screen.findByRole("menuitemcheckbox", { name: "Size" })).toBeInTheDocument();
  });

  it("shows every column when the stored preference is corrupt", () => {
    localStorage.setItem("antimage.table.hidden.test", "not json");
    render(
      <DataTable rows={rows} columns={columns} rowKey={(r) => r.id} storageKey="test" empty="none" />,
    );
    expect(screen.getByRole("columnheader", { name: /Size/ })).toBeInTheDocument();
  });
});

describe("row activation", () => {
  // The tables this replaced put onClick on the row and offered no keyboard
  // path at all, so a list could be read without a mouse and not opened.
  it("activates a row with Enter and with Space", async () => {
    const user = userEvent.setup();
    const onRowActivate = vi.fn();
    render(
      <DataTable
        rows={rows}
        columns={columns}
        rowKey={(r) => r.id}
        onRowActivate={onRowActivate}
        empty="none"
      />,
    );
    const body = screen.getAllByRole("rowgroup")[1];
    const first = within(body).getAllByRole("row")[0];

    first.focus();
    expect(first).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(onRowActivate).toHaveBeenCalledWith(rows[0]);

    await user.keyboard(" ");
    expect(onRowActivate).toHaveBeenCalledTimes(2);
  });

  it("is not focusable when there is nothing to activate", () => {
    render(<DataTable rows={rows} columns={columns} rowKey={(r) => r.id} empty="none" />);
    const body = screen.getAllByRole("rowgroup")[1];
    expect(within(body).getAllByRole("row")[0]).not.toHaveAttribute("tabindex");
  });
});

describe("empty state", () => {
  it("says so rather than rendering an empty grid", () => {
    render(<DataTable rows={[]} columns={columns} rowKey={(r) => r.id} empty="No nodes yet." />);
    expect(screen.getByText("No nodes yet.")).toBeInTheDocument();
  });
});
