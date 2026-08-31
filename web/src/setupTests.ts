// The /vitest entry point registers the matchers with vitest's expect and
// augments its Assertion type, so toHaveAttribute type-checks under tsc -b.
import "@testing-library/jest-dom/vitest";

import { cleanup, configure } from "@testing-library/react";
import { afterEach } from "vitest";

// Testing Library gives every findBy/waitFor one second. That is ample for a
// single file and not ample for the whole suite in parallel workers: opening a
// Radix menu and settling a React Query mutation can take most of a second
// each on a loaded machine, so a test would fail on the clock while asserting
// something that was about to be true. Raising the budget changes no
// assertion -- a genuinely wrong expectation still fails, it just takes longer
// to say so.
configure({ asyncUtilTimeout: 5000 });

// Testing Library only auto-registers its own afterEach when vitest runs with
// globals enabled, which this project does not: every test imports what it
// uses. Without this, a component left mounted by one test is still in the
// document for the next, and getByRole throws "found multiple elements".
afterEach(cleanup);

// Radix locks scroll while a modal is open by setting pointer-events:none and
// overflow:hidden on <body>, and it undoes that on close -- but a test that
// ends with a dialog or menu still open never reaches the close. cleanup()
// unmounts the React tree and does not touch body styles, so the lock survives
// into the next test, where userEvent finds the whole document unclickable and
// every click silently does nothing. The failure surfaces as an assertion about
// a request that was never sent, several tests away from the one that caused it.
afterEach(() => {
  document.body.style.pointerEvents = "";
  document.body.style.overflow = "";
  document.body.removeAttribute("data-scroll-locked");
  document.body.removeAttribute("aria-hidden");
});

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

// jsdom implements no ResizeObserver either, and cmdk (the command palette)
// observes its list to keep the active item scrolled into view. Same class of
// gap as matchMedia above: a missing browser API that surfaces as a component
// crash rather than as the missing API it is.
if (typeof window !== "undefined" && !window.ResizeObserver) {
  window.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
}

// And jsdom implements no scrollIntoView. cmdk calls it to keep the highlighted
// command visible as the arrow keys move down the list.
if (typeof Element !== "undefined" && !Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = function scrollIntoView() {};
}
