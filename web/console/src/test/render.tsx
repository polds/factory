import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { render } from "@testing-library/react";
import type { ReactElement } from "react";
import { ThemeProvider } from "../lib/theme";
import { createAppRouter } from "../routes";

export async function renderApp(initialPath = "/") {
  // render() returns before the router finishes loading. load() preloads the
  // matched lazy route (component.preload()), and that import was landing
  // inside findBy's 1000ms budget, so a busy runner still showed "Loading run".
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
