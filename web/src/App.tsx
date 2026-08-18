import { useState } from "react";
import { Login } from "./routes/Login";
import { Nodes } from "./routes/Nodes";
import { NodeDetail } from "./routes/NodeDetail";
import { api } from "./lib/api";
import { t } from "./i18n";

export default function App() {
  const [authed, setAuthed] = useState(false);
  const [selected, setSelected] = useState<number | null>(null);

  if (!authed) {
    return <Login onSuccess={() => setAuthed(true)} />;
  }

  async function signOut() {
    // Logout revokes the session server-side, so a failure here still leaves
    // the cookie live; dropping the local flag anyway would show a signed-out
    // UI backed by a session that still works.
    await api.post("/api/v1/auth/logout");
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
            onClick={() => setSelected(null)}
            className="text-zinc-400 hover:text-zinc-100"
          >
            {t("nav.nodes")}
          </button>
        </nav>
        <button
          type="button"
          onClick={signOut}
          className="ms-auto text-xs text-zinc-500 hover:text-zinc-200"
        >
          {t("nav.signOut")}
        </button>
      </header>

      <main className="p-4">
        {selected === null ? (
          <Nodes onSelect={setSelected} />
        ) : (
          <NodeDetail nodeId={selected} />
        )}
      </main>
    </div>
  );
}
