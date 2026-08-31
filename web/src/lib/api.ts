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
  /**
   * The full JSON body the server sent, when there was one.
   *
   * Some failures carry fields beside the error envelope that an operator
   * needs -- the SSH bootstrap 502 returns the install script's stderr as
   * `output`, for example. `error.message` alone is a header without the
   * receipt. Kept as `unknown` because the callers that read it are the ones
   * that know what shape they expect; the common case still uses `.message`.
   */
  readonly body: unknown;

  constructor(status: number, code: string, message: string, requestID = "", body: unknown = null) {
    super(message);
    this.status = status;
    this.code = code;
    this.requestID = requestID;
    this.body = body;
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
      payload,
    );
  }
  return payload as T;
}

/**
 * Fetches a non-JSON body, such as the CSV export.
 *
 * request() parses every response as JSON, so it cannot be used here -- but a
 * FAILED export still answers with the usual JSON error envelope, so the error
 * path has to parse and the success path must not. Reading the text once and
 * only parsing it when the status is bad keeps both honest.
 */
async function requestText(path: string): Promise<string> {
  const response = await fetch(path, { credentials: "same-origin" });
  const text = await response.text();
  if (!response.ok) {
    let code = "unknown";
    let message = "request failed";
    let requestID = response.headers.get("X-Request-ID") ?? "";
    try {
      const err = (JSON.parse(text) as {
        error?: { code: string; message: string; request_id?: string };
      }).error;
      if (err) {
        code = err.code;
        message = err.message;
        requestID = err.request_id ?? requestID;
      }
    } catch {
      // A non-JSON error body, e.g. http.Error's plain text. The status and
      // whatever text arrived are still more useful than a generic message.
      if (text.trim() !== "") message = text.trim();
    }
    throw new ApiError(response.status, code, message, requestID);
  }
  return text;
}

export const api = {
  get: <T>(path: string) => request<T>("GET", path),
  getText: (path: string) => requestText(path),
  post: <T>(path: string, body?: unknown) => request<T>("POST", path, body),
  put: <T>(path: string, body?: unknown) => request<T>("PUT", path, body),
  del: <T>(path: string) => request<T>("DELETE", path),
};
