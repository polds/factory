import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, XCircle } from "lucide-react";
import { useRef, useState } from "react";
import { APIError, client } from "../../api/client";
import type { RunScore, ScorerResultStatus } from "../../api/types";
import { Button } from "../../components/Button";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { ErrorState } from "../../components/ErrorState";
import { Panel } from "../../components/Panel";
import { unexpected } from "../../lib/unexpected";
import { problemDetail } from "../dashboard/model";

type PendingScore = {
  scorer: string;
  baseline: RunScore | null;
};

export function ScoresPanel({ runId, factoryId }: { runId: string; factoryId: string }) {
  const queryClient = useQueryClient();
  const scoresQuery = useQuery({
    queryKey: ["scores", factoryId, runId],
    queryFn: async () => (await client.runScores(runId)).map(presentScore),
  });
  const scorersQuery = useQuery({
    queryKey: ["scorers", factoryId],
    queryFn: () => client.listScorers(factoryId),
    enabled: factoryId.length > 0,
  });
  const [chosen, setChosen] = useState("");
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [pending, setPending] = useState<PendingScore | null>(null);
  const [blocked, setBlocked] = useState<string | null>(null);
  const [scoreError, setScoreError] = useState("");
  const baselineRef = useRef<PendingScore | null>(null);

  const scorers = scorersQuery.data ?? [];
  const selected = scorers.some((scorer) => scorer.name === chosen)
    ? chosen
    : (scorers[0]?.name ?? "");
  const serverScores = scoresQuery.data ?? [];
  const rows = displayScores(serverScores, pending);

  const score = useMutation({
    mutationFn: (scorer: string) => client.scoreRun(runId, scorer),
    onSuccess: async () => {
      setPending(baselineRef.current);
      setScoreError("");
      await queryClient.invalidateQueries({ queryKey: ["scores", factoryId, runId] });
    },
    onError: (error: unknown) => {
      if (error instanceof APIError && error.status === 422) {
        setBlocked(error.problem?.detail || error.message);
        setScoreError("");
        return;
      }
      setScoreError(problemDetail(error));
    },
  });

  function requestScore() {
    if (selected === "" || score.isPending) return;
    baselineRef.current = {
      scorer: selected,
      baseline: serverScores.find((row) => row.scorer === selected) ?? null,
    };
    setConfirmOpen(false);
    score.mutate(selected);
  }

  return (
    <Panel title="Scores">
      {scoresQuery.isLoading ? <p>Loading scores</p> : null}
      {scoresQuery.isError ? (
        <ErrorState
          title="Could not load scores"
          message={problemDetail(scoresQuery.error)}
          onRetry={() => void scoresQuery.refetch()}
        />
      ) : null}
      {scoresQuery.isSuccess && rows.length === 0 ? <p>No scores</p> : null}
      {rows.length > 0 ? <ScoresTable rows={rows} /> : null}
      {factoryId.length > 0 && scorersQuery.isLoading ? <p>Loading scorers</p> : null}
      {scorersQuery.isError ? <p role="alert">{problemDetail(scorersQuery.error)}</p> : null}
      {blocked ? <p role="alert">{blocked}</p> : null}
      {scoreError ? <p role="alert">{scoreError}</p> : null}
      {factoryId.length > 0 && scorersQuery.isSuccess && scorers.length === 0 ? (
        <p>No scorers</p>
      ) : null}
      {!blocked && selected !== "" ? (
        <div className="actions">
          <label className="field">
            <span>Scorer</span>
            <select value={selected} onChange={(event) => setChosen(event.target.value)}>
              {scorers.map((scorer) => (
                <option key={scorer.name} value={scorer.name}>
                  {scorer.name}
                </option>
              ))}
            </select>
          </label>
          <Button type="button" disabled={score.isPending} onClick={() => setConfirmOpen(true)}>
            Score now
          </Button>
        </div>
      ) : null}
      <ConfirmDialog
        open={confirmOpen}
        title="Score this run?"
        description={`Score this run with ${selected}.`}
        confirmLabel="Score now"
        onConfirm={requestScore}
        onOpenChange={setConfirmOpen}
      />
    </Panel>
  );
}

function ScoresTable({ rows }: { rows: readonly RunScore[] }) {
  return (
    <div className="table-wrap" tabIndex={0}>
      <table className="data-table">
        <caption className="sr-only">Scores</caption>
        <thead>
          <tr>
            <th scope="col">Scorer</th>
            <th scope="col">Label</th>
            <th scope="col">Passed</th>
            <th scope="col">Score</th>
            <th scope="col">Status</th>
            <th scope="col">Reasoning</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.scorer}>
              <th scope="row">{row.scorer}</th>
              <td>{row.label}</td>
              <td>
                <PassedMark passed={row.passed} />
              </td>
              <td className="mono">{formatScore(row.score)}</td>
              <td>{statusLabel(row.status)}</td>
              <td>
                <p className="quote">{row.reasoning}</p>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function PassedMark({ passed }: { passed: boolean }) {
  const Icon = passed ? CheckCircle2 : XCircle;
  return (
    <span className="stat-label">
      <Icon aria-hidden="true" size={16} />
      <span>{passed ? "Passed" : "Not passed"}</span>
    </span>
  );
}

function presentScore(raw: RunScore): RunScore {
  return {
    scorer: raw.scorer,
    label: raw.label,
    score: raw.score,
    passed: raw.passed,
    reasoning: raw.reasoning,
    status: raw.status,
  };
}

function formatScore(score: number): string {
  if (!Number.isFinite(score)) return "—";
  return String(score);
}

function statusLabel(status: ScorerResultStatus): string {
  switch (status) {
    case "ok":
      return "OK";
    case "scorer_invalid":
      return "Scorer invalid";
    case "pending":
      return "Pending";
    default:
      return unexpected(status);
  }
}

function sameRunScore(row: RunScore, baseline: RunScore | null): boolean {
  if (!baseline) return false;
  return (
    row.scorer === baseline.scorer &&
    row.label === baseline.label &&
    row.score === baseline.score &&
    row.passed === baseline.passed &&
    row.reasoning === baseline.reasoning &&
    row.status === baseline.status
  );
}

function latestByScorer(rows: readonly RunScore[]): RunScore[] {
  const byScorer = new Map<string, RunScore>();
  for (const row of rows) byScorer.set(row.scorer, presentScore(row));
  return [...byScorer.values()];
}

function displayScores(server: readonly RunScore[], pending: PendingScore | null): RunScore[] {
  const latest = latestByScorer(server);
  if (!pending) return latest;
  const current = latest.find((row) => row.scorer === pending.scorer);
  if (current && !sameRunScore(current, pending.baseline)) return latest;
  const placeholder: RunScore = {
    scorer: pending.scorer,
    label: pending.baseline?.label ?? "",
    score: Number.NaN,
    passed: false,
    reasoning: "",
    status: "pending",
  };
  if (!current) return [...latest, placeholder];
  return latest.map((row) => (row.scorer === pending.scorer ? placeholder : row));
}
