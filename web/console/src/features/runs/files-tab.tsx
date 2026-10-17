import { FileDiff, GitPullRequest } from "lucide-react";
import { prNumber } from "./format";
import type { TranscriptRow } from "./types";

export function FilesTab({ rows, prUrl }: { rows: readonly TranscriptRow[]; prUrl?: string }) {
  const files = rows.filter((row) => row.kind === "file");
  const number = prUrl ? prNumber(prUrl) : null;
  return (
    <div className="files-pr">
      <section>
        <p className="section-label">Changed files ({files.length})</p>
        {files.length === 0 ? (
          <p className="muted">No file changes in this transcript.</p>
        ) : (
          <ul className="file-list">
            {files.map((file) => (
              <li key={file.key}>
                <FileDiff aria-hidden="true" size={14} />
                <span className="mono">{file.path}</span>
                <span className="add">+{file.additions ?? 0}</span>
                <span className="del">-{file.deletions ?? 0}</span>
              </li>
            ))}
          </ul>
        )}
      </section>
      <section>
        <p className="section-label">Pull request</p>
        {prUrl ? (
          <a className="pr-link" href={prUrl} target="_blank" rel="noreferrer">
            <GitPullRequest aria-hidden="true" size={16} />
            {number ? `Open PR #${number}` : "Open PR"}
          </a>
        ) : (
          <p className="muted">No pull request recorded for this run.</p>
        )}
      </section>
    </div>
  );
}
