import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import App from "./App.tsx";
import { setLocale } from "./i18n";

// Setting the locale once at boot fixes <html lang> and <html dir>, which is
// what every logical CSS property in the app resolves against.
setLocale(navigator.language.startsWith("fa") ? "fa" : "en");

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
