import { initializeLanguage } from "./i18n";
import { SiteTimezoneSync } from "./ui/SiteTimezoneSync";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter } from "react-router-dom";

import { App } from "./ui/App";
import { ThemeProvider } from "./ui/ThemeProvider";
import "./ui/theme.css";
import "./ui/styles.css";

initializeLanguage();

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 0,
      gcTime: 0,
      retry: 1,
      refetchOnWindowFocus: true,
      refetchOnReconnect: true
    },
    mutations: { retry: 0 }
  }
});

const root = document.getElementById("root");
if (!root) {
  throw new Error("React root element is missing");
}

createRoot(root).render(
  <QueryClientProvider client={queryClient}>
    <ThemeProvider>
      <SiteTimezoneSync />
      <BrowserRouter basename="/admin">
        <App />
      </BrowserRouter>
    </ThemeProvider>
  </QueryClientProvider>
);
