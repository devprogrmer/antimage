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
