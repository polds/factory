import { useQuery } from "@tanstack/react-query";
import { getAPIKey } from "../../api/session";

export type NativeBudgetRow = {
  scopeId: string;
  kind: string;
  ceilingCents: number;
  spentCents: number;
  reservedCents: number;
  paused: boolean;
};

export type NativeBlockerRow = {
  parentTaskId: string;
  childTaskId: string;
  policy: string;
  childStatus: string;
};

export type NativeTreeView = {
  invocationId: string;
  stageOwner: string;
  nodes: { taskId: string; assignee: string; status: string; depth: number; stageOwner: boolean }[];
  budgets: NativeBudgetRow[];
  blockers: NativeBlockerRow[];
};

export function NativePanel({
  views,
  customStages,
}: {
  views: readonly NativeTreeView[] | null;
  customStages: readonly string[];
}) {
  if ((views == null || views.length === 0) && customStages.length === 0) return null;
  return (
    <section className="panel" aria-label="Native coordination">
      <h3>Native coordination</h3>
      {customStages.map((stage) => (
        <p key={stage}>Custom stage {stage}</p>
      ))}
      {(views ?? []).map((view) => (
        <div key={view.invocationId}>
          <p>Stage owner {view.stageOwner}</p>
          {view.nodes.map((node) => (
            <p key={node.taskId}>
              {node.assignee} {node.status}
            </p>
          ))}
          {view.budgets.map((budget) => (
            <p key={budget.scopeId}>
              Budget {budget.kind} {budget.spentCents} of {budget.ceilingCents}
              {budget.paused ? " paused" : ""}
            </p>
          ))}
          {view.blockers.map((blocker) => (
            <p key={`${blocker.parentTaskId}-${blocker.childTaskId}`}>
              Blocker {blocker.childTaskId} {blocker.childStatus} {blocker.policy}
            </p>
          ))}
        </div>
      ))}
    </section>
  );
}

async function loadNativeTrees(factoryId: string): Promise<NativeTreeView[] | null> {
  try {
    const headers = new Headers({ Accept: "application/json" });
    const key = getAPIKey();
    if (key) headers.set("Authorization", `Bearer ${key}`);
    const response = await fetch(`/api/v1/factory/${encodeURIComponent(factoryId)}/native-trees`, {
      headers,
    });
    if (!response.ok) return null;
    const body: unknown = await response.json();
    if (!Array.isArray(body)) return null;
    return body as NativeTreeView[];
  } catch {
    return null;
  }
}

export function NativeConsole({
  factoryId,
  customStages,
}: {
  factoryId: string;
  customStages: readonly string[];
}) {
  const query = useQuery({
    queryKey: ["native-trees", factoryId],
    enabled: factoryId.length > 0,
    queryFn: () => loadNativeTrees(factoryId),
  });
  return <NativePanel views={query.data ?? null} customStages={customStages} />;
}
