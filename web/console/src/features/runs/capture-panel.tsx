import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { client } from "../../api/client";
import type { CapturedBenchmarkTask } from "../../api/types";
import { Button } from "../../components/Button";
import { Panel } from "../../components/Panel";
import { problemDetail } from "../dashboard/model";

export function CapturePanel({ runId }: { runId: string }) {
  const [suite, setSuite] = useState("");
  const [name, setName] = useState("");
  const [captured, setCaptured] = useState<CapturedBenchmarkTask | null>(null);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState("");
  const capture = useMutation({
    mutationFn: () =>
      client.captureBenchmarkTask(runId, { suite: suite.trim(), name: name.trim() }),
    onSuccess: (task) => {
      setCaptured({
        path: task.path,
        content: task.content,
        head_refs: task.head_refs.map((ref) => ({
          host: ref.host,
          owner: ref.owner,
          repo: ref.repo,
          sha: ref.sha,
        })),
      });
      setCopied("");
      setError("");
    },
    onError: (err: unknown) => setError(problemDetail(err)),
  });
  const ready = suite.trim() !== "" && name.trim() !== "";

  async function onCopy() {
    if (!captured) return;
    try {
      await navigator.clipboard.writeText(captured.content);
      setCopied("Benchmark task copied.");
    } catch {
      setCopied("Could not copy the benchmark task.");
    }
  }

  return (
    <Panel title="Capture as benchmark task">
      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (!ready || capture.isPending) return;
          capture.mutate();
        }}
      >
        <div className="form-grid">
          <label className="field">
            <span>Suite</span>
            <input value={suite} onChange={(event) => setSuite(event.target.value)} />
          </label>
          <label className="field">
            <span>Name</span>
            <input value={name} onChange={(event) => setName(event.target.value)} />
          </label>
        </div>
        <div className="actions">
          <Button type="submit" disabled={!ready || capture.isPending}>
            Capture as benchmark task
          </Button>
        </div>
      </form>
      {error ? <p role="alert">{error}</p> : null}
      {captured ? (
        <>
          <p>
            Path <span className="mono">{captured.path}</span>
          </p>
          <div className="table-scroll" tabIndex={0}>
            <pre>{captured.content}</pre>
          </div>
          <div className="actions">
            <Button type="button" onClick={() => void onCopy()}>
              Copy benchmark task
            </Button>
          </div>
          {copied ? <p role="status">{copied}</p> : null}
          <ul className="stat-list">
            {captured.head_refs.map((ref) => (
              <li key={`${ref.host}/${ref.owner}/${ref.repo}/${ref.sha}`}>
                <span className="mono">
                  {ref.host}/{ref.owner}/{ref.repo}
                </span>{" "}
                <span className="mono">{ref.sha}</span>
              </li>
            ))}
          </ul>
        </>
      ) : null}
    </Panel>
  );
}
