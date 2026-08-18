import { useEffect, useState } from "react";
import { api } from "./api";

export interface NodeStatusUpdate {
  id: number;
  status: string;
  online: boolean;
  desired_revision: number;
  applied_revision: number;
  drift: boolean;
}

/** The wire shape shared by the two feeds that can drive this hook.
 *
 *  The SSE snapshot (`event: nodes`) precomputes `drift`; `GET /api/v1/nodes`
 *  does not, because its DTO is the node summary and drift is a derived view
 *  concern. Making it optional here is what lets one projection serve both. */
interface NodeWire {
  id: number;
  status: string;
  online: boolean;
  desired_revision: number;
  applied_revision: number;
  drift?: boolean;
}

/** How long to wait between polls once the stream has been given up on. */
const pollIntervalMs = 5000;

/** How many consecutive poll failures are tolerated before the fallback stops.
 *  A blip must not kill the fallback, and a panel that is genuinely gone must
 *  not be requested forever, so the retry budget is small and finite. */
const maxPollFailures = 5;

/** Projects one wire node onto the update shape.
 *
 *  Drift is derived exactly as the server derives it — applied behind desired
 *  — so a payload that omits the field is not a different thing from one that
 *  carries it. The UI must not behave differently depending on which feed won. */
function toUpdate(n: NodeWire): NodeStatusUpdate {
  return {
    id: n.id,
    status: n.status,
    online: n.online,
    desired_revision: n.desired_revision,
    applied_revision: n.applied_revision,
    drift: n.drift ?? n.applied_revision !== n.desired_revision,
  };
}

/** Indexes a node array by id. A non-array yields an empty map, so a payload
 *  whose shape changed cannot take the live view down. */
export function indexNodes(nodes: NodeWire[] | null | undefined): Record<number, NodeStatusUpdate> {
  if (!Array.isArray(nodes)) return {};
  return Object.fromEntries(nodes.map((n) => [n.id, toUpdate(n)]));
}

/** Parses one SSE payload. Malformed data yields an empty map rather than
 *  throwing, so one bad frame cannot break the live view. */
export function parseNodeEvent(data: string): Record<number, NodeStatusUpdate> {
  try {
    const parsed = JSON.parse(data) as { nodes?: NodeWire[] };
    return indexNodes(parsed.nodes);
  } catch {
    return {};
  }
}

/** Subscribes to live node status, falling back to polling if SSE fails.
 *
 *  The fallback polls `GET /api/v1/nodes`, not `/api/v1/events`. The events
 *  endpoint is the stream itself: it always answers `text/event-stream`,
 *  ignores Accept, and only ends when the client disconnects, so reading it to
 *  completion never returns. Polling it would pile up one never-resolving
 *  request every interval and display nothing. `/api/v1/nodes` returns a
 *  finite JSON body carrying the same fields. */
export function useNodeStream(): Record<number, NodeStatusUpdate> {
  const [statuses, setStatuses] = useState<Record<number, NodeStatusUpdate>>({});

  useEffect(() => {
    const source = new EventSource("/api/v1/events");
    source.addEventListener("nodes", (event) => {
      setStatuses(parseNodeEvent((event as MessageEvent<string>).data));
    });

    let pollTimer: number | undefined;
    let failures = 0;
    let cancelled = false;

    const stopPolling = () => {
      if (pollTimer !== undefined) {
        window.clearInterval(pollTimer);
        pollTimer = undefined;
      }
    };

    const poll = async () => {
      try {
        const body = await api.get<{ nodes?: NodeWire[] }>("/api/v1/nodes");
        if (cancelled) return;
        failures = 0;
        setStatuses(indexNodes(body.nodes));
      } catch {
        // Nothing to show and nothing to log: the panel is unreachable, which
        // the stale rows already say. Give up once the budget is spent so the
        // tab does not keep a dead request loop running indefinitely.
        failures += 1;
        if (failures >= maxPollFailures) stopPolling();
      }
    };

    source.onerror = () => {
      source.close();
      if (cancelled || pollTimer !== undefined) return;
      void poll();
      pollTimer = window.setInterval(() => void poll(), pollIntervalMs);
    };

    return () => {
      cancelled = true;
      source.close();
      stopPolling();
    };
  }, []);

  return statuses;
}
