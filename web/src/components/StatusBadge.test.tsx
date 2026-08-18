import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StatusBadge } from "./StatusBadge";

describe("StatusBadge", () => {
  it("renders every node status with a label, not colour alone", () => {
    for (const status of [
      "pending",
      "enrolling",
      "online",
      "degraded",
      "integrity",
      "offline",
      "disabled",
    ] as const) {
      const { unmount } = render(<StatusBadge status={status} />);
      // Accessibility: colour must never be the only signal.
      expect(screen.getByRole("status").textContent?.trim()).not.toBe("");
      unmount();
    }
  });

  it("marks integrity faults as alerts", () => {
    render(<StatusBadge status="integrity" />);
    expect(screen.getByRole("status")).toHaveAttribute("data-severity", "alert");
  });

  it("falls back gracefully on an unknown status", () => {
    render(<StatusBadge status={"martian" as never} />);
    expect(screen.getByRole("status").textContent).toContain("martian");
  });
});
