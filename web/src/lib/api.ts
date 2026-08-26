// The fields are declared and assigned rather than written as constructor
// parameter properties: the Vite template compiles with erasableSyntaxOnly,
// which rejects any TypeScript syntax that emits code.
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  /**
   * The id the server stamped on this request.
   *
   * It is the same id on the audit row and in the server log, so it is the one
   * thing an operator can quote that lets somebody else find what happened.
   * Empty when the server did not send one, which means the failure happened
   * before the middleware ran -- a network error, or a proxy in between.
   */
  readonly requestID: string;

  constructor(status: number, code: string, message: string, requestID = "") {
    super(message);
    this.status = status;
    this.code = code;
    this.requestID = requestID;
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch(path, {
    method,
    headers: body === undefined ? {} : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: "same-origin",
  });

  if (response.status === 204) return undefined as T;

  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    const err = (payload as {
      error?: { code: string; message: string; request_id?: string };
    }).error;
    // Falls back to the header, which the middleware sets even on a response
    // whose body never made it through an intermediary.
    const requestID = err?.request_id ?? response.headers.get("X-Request-ID") ?? "";
    throw new ApiError(
      response.status,
      err?.code ?? "unknown",
      err?.message ?? "request failed",
      requestID,
    );
  }
  return payload as T;
}

export const api = {
  get: <T>(path: string) => request<T>("GET", path),
  post: <T>(path: string, body?: unknown) => request<T>("POST", path, body),
  put: <T>(path: string, body?: unknown) => request<T>("PUT", path, body),
  del: <T>(path: string) => request<T>("DELETE", path),
};
