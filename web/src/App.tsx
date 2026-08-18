import { useState } from "react";
import { Login } from "./routes/Login";
import { t } from "./i18n";

export default function App() {
  const [authed, setAuthed] = useState(false);

  if (!authed) {
    return <Login onSuccess={() => setAuthed(true)} />;
  }

  // The authenticated shell (navigation, node list, audit) arrives with the
  // later UI tasks; the session cookie is set by then, so this only has to
  // prove the login flow lands somewhere.
  return (
    <main className="min-h-screen bg-zinc-950 p-6 text-sm text-zinc-100">
      <h1 className="font-semibold">{t("app.name")}</h1>
    </main>
  );
}
