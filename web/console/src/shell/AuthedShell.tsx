import { Outlet, useNavigate, useRouterState } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Suspense, useState } from "react";
import { client } from "../api/client";
import { ErrorState } from "../components/ErrorState";
import { AppShell } from "./AppShell";

function factoryFromPath(pathname: string): string | null {
  const match = /^\/f\/([^/]+)/.exec(pathname);
  const id = match?.[1];
  return id ? decodeURIComponent(id) : null;
}

function sectionFromPath(pathname: string): string | null {
  const match = /^\/f\/[^/]+\/([^/]+)/.exec(pathname);
  return match?.[1] ?? null;
}

export function AuthedShell() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const [picked, setPicked] = useState<string | null>(null);
  const factories = useQuery({ queryKey: ["factories"], queryFn: () => client.listFactories() });
  const routeFactory = factoryFromPath(pathname);
  const factoryId = routeFactory ?? picked ?? factories.data?.[0]?.id ?? null;

  function onFactoryChange(id: string) {
    setPicked(id);
    const section = sectionFromPath(pathname);
    if (!section) return;
    const next =
      section === "work-items"
        ? `/f/${encodeURIComponent(id)}/work-items`
        : `/f/${encodeURIComponent(id)}/${section}`;
    void navigate({ to: next });
  }

  const factoriesState = factories.isPending ? "loading" : factories.isError ? "error" : "ready";

  return (
    <AppShell
      factories={factories.data ?? []}
      factoryId={factoryId}
      onFactoryChange={onFactoryChange}
      factoriesState={factoriesState}
    >
      {factories.isError ? (
        <ErrorState
          title="Could not load factories"
          message="The factory list could not be loaded."
          onRetry={() => void queryClient.invalidateQueries({ queryKey: ["factories"] })}
        />
      ) : (
        <Suspense fallback={<p>Loading page</p>}>
          <Outlet />
        </Suspense>
      )}
    </AppShell>
  );
}
