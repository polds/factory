import { CheckCircle2, Copy, XCircle } from "lucide-react";
import { useState } from "react";
import { StatusBadge } from "../../components/StatusBadge";
import { Button } from "../../components/Button";
import { Caveat } from "./caveat";
import { INFRA_CAVEAT, isFailureClass, snapshotRows } from "./format";
import type { RunDetailWire } from "./types";

function copyBlock(
  run: RunDetailWire,
  failureClass: string,
  code: string,
  summary: string,
): string {
  const evidence = run.failure?.evidence ?? [];
  const lines = [
    `class: ${failureClass}`,
    `code: ${code}`,
    `summary: ${summary}`,
    "evidence:",
    ...evidence.map((line) => `- ${line}`),
    `run id: ${run.id}`,
    `failure_reason: ${run.failure_reason ?? ""}`,
  ];
  return lines.join("\n");
}

export function FailurePanel({ run }: { run: RunDetailWire }) {
  const failureClass = run.failure?.class ?? "";
  const [toast, setToast] = useState("");
  if (!isFailureClass(failureClass) || !run.failure) return null;
  const failure = run.failure;
  const snapshot = snapshotRows(run.kubernetes_snapshot);

  async function onCopy() {
    const text = copyBlock(run, failureClass, failure.code, failure.summary);
    try {
      await navigator.clipboard.writeText(text);
      setToast("Failure details copied.");
    } catch {
      setToast("Could not copy failure details.");
    }
  }

  return (
    <section className="failure-panel" aria-label="Failure" data-failure-class={failureClass}>
      <div className="failure-panel-head">
        <StatusBadge failureClass={failureClass} />
        <div className="failure-panel-copy">
          <h2 id="failure-summary">{failure.summary}</h2>
          <p className="failure-meta">
            <code className="mono">{failure.code}</code>
            <span className="retryable">
              {failure.retryable ? (
                <CheckCircle2 aria-hidden="true" size={16} />
              ) : (
                <XCircle aria-hidden="true" size={16} />
              )}
              <span>Retryable: {failure.retryable ? "yes" : "no"}</span>
            </span>
          </p>
        </div>
        <Button type="button" onClick={() => void onCopy()}>
          <Copy aria-hidden="true" size={16} /> Copy as text
        </Button>
      </div>
      <p className="section-label">Evidence</p>
      <ul aria-label="Evidence">
        {failure.evidence.map((line, index) => (
          <li key={`${index}-${line}`} className="mono">
            {line}
          </li>
        ))}
      </ul>
      {failureClass === "infrastructure" ? <Caveat>{INFRA_CAVEAT}</Caveat> : null}
      <details className="raw-details">
        <summary>Raw details</summary>
        <dl>
          <dt>failure_reason</dt>
          <dd className="mono">{run.failure_reason ?? "—"}</dd>
          <dt>exit_code</dt>
          <dd className="mono">{run.exit_code != null ? String(run.exit_code) : "—"}</dd>
          <dt>code</dt>
          <dd className="mono">{failure.code}</dd>
          <dt>retryable</dt>
          <dd>{failure.retryable ? "yes" : "no"}</dd>
          <dt>evidence</dt>
          <dd>
            <ol>
              {failure.evidence.map((line, index) => (
                <li key={`${index}-${line}`}>{line}</li>
              ))}
            </ol>
          </dd>
        </dl>
        {snapshot ? (
          <table className="data-table">
            <caption>Kubernetes snapshot</caption>
            <tbody>
              {snapshot.map((row) => (
                <tr key={row.key}>
                  <th scope="row">{row.key}</th>
                  <td>{row.value}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : null}
      </details>
      <div role="status" aria-live="polite" className="copy-toast">
        {toast}
      </div>
    </section>
  );
}
