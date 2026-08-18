import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import "./index.css";
import App from "./App.tsx";
import { setLocale } from "./i18n";

// Setting the locale once at boot fixes <html lang> and <html dir>, which is
// what every logical CSS property in the app resolves against.
setLocale(navigator.language.startsWith("fa") ? "fa" : "en");

// The node list and detail screens run on useQuery, which throws without a
// provider above it. Retries are off for 401s: an expired session should send
// the operator back to the login screen, not silently retry three times.
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (failureCount, error) => {
        const status = (error as { status?: number }).status;
        if (status === 401 || status === 403) return false;
        return failureCount < 2;
      },
      staleTime: 5_000,
    },
  },
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </StrictMode>,
);
