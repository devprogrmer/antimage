// The /vitest entry point registers the matchers with vitest's expect and
// augments its Assertion type, so toHaveAttribute type-checks under tsc -b.
import "@testing-library/jest-dom/vitest";

import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// Testing Library only auto-registers its own afterEach when vitest runs with
// globals enabled, which this project does not: every test imports what it
// uses. Without this, a component left mounted by one test is still in the
// document for the next, and getByRole throws "found multiple elements".
afterEach(cleanup);

// jsdom implements no matchMedia, and the theme provider asks it whether the
// OS prefers dark. Without this every component that renders inside
// ThemeProvider throws on mount, which reads as a component bug rather than a
// missing browser API.
//
// Defaults to "light" so a test that does not care gets a stable answer;
// setSystemPrefersDark lets one that does care state it.
let systemDark = false;

export function setSystemPrefersDark(value: boolean) {
  systemDark = value;
}

if (typeof window !== "undefined" && !window.matchMedia) {
  window.matchMedia = (query: string): MediaQueryList =>
    ({
      matches: query.includes("prefers-color-scheme: dark") ? systemDark : false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList;
}
