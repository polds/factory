import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { client } from "../../api/client";
import type { ApprovalDecision } from "../../api/types";
import { Button } from "../../components/Button";
import { problemDetail } from "../dashboard/model";

export async function refreshHumanQueries(
  queryClient: QueryClient,
  factoryId: string,
): Promise<void> {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: ["inbox", factoryId] }),
    queryClient.invalidateQueries({ queryKey: ["approvals", factoryId] }),
    queryClient.invalidateQueries({ queryKey: ["tasks", factoryId] }),
    queryClient.invalidateQueries({ queryKey: ["task", factoryId] }),
  ]);
}

export async function submitDecision(
  queryClient: QueryClient,
  factoryId: string,
  approvalId: string,
  decision: ApprovalDecision,
  note: string,
): Promise<void> {
  try {
    await client.decideApproval(approvalId, decision, note);
  } finally {
    await refreshHumanQueries(queryClient, factoryId);
  }
}

export async function submitAnswer(
  queryClient: QueryClient,
  factoryId: string,
  workItemId: string,
  text: string,
): Promise<void> {
  try {
    await client.answerInput(workItemId, text);
  } finally {
    await refreshHumanQueries(queryClient, factoryId);
  }
}

export function ApprovalActions({
  factoryId,
  approvalId,
  gate,
}: {
  factoryId: string;
  approvalId: string;
  gate: string;
}) {
  const queryClient = useQueryClient();
  const [note, setNote] = useState("");
  const [phase, setPhase] = useState<"idle" | "saving" | "saved">("idle");
  const [decision, setDecision] = useState<ApprovalDecision>("approve");
  const [error, setError] = useState<string | null>(null);
  const busy = useRef(false);
  const verb = decision === "approve" ? "Approved" : "Rejected";

  async function submit(next: ApprovalDecision) {
    if (busy.current) return;
    busy.current = true;
    setDecision(next);
    setPhase("saving");
    setError(null);
    try {
      await submitDecision(queryClient, factoryId, approvalId, next, note);
      setPhase("saved");
    } catch (err) {
      busy.current = false;
      setPhase("idle");
      setError(problemDetail(err));
    }
  }

  return (
    <div className="field">
      <p>
        <span className="muted">Gate: </span>
        <span className="mono">{gate}</span>
      </p>
      {phase !== "idle" ? (
        <p role="status">
          {verb} by you · {phase === "saving" ? "Saving…" : "Saved"}
        </p>
      ) : null}
      {phase !== "saved" ? (
        <>
          <div className="actions">
            <Button
              type="button"
              aria-disabled={phase === "saving"}
              onClick={() => void submit("approve")}
            >
              Approve
            </Button>
            <Button
              type="button"
              aria-disabled={phase === "saving"}
              onClick={() => void submit("reject")}
            >
              Reject
            </Button>
          </div>
          <label className="field" htmlFor={`note-${approvalId}`}>
            Note (optional)
            <textarea
              id={`note-${approvalId}`}
              value={note}
              onChange={(event) => setNote(event.target.value)}
            />
          </label>
        </>
      ) : null}
      {error ? <p role="alert">{error}</p> : null}
    </div>
  );
}

export function AnswerForm({
  inputId,
  prompt,
  onSubmit,
  error,
}: {
  inputId: string;
  prompt: string;
  onSubmit: (text: string) => Promise<void>;
  error: string | null;
}) {
  const [text, setText] = useState("");
  const [pending, setPending] = useState(false);

  async function submit() {
    const answer = text.trim();
    if (!answer || pending) return;
    setPending(true);
    try {
      await onSubmit(answer);
    } finally {
      setPending(false);
    }
  }

  return (
    <div className="field">
      <blockquote className="quote">{prompt}</blockquote>
      <label className="field" htmlFor={inputId}>
        Answer
        <textarea id={inputId} value={text} onChange={(event) => setText(event.target.value)} />
      </label>
      <div className="actions">
        <Button type="button" onClick={() => void submit()}>
          Send answer
        </Button>
      </div>
      {error ? <p role="alert">{error}</p> : null}
    </div>
  );
}
