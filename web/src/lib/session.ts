import { useQuery } from "@tanstack/react-query";
import { api } from "./api";

export interface Session {
  admin_id: number;
  role: string;
  is_super: boolean;
  permissions: string[];
  // Whether the signed-in admin has TOTP enrolled. The Profile TOTP section
  // reads this to decide whether to offer enrolment or the disable form; a
  // missing field (older backend or older session cache) is treated as
  // disabled so the UI leans toward enabling.
  totp_enabled?: boolean;
}

/**
 * The signed-in actor, as the server reports them.
 *
 * Cached under a stable key so the header, the nav and any permission-gated
 * panel share one fetch: the answer only changes on sign-in or sign-out, and a
 * refetch per gate would put a request behind every render.
 *
 * `enabled` exists because hooks cannot be called conditionally: App must call
 * this above its signed-out early return, and without the gate every visit to
 * the login screen would fire a request that can only ever 401.
 */
export function useSession(enabled = true) {
  return useQuery({
    queryKey: ["session"],
    queryFn: () => api.get<Session>("/api/v1/auth/me"),
    staleTime: Infinity,
    enabled,
  });
}

/**
 * Whether the actor holds a permission.
 *
 * This decides what the UI OFFERS, never what it allows. Every route re-checks
 * server-side, so hiding a control is a courtesy -- it keeps an operator from
 * filling in a form that was always going to 403 -- and not a boundary. A
 * missing session answers false, so a control appears only once the grant is
 * known rather than flashing in and being withdrawn.
 *
 * is_super is deliberately NOT consulted. rbac.Check gives a super admin a
 * bypass on SCOPE and none on permission -- "a custom role stripped of a
 * permission is honoured even for supers" -- so treating is_super as a blanket
 * yes would offer controls the server then refuses. The permissions list from
 * /auth/me is the actor's resolved set and is the whole answer.
 */
export function can(session: Session | undefined, permission: string): boolean {
  if (!session) return false;
  return session.permissions.includes(permission);
}
