import { Navigate } from "@tanstack/react-router";
import { useSyncExternalStore, type ReactNode } from "react";
import { getAPIKey, getAPIKeyServer, subscribeSession } from "../api/session";

export function RequireAuth({ children }: { children: ReactNode }) {
  const key = useSyncExternalStore(subscribeSession, getAPIKey, getAPIKeyServer);
  if (!key) return <Navigate to="/login" replace />;
  return children;
}
