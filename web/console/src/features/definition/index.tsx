import * as Dialog from "@radix-ui/react-dialog";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import {
  AlertCircle,
  CheckCircle2,
  ExternalLink,
  FileJson,
  GitBranch,
  Pencil,
  Upload,
  XCircle,
} from "lucide-react";
import { useRef, useState } from "react";
import { client } from "../../api/client";
import type { DefinitionFile, Diagnostic } from "../../api/types";
import { Button } from "../../components/Button";
import { EmptyState } from "../../components/EmptyState";
import { ErrorState } from "../../components/ErrorState";
import { applyDefinitionTree } from "./apply";
import { CodeEditor } from "./editor";
import { FileTree } from "./file-tree";
import {
  cloneFiles,
  diffFiles,
  errorCount,
  filesEqual,
  filesKey,
  schemaVersion,
  shortSha,
} from "./model";

function severityLabel(severity: string): string {
  if (!severity) return "Diagnostic";
  return severity.charAt(0).toUpperCase() + severity.slice(1).toLowerCase();
}

function ApplyDialog({
  open,
  diff,
  busy,
  error,
  onOpenChange,
  onConfirm,
}: {
  open: boolean;
  diff: ReturnType<typeof diffFiles>;
  busy: boolean;
  error: string | null;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => void;
}) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content className="dialog-content">
          <Dialog.Title>Apply definition</Dialog.Title>
          <Dialog.Description>
            Preview of file changes. Apply is atomic: all files change together, or none do.
          </Dialog.Description>
          <DiffList label="Created" paths={diff.created} />
          <DiffList label="Updated" paths={diff.updated} />
          <DiffList label="Deleted" paths={diff.deleted} />
          {error ? (
            <p role="alert" className="mt-3 text-sm text-[var(--danger)]">
              {error}
            </p>
          ) : null}
          <div className="dialog-actions">
            <Dialog.Close asChild>
              <Button type="button">Cancel</Button>
            </Dialog.Close>
            <Button type="button" disabled={busy} onClick={onConfirm}>
              Confirm apply
            </Button>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function DiffList({ label, paths }: { label: string; paths: readonly string[] }) {
  return (
    <section className="mt-3">
      <h3 className="text-sm font-semibold">
        {label} ({paths.length})
      </h3>
      {paths.length === 0 ? (
        <p className="text-sm text-[var(--muted-foreground)]">None</p>
      ) : (
        <ul className="mt-1 list-disc pl-5 font-mono text-xs">
          {paths.map((path) => (
            <li key={path}>{path}</li>
          ))}
        </ul>
      )}
    </section>
  );
}

function DefinitionScreen({ factoryId }: { factoryId: string }) {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["definition", factoryId],
    queryFn: () => client.getDefinition(factoryId),
    enabled: factoryId.length > 0,
  });
  const [draft, setDraft] = useState<DefinitionFile[] | null>(null);
  const [baseline, setBaseline] = useState<DefinitionFile[] | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [validatedKey, setValidatedKey] = useState<string | null>(null);
  const [diagnostics, setDiagnostics] = useState<Diagnostic[]>([]);
  const [mismatch, setMismatch] = useState(false);
  const [applyOpen, setApplyOpen] = useState(false);
  const [busy, setBusy] = useState<"validate" | "apply" | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [newPath, setNewPath] = useState("");
  const [currentLine, setCurrentLine] = useState(1);
  const [focusNonce, setFocusNonce] = useState(0);
  const applyLock = useRef(false);
  const serverFiles = query.data ? cloneFiles(query.data.files ?? []) : null;
  const files = draft ?? serverFiles ?? [];
  const version = schemaVersion(files);
  useQuery({
    queryKey: ["definition-schema", version],
    queryFn: async () => {
      const response = await fetch(
        `/api/v1/factory-files/schemas/${encodeURIComponent(version ?? "")}`,
        { headers: { Accept: "application/json" } },
      );
      if (!response.ok) throw new Error(`schema ${response.status}`);
      return true;
    },
    enabled: Boolean(version),
    retry: false,
  });

  if (query.isPending) return <p>Loading definition</p>;
  if (query.isError || !query.data) {
    return (
      <ErrorState
        title="Could not load the definition"
        message="The definition could not be loaded."
        onRetry={() => void query.refetch()}
      />
    );
  }

  const view = query.data;
  const readOnly = view.source === "git" || !view.editable;
  const base = baseline ?? serverFiles ?? [];
  const activePath =
    selected && files.some((file) => file.path === selected) ? selected : (files[0]?.path ?? null);
  const selectedFile = files.find((file) => file.path === activePath) ?? null;
  const currentKey = filesKey(files);
  const validated = validatedKey !== null && validatedKey === currentKey;
  const errors = errorCount(diagnostics);
  const canApply = !readOnly && validated && errors === 0 && busy === null;
  const applyReason = !validated
    ? "Validate before applying"
    : errors === 1
      ? "Fix 1 error to apply"
      : errors > 1
        ? `Fix ${errors} errors to apply`
        : null;
  const errorPaths = new Set(diagnostics.map((diagnostic) => diagnostic.file));
  const diff = diffFiles(base, files);

  function updateSelected(content: string) {
    setDraft(files.map((file) => (file.path === activePath ? { ...file, content } : file)));
  }

  function addFile(path: string) {
    const trimmed = path.trim();
    if (!trimmed) return;
    if (files.some((file) => file.path === trimmed)) {
      setActionError(`A file named ${trimmed} is already in the tree.`);
      return;
    }
    setActionError(null);
    setDraft([...files, { path: trimmed, content: "" }]);
    setSelected(trimmed);
    setCurrentLine(1);
    setNewPath("");
  }

  function deleteSelected() {
    if (!activePath) return;
    const next = files.filter((file) => file.path !== activePath);
    setDraft(next);
    setSelected(next[0]?.path ?? null);
    setCurrentLine(1);
  }

  async function onValidate() {
    if (readOnly || busy) return;
    setBusy("validate");
    setActionError(null);
    try {
      const result = await client.validateDefinition(
        factoryId,
        JSON.stringify({
          files: files.map((file) => ({ path: file.path, content: file.content })),
        }),
      );
      setValidatedKey(filesKey(files));
      setDiagnostics(result.diagnostics ?? []);
    } catch (error) {
      setActionError(error instanceof Error ? error.message : "Validation failed");
    } finally {
      setBusy(null);
    }
  }

  async function onApply() {
    if (applyLock.current || !canApply) return;
    applyLock.current = true;
    setBusy("apply");
    setActionError(null);
    const applied = cloneFiles(files);
    try {
      const result = await applyDefinitionTree(factoryId, applied);
      const diags = result.diagnostics ?? [];
      setDiagnostics(diags);
      const next = await queryClient.fetchQuery({
        queryKey: ["definition", factoryId],
        queryFn: () => client.getDefinition(factoryId),
      });
      const nextFiles = cloneFiles(next.files ?? []);
      if (!filesEqual(nextFiles, applied)) {
        setMismatch(true);
      } else {
        setMismatch(false);
        setDraft(nextFiles);
        setBaseline(cloneFiles(nextFiles));
        setValidatedKey(errorCount(diags) > 0 ? filesKey(nextFiles) : null);
      }
      setApplyOpen(false);
    } catch (error) {
      setActionError(error instanceof Error ? error.message : "Apply failed");
    } finally {
      applyLock.current = false;
      setBusy(null);
    }
  }

  function focusApply() {
    const buttons = document.querySelectorAll("button");
    for (const button of buttons) {
      if (button.textContent?.replace(/\s+/g, " ").trim() === "Apply") {
        button.focus();
        return;
      }
    }
  }

  function focusDiagnostic(diagnostic: Diagnostic) {
    setSelected(diagnostic.file);
    setCurrentLine(diagnostic.line);
    setFocusNonce((nonce) => nonce + 1);
  }

  return (
    <div>
      <header className="mb-4 flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">Definition</h1>
          <p className="text-sm text-[var(--muted-foreground)]">
            Factory <span className="font-mono">{factoryId}</span>
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {version ? (
            <a
              href={`/api/v1/factory-files/schemas/${encodeURIComponent(version)}`}
              className="inline-flex items-center gap-1 border border-[var(--border)] px-2 py-1 font-mono text-xs hover:underline"
            >
              <FileJson className="size-3.5" aria-hidden />
              Schema: {version}
            </a>
          ) : null}
          {readOnly ? null : (
            <>
              <Button type="button" onClick={() => void onValidate()} disabled={busy !== null}>
                <CheckCircle2 className="size-4" aria-hidden /> Validate
              </Button>
              {applyReason ? (
                <span
                  id="apply-reason"
                  className="inline-flex items-center gap-1 text-sm text-[var(--danger)]"
                >
                  <XCircle className="size-4" aria-hidden />
                  {applyReason}
                </span>
              ) : null}
              <Button
                type="button"
                disabled={!canApply}
                aria-describedby={applyReason ? "apply-reason" : undefined}
                onClick={() => setApplyOpen(true)}
              >
                <Upload className="size-4" aria-hidden /> Apply
              </Button>
            </>
          )}
        </div>
      </header>

      {readOnly ? (
        <section
          aria-label="Definition source"
          className="mb-4 flex flex-wrap items-center gap-x-4 gap-y-2 border-2 border-[var(--foreground)] bg-[var(--card)] p-3"
        >
          <GitBranch className="size-5 shrink-0" aria-hidden />
          <p className="font-semibold">Read-only. Changes go through a pull request.</p>
          <p className="text-sm">
            Production ref <span className="font-mono">{view.production_ref}</span>
            {" @ "}
            <span className="font-mono">{shortSha(view.git_sha)}</span>
          </p>
          {view.forge_url ? (
            <a
              href={view.forge_url}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-sm text-[var(--ring)] hover:underline"
            >
              <ExternalLink className="size-4" aria-hidden />
              View on forge
            </a>
          ) : null}
        </section>
      ) : (
        <section
          aria-label="Definition source"
          className="mb-4 flex flex-wrap items-center gap-2 border border-dashed border-[var(--border)] bg-[var(--card)] p-3"
        >
          <Pencil className="size-4" aria-hidden />
          <p className="font-semibold">Local definition — editable</p>
          <p className="text-sm text-[var(--muted-foreground)]">
            Stored on the control plane. Validate, then Apply to take effect.
          </p>
        </section>
      )}

      {mismatch ? (
        <p role="alert" className="mb-3 border border-[var(--danger)] px-3 py-2 text-sm">
          The server applied a different definition than your edits
        </p>
      ) : null}
      {actionError ? (
        <p role="alert" className="mb-3 text-sm text-[var(--danger)]">
          {actionError}
        </p>
      ) : null}

      {files.length === 0 ? (
        <EmptyState title="No definition files" message="This factory has no files to show." />
      ) : (
        <div className="grid items-start gap-3 lg:grid-cols-[16rem_minmax(0,1fr)]">
          <FileTree
            paths={files.map((file) => file.path)}
            selected={activePath}
            errorPaths={errorPaths}
            onSelect={(path) => {
              setSelected(path);
              setCurrentLine(1);
            }}
          />
          <div className="min-w-0">
            {selectedFile ? (
              <>
                <div className="flex items-center justify-between border border-b-0 border-[var(--border)] bg-[var(--muted)] px-3 py-1.5 font-mono text-xs">
                  <span>{selectedFile.path}</span>
                  <span className="text-[var(--muted-foreground)]">
                    {readOnly ? "read-only" : "editing"} · YAML
                  </span>
                </div>
                <CodeEditor
                  path={selectedFile.path}
                  value={selectedFile.content}
                  readOnly={readOnly}
                  diagnostics={diagnostics}
                  currentLine={currentLine}
                  onLine={setCurrentLine}
                  onChange={updateSelected}
                  focusNonce={focusNonce}
                />
              </>
            ) : (
              <EmptyState title="No file selected" message="Choose a file from the tree." />
            )}
            {readOnly ? null : (
              <section
                aria-labelledby="diagnostics-heading"
                className="mt-3 border border-[var(--border)] bg-[var(--card)]"
              >
                <h2
                  id="diagnostics-heading"
                  className="flex items-center gap-2 border-b border-[var(--border)] px-3 py-2 text-sm font-semibold"
                >
                  Diagnostics
                  <span className="font-mono text-xs font-normal">
                    {errors} {errors === 1 ? "error" : "errors"}
                  </span>
                </h2>
                {diagnostics.length === 0 ? (
                  <p className="flex items-center gap-1.5 px-3 py-2 text-sm">
                    <CheckCircle2 className="size-4" aria-hidden />
                    No errors. Ready to apply.
                  </p>
                ) : (
                  <ul>
                    {diagnostics.map((diagnostic) => (
                      <li
                        key={`${diagnostic.file}:${diagnostic.line}:${diagnostic.severity}:${diagnostic.message}`}
                      >
                        <button
                          type="button"
                          className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm hover:bg-[var(--muted)]"
                          onClick={() => focusDiagnostic(diagnostic)}
                        >
                          {diagnostic.severity.toLowerCase() === "error" ? (
                            <XCircle className="size-4 shrink-0 text-[var(--danger)]" aria-hidden />
                          ) : (
                            <AlertCircle className="size-4 shrink-0" aria-hidden />
                          )}
                          <span className="font-medium">{severityLabel(diagnostic.severity)}</span>
                          <span className="font-mono text-xs">
                            {diagnostic.file}:{diagnostic.line}
                          </span>
                          <span>{diagnostic.message}</span>
                        </button>
                      </li>
                    ))}
                  </ul>
                )}
                <p className="flex items-center gap-1 border-t border-[var(--border)] px-3 py-1.5 text-xs text-[var(--muted-foreground)]">
                  <AlertCircle className="size-3.5" aria-hidden />
                  Apply is atomic: all resources change together, or none do.
                </p>
              </section>
            )}
            {readOnly ? null : (
              <div className="mt-3 flex flex-wrap items-end gap-2">
                <form
                  className="flex flex-wrap items-end gap-2"
                  onSubmit={(event) => {
                    event.preventDefault();
                    addFile(newPath);
                  }}
                >
                  <label className="grid gap-1 text-sm">
                    New file path
                    <input
                      value={newPath}
                      onChange={(event) => setNewPath(event.target.value)}
                      className="border border-[var(--border)] bg-[var(--card)] px-2 py-1 font-mono text-sm"
                    />
                  </label>
                  <Button type="submit">Add file</Button>
                </form>
                <Button type="button" tone="danger" onClick={deleteSelected} disabled={!activePath}>
                  Delete file
                </Button>
              </div>
            )}
          </div>
        </div>
      )}

      {readOnly ? null : (
        <ApplyDialog
          open={applyOpen}
          diff={diff}
          busy={busy === "apply"}
          error={actionError}
          onOpenChange={(open) => {
            setApplyOpen(open);
            if (!open) window.setTimeout(() => focusApply(), 0);
          }}
          onConfirm={() => void onApply()}
        />
      )}
    </div>
  );
}

export default function DefinitionPage() {
  const params = useParams({ strict: false });
  const factoryId = typeof params.factory === "string" ? params.factory : "";
  if (!factoryId) {
    return <ErrorState title="Missing factory" message="Open a definition from a factory." />;
  }
  return <DefinitionScreen key={factoryId} factoryId={factoryId} />;
}
