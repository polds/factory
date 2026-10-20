import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { createAppRouter } from "./routes";
import { ThemeProvider } from "./lib/theme";
import "./index.css";
// Shared operator-surface vocabulary (.panel, .page-header, .table-scroll, .data-head).
// Imported here so a direct load of any route gets it, not only the dashboard chunk.
import "./features/dashboard/surfaces.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { retry: false, refetchOnWindowFocus: false },
  },
});

const router = createAppRouter();

const root = document.getElementById("root");
if (!root) throw new Error("missing root element");

createRoot(root).render(
  <StrictMode>
    <ThemeProvider>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </ThemeProvider>
  </StrictMode>,
);
