import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Navigate, Route, Routes, useNavigate, useParams } from "react-router-dom";

import { AppShell } from "@/components/AppShell";
import { Login } from "@/routes/Login";
import { Dashboard } from "@/routes/Dashboard";
import { Nodes } from "@/routes/Nodes";
import { NodeDetail } from "@/routes/NodeDetail";
import { Observability } from "@/routes/Observability";
import { Audit } from "@/routes/Audit";
import { Templates } from "@/routes/Templates";
import { Subjects } from "@/routes/Subjects";
import { SubjectDetail } from "@/routes/SubjectDetail";
import { Profile } from "@/routes/Profile";
import { Resellers } from "@/routes/Resellers";
import { ResellerDetail } from "@/routes/ResellerDetail";
import { Hosts } from "@/routes/Hosts";
import { Settings } from "@/routes/Settings";
import { Admins } from "@/routes/Admins";
import { api } from "@/lib/api";
import { can, useSession } from "@/lib/session";
import type { Session } from "@/lib/session";
import { getLocale, setLocale, t } from "@/i18n";
import type { Locale } from "@/i18n";

export default function App() {
  const [authed, setAuthed] = useState(false);
  const session = useSession(authed);
  const queryClient = useQueryClient();
  // setLocale mutates module state and flips <html lang>/<html dir>; this
  // mirrors it into React state so the tree re-renders with the new catalogue.
  const [, setLocaleState] = useState<Locale>(getLocale());

  function changeLocale(next: Locale) {
    setLocale(next);
    setLocaleState(next);
  }

  if (!authed) {
    return <Login onSuccess={() => setAuthed(true)} />;
  }

  async function signOut() {
    // Logout revokes the session server-side, so a failure here still leaves
    // the cookie live; dropping the local flag anyway would show a signed-out
    // UI backed by a session that still works.
    await api.post("/api/v1/auth/logout");
    setAuthed(false);
    // The session is cached with staleTime: Infinity, so without this the next
    // account to sign in on this page inherits the previous actor's permission
    // set and is offered controls it does not hold. Everything else is dropped
    // for the same reason: it was all fetched under the old session's scope.
    queryClient.clear();
  }

  return (
    <Routes>
      <Route
        element={
          <AppShell
            session={session.data}
            onSignOut={signOut}
            onLocaleChange={changeLocale}
          />
        }
      >
        <Route index element={<Dashboard />} />
        <Route path="nodes" element={<NodesRoute />} />
        <Route path="nodes/:nodeId" element={<NodeDetailRoute />} />
        <Route path="subjects" element={<SubjectsRoute />} />
        <Route path="subjects/:subjectId" element={<SubjectDetailRoute />} />
        <Route
          path="resellers"
          element={
            <RequirePermission session={session.data} permission="reseller:read">
              <ResellersRoute />
            </RequirePermission>
          }
        />
        <Route
          path="resellers/:resellerId"
          element={
            <RequirePermission session={session.data} permission="reseller:read">
              <ResellerDetailRoute />
            </RequirePermission>
          }
        />
        <Route path="observability" element={<Observability />} />
        <Route
          path="audit"
          element={
            <RequirePermission session={session.data} permission="audit:read">
              <Audit />
            </RequirePermission>
          }
        />
        {/* Ownership-scoped in the service layer rather than behind a
            permission, so every signed-in operator has their own. */}
        <Route path="templates" element={<Templates />} />
        <Route
          path="hosts"
          element={
            <RequirePermission session={session.data} permission="service:read">
              <Hosts />
            </RequirePermission>
          }
        />
        <Route path="settings" element={<Settings />} />
        <Route
          path="admins"
          element={
            <RequirePermission session={session.data} permission="admin:manage">
              <Admins />
            </RequirePermission>
          }
        />
        <Route path="profile" element={<Profile />} />
        {/* An unknown path is an operator's typo or a stale bookmark, not an
            error worth a screen of its own. */}
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}

/**
 * RequirePermission keeps a gated screen out of reach of a direct URL.
 *
 * The nav already hides these entries, but hiding a link is not a control: an
 * operator can type the path, and a stale bookmark survives a role change. This
 * is still a courtesy rather than a boundary -- every route re-checks
 * server-side -- but it turns a wall of 403s into a redirect.
 */
function RequirePermission({
  session,
  permission,
  children,
}: {
  session: Session | undefined;
  permission: string;
  children: React.ReactNode;
}) {
  // While the session is still loading nothing is known, so nothing is shown.
  // Rendering the child first and pulling it back would flash a screen the
  // actor may not be allowed to see.
  if (!session) {
    return <p className="text-sm text-muted-foreground">{t("loading")}</p>;
  }
  if (!can(session, permission)) {
    return <Navigate to="/" replace />;
  }
  return <>{children}</>;
}

// --- Adapters between the router and screens that predate it.
//
// Each screen still takes ids and callbacks rather than reading the URL, which
// keeps them testable in isolation. These translate one into the other, so the
// address bar becomes the source of truth without every screen being rewritten.

function NodesRoute() {
  const navigate = useNavigate();
  return <Nodes onSelect={(id) => navigate(`/nodes/${id}`)} />;
}

function NodeDetailRoute() {
  const { nodeId } = useParams();
  return <NodeDetail nodeId={Number(nodeId)} />;
}

function SubjectsRoute() {
  const navigate = useNavigate();
  return <Subjects onSelect={(id) => navigate(`/subjects/${id}`)} />;
}

function SubjectDetailRoute() {
  const { subjectId } = useParams();
  return <SubjectDetail subjectId={Number(subjectId)} />;
}

function ResellersRoute() {
  const navigate = useNavigate();
  return <Resellers onSelect={(id) => navigate(`/resellers/${id}`)} />;
}

function ResellerDetailRoute() {
  const { resellerId } = useParams();
  return <ResellerDetail resellerID={Number(resellerId)} />;
}
