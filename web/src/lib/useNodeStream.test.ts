import { describe, expect, it } from "vitest";
import { indexNodes, parseNodeEvent } from "./useNodeStream";

describe("parseNodeEvent", () => {
  it("indexes node status by id", () => {
    const parsed = parseNodeEvent(
      JSON.stringify({
        nodes: [
          {
            id: 1,
            status: "online",
            online: true,
            desired_revision: 3,
            applied_revision: 3,
            drift: false,
          },
          {
            id: 2,
            status: "degraded",
            online: true,
            desired_revision: 5,
            applied_revision: 4,
            drift: true,
          },
        ],
      }),
    );
    expect(parsed[1].status).toBe("online");
    expect(parsed[2].drift).toBe(true);
  });

  it("returns an empty map for malformed payloads rather than throwing", () => {
    expect(parseNodeEvent("not json")).toEqual({});
    expect(parseNodeEvent(JSON.stringify({ nodes: null }))).toEqual({});
  });
});

describe("polling fallback", () => {
  // GET /api/v1/nodes is what the fallback reads, and its DTO has no drift
  // field: the server derives drift only on the stream. If the two feeds
  // disagreed, the same node would render differently depending on which one
  // happened to be alive, which is exactly the bug the fallback exists to
  // avoid. So the derived map must equal the parsed frame, field for field.
  const streamFrame = {
    nodes: [
      {
        id: 1,
        name: "de-1",
        status: "online",
        online: true,
        desired_revision: 3,
        applied_revision: 3,
        drift: false,
      },
      {
        id: 2,
        name: "de-2",
        status: "degraded",
        online: true,
        desired_revision: 5,
        applied_revision: 4,
        drift: true,
      },
    ],
  };

  // The same two nodes as GET /api/v1/nodes reports them: extra fields the
  // stream does not carry, and no drift at all.
  const listBody = {
    nodes: [
      {
        id: 1,
        name: "de-1",
        address: "1.2.3.4",
        status: "online",
        online: true,
        desired_revision: 3,
        applied_revision: 3,
        last_seen_at: 1700000000,
      },
      {
        id: 2,
        name: "de-2",
        address: "5.6.7.8",
        status: "degraded",
        online: true,
        desired_revision: 5,
        applied_revision: 4,
        last_seen_at: null,
      },
    ],
  };

  it("derives the same map from the node list as from the equivalent stream frame", () => {
    expect(indexNodes(listBody.nodes)).toEqual(parseNodeEvent(JSON.stringify(streamFrame)));
  });

  it("derives drift from the revisions when the payload omits it", () => {
    const derived = indexNodes(listBody.nodes);
    expect(derived[1].drift).toBe(false);
    expect(derived[2].drift).toBe(true);
  });

  it("returns an empty map when the node list has no nodes array", () => {
    expect(indexNodes(undefined)).toEqual({});
    expect(indexNodes(null)).toEqual({});
  });
});
