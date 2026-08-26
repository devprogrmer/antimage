import { describe, expect, it } from "vitest";

import { searchParamsFor } from "./SubjectFilters";
import type { FilterParams } from "./SubjectFilters";

// SubjectFilters and /api/v2/subjects were both written and never connected:
// the bar was imported by nothing, and the endpoint was called by nothing. The
// query string is the joint between them, so it is what these cover.

const empty: FilterParams = {
  search: "",
  status: "",
  trafficMin: "",
  trafficMax: "",
  quotaStatus: "",
  expiresBefore: "",
  expiresAfter: "",
  sort: "",
  order: "",
};

describe("searchParamsFor", () => {
  it("sends nothing when nothing is filtered", () => {
    expect(searchParamsFor(empty)).toBe("");
    expect(searchParamsFor(null)).toBe("");
  });

  it("uses the parameter names the handler actually reads", () => {
    // These are the strings in subjects_search.go. A camelCase key here would
    // be silently ignored server-side and the operator would see an unfiltered
    // list with the filter bar showing their choice.
    const params = new URLSearchParams(
      searchParamsFor({
        ...empty,
        search: "alice",
        status: "active",
        trafficMin: "100",
        trafficMax: "200",
        quotaStatus: "over_limit",
        expiresBefore: "2026-12-31",
        expiresAfter: "2026-01-01",
        sort: "created",
        order: "desc",
      }),
    );
    expect(params.get("search")).toBe("alice");
    expect(params.get("status")).toBe("active");
    expect(params.get("traffic_min")).toBe("100");
    expect(params.get("traffic_max")).toBe("200");
    expect(params.get("quota_status")).toBe("over_limit");
    expect(params.get("expires_before")).toBe("2026-12-31");
    expect(params.get("expires_after")).toBe("2026-01-01");
    expect(params.get("sort")).toBe("created");
    expect(params.get("order")).toBe("desc");
  });

  it("omits the filters that were left alone", () => {
    const params = new URLSearchParams(searchParamsFor({ ...empty, search: "bob" }));
    expect(params.get("search")).toBe("bob");
    expect(params.has("status")).toBe(false);
    expect(params.has("quota_status")).toBe(false);
  });

  it("escapes a search term rather than splicing it into the URL", () => {
    const params = new URLSearchParams(
      searchParamsFor({ ...empty, search: "a&b=c d" }),
    );
    expect(params.get("search")).toBe("a&b=c d");
  });
});
