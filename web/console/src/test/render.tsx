import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { render } from "@testing-library/react";
import type { ReactElement } from "react";
import { createAppRouter, preloadAppRoutes } from "../routes";
import { ThemeProvider } from "../lib/theme";

export async function renderApp(initialPath = "/") {
  // Lazy route chunks and router.load() both finish after Testing Library's
  // render() returns. findBy's 1000ms budget was spent on that cold start, so
  // a busy runner still showed "Loading run". Settle both before returning.
  await preloadAppRoutes();
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } },
  });
  const router = createAppRouter(createMemoryHistory({ initialEntries: [initialPath] }));
  await router.load();
  const view = render(
    <ThemeProvider>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </ThemeProvider>,
  );
  return { ...view, queryClient, router };
}

export function renderWithTheme(ui: ReactElement) {
  return render(<ThemeProvider>{ui}</ThemeProvider>);
}
