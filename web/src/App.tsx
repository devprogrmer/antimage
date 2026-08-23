import { useState } from "react";
import { Login } from "./routes/Login";
import { Dashboard } from "./routes/Dashboard";
import { Nodes } from "./routes/Nodes";
import { NodeDetail } from "./routes/NodeDetail";
import { Observability } from "./routes/Observability";
import { Subjects } from "./routes/Subjects";
import { SubjectDetail } from "./routes/SubjectDetail";
import { Profile } from "./routes/Profile";
import { api } from "./lib/api";
import { getLocale, locales, setLocale, t } from "./i18n";
import type { Locale } from "./i18n";

type Route = "dashboard" | "nodes" | "observability" | "subjects" | "profile";

export default function App() {
  const [authed, setAuthed] = useState(false);
  const [route, setRoute] = useState<Route>("dashboard");
  const [selected, setSelected] = useState<number | null>(null);
  // setLocale mutates module state and flips <html lang>/<html dir>; this
  // mirrors it into React state so the tree re-renders with the new catalogue.
  const [locale, setLocaleState] = useState<Locale>(getLocale());

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
    setRoute("nodes");
    setSelected(null);
    setAuthed(false);
  }

  return (
    <div className="min-h-screen bg-zinc-950 text-zinc-100">
      <header className="flex items-center gap-4 border-b border-zinc-800 px-4 py-2">
        <h1 className="font-mono text-sm font-semibold">{t("app.name")}</h1>
        <nav className="flex gap-3 text-xs">
          <button
            type="button"
            onClick={() => {
              setRoute("dashboard");
              setSelected(null);
            }}
            className={route === "dashboard" ? "text-zinc-100" : "text-zinc-400 hover:text-zinc-100"}
          >
            {t("nav.dashboard")}
          </button>
          <button
            type="button"
            onClick={() => {
              setRoute("nodes");
              setSelected(null);
            }}
            className={route === "nodes" ? "text-zinc-100" : "text-zinc-400 hover:text-zinc-100"}
          >
            {t("nav.nodes")}
          </button>
          <button
            type="button"
            onClick={() => {
              setRoute("subjects");
              setSelected(null);
            }}
            className={route === "subjects" ? "text-zinc-100" : "text-zinc-400 hover:text-zinc-100"}
          >
            {t("nav.subjects")}
          </button>
          <button
            type="button"
            onClick={() => {
              setRoute("observability");
              setSelected(null);
            }}
            className={route === "observability" ? "text-zinc-100" : "text-zinc-400 hover:text-zinc-100"}
          >
            {t("observability.title")}
          </button>
          <button
            type="button"
            onClick={() => {
              setRoute("profile");
              setSelected(null);
            }}
            className={route === "profile" ? "text-zinc-100" : "text-zinc-400 hover:text-zinc-100"}
          >
            {t("nav.profile")}
          </button>
        </nav>
        <select
          value={locale}
          onChange={(e) => changeLocale(e.target.value as Locale)}
          aria-label={t("nav.language")}
          className="ms-auto rounded border border-zinc-800 bg-zinc-900 px-1 py-0.5 text-xs text-zinc-300"
        >
          {locales.map((l) => (
            <option key={l.code} value={l.code}>
              {l.label}
            </option>
          ))}
        </select>
        <button
          type="button"
          onClick={signOut}
          className="text-xs text-zinc-500 hover:text-zinc-200"
        >
          {t("nav.signOut")}
        </button>
      </header>

      <main className="p-4">
        {route === "dashboard" ? (
          <Dashboard />
        ) : route === "profile" ? (
          <Profile />
        ) : route === "observability" ? (
          <Observability />
        ) : route === "subjects" ? (
          selected === null ? (
            <Subjects onSelect={(id) => setSelected(id)} />
          ) : (
            <SubjectDetail subjectId={selected} />
          )
        ) : selected === null ? (
          <Nodes onSelect={(id) => {
            setRoute("nodes");
            setSelected(id);
          }} />
        ) : (
          <NodeDetail nodeId={selected} />
        )}
      </main>
    </div>
  );
}
