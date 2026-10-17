import {
  createRootRoute,
  createRoute,
  createRouter,
  lazyRouteComponent,
  Outlet,
  useRouterState,
  type RouterHistory,
} from "@tanstack/react-router";
import { createBrowserHistory } from "@tanstack/react-router";
import { AuthedShell } from "./shell/AuthedShell";
import { LoginPage } from "./shell/LoginPage";
import { RequireAuth } from "./shell/RequireAuth";

const DashboardPage = lazyRouteComponent(() => import("./features/dashboard"));
const WorkItemsPage = lazyRouteComponent(() => import("./features/work-items"));
const RunsPage = lazyRouteComponent(() => import("./features/runs"));
const InboxPage = lazyRouteComponent(() => import("./features/inbox"));
const DefinitionPage = lazyRouteComponent(() => import("./features/definition"));
const AutomationsPage = lazyRouteComponent(() => import("./features/automations"));
const WebhooksPage = lazyRouteComponent(() => import("./features/webhooks"));
const DeliveriesPage = lazyRouteComponent(() => import("./features/deliveries"));
const WorkersPage = lazyRouteComponent(() => import("./features/workers"));
const SecretsPage = lazyRouteComponent(() => import("./features/secrets"));

function RootLayout() {
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  if (pathname === "/login") return <Outlet />;
  return (
    <RequireAuth>
      <AuthedShell />
    </RequireAuth>
  );
}

const rootRoute = createRootRoute({ component: RootLayout });

const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  component: DashboardPage,
});

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  component: LoginPage,
});

const workItemsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/f/$factory/work-items",
  component: WorkItemsPage,
});

const workItemRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/f/$factory/work-items/$id",
  component: WorkItemsPage,
});

const runsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/runs",
  component: RunsPage,
});

const runRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/runs/$runId",
  component: RunsPage,
});

const inboxRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/f/$factory/inbox",
  component: InboxPage,
});

const definitionRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/f/$factory/definition",
  component: DefinitionPage,
});

const automationsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/f/$factory/automations",
  component: AutomationsPage,
});

const webhooksRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/f/$factory/webhooks",
  component: WebhooksPage,
});

const deliveriesRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/f/$factory/deliveries",
  component: DeliveriesPage,
});

const workersRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/workers",
  component: WorkersPage,
});

const secretsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/f/$factory/secrets",
  component: SecretsPage,
});

const lazyPages = [
  DashboardPage,
  WorkItemsPage,
  RunsPage,
  InboxPage,
  DefinitionPage,
  AutomationsPage,
  WebhooksPage,
  DeliveriesPage,
  WorkersPage,
  SecretsPage,
];

export function preloadAppRoutes(): Promise<void> {
  return Promise.all(lazyPages.map((page) => page.preload?.() ?? Promise.resolve())).then(
    () => undefined,
  );
}

const routeTree = rootRoute.addChildren([
  indexRoute,
  loginRoute,
  workItemsRoute,
  workItemRoute,
  runsRoute,
  runRoute,
  inboxRoute,
  definitionRoute,
  automationsRoute,
  webhooksRoute,
  deliveriesRoute,
  workersRoute,
  secretsRoute,
]);

export function createAppRouter(history?: RouterHistory) {
  return createRouter({
    routeTree,
    history: history ?? createBrowserHistory(),
  });
}

export type AppRouter = ReturnType<typeof createAppRouter>;
