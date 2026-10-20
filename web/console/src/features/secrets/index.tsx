import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { AlertCircle, Bot, CheckCircle2, Cpu, Plug, ShieldCheck, Webhook } from "lucide-react";
import { client } from "../../api/client";
import type { Identity, SecretPresence } from "../../api/types";
import { EmptyState } from "../../components/EmptyState";
import { ErrorState } from "../../components/ErrorState";

type UsedBy = { kind: string; name: string };

type SecretRow = {
  name: string;
  provider: string;
  present: boolean;
  used_by: UsedBy[];
  last_used_run_id?: string;
};

type IdentityRow = {
  subject: string;
  kind: string;
  scope: string;
  last_used_at: string | null;
};

function presentSecret(raw: SecretPresence): SecretRow {
  return {
    name: raw.name,
    provider: raw.provider,
    present: raw.present,
    used_by: (raw.used_by ?? []).map((used) => ({ kind: used.kind, name: used.name })),
    last_used_run_id: raw.last_used_run_id,
  };
}

function presentIdentity(raw: Identity): IdentityRow {
  return {
    subject: raw.subject,
    kind: raw.kind,
    scope: raw.scope,
    last_used_at: raw.last_used_at,
  };
}

function kindMark(kind: string) {
  const className = "size-3.5";
  if (kind === "agent") return <Bot className={className} aria-hidden />;
  if (kind === "webhook") return <Webhook className={className} aria-hidden />;
  if (kind === "harness") return <Cpu className={className} aria-hidden />;
  return <Plug className={className} aria-hidden />;
}

function formatWhen(iso: string | null): string {
  if (!iso) return "—";
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return iso;
  const minutes = Math.round((Date.now() - then) / 60000);
  if (Math.abs(minutes) < 1) return "just now";
  if (Math.abs(minutes) < 60) return `${minutes}m`;
  const hours = Math.round(minutes / 60);
  if (Math.abs(hours) < 48) return `${hours}h`;
  return `${Math.round(hours / 24)}d`;
}

function SecretsScreen({ factoryId }: { factoryId: string }) {
  const secrets = useQuery({
    queryKey: ["secrets", factoryId],
    queryFn: async () => (await client.listSecrets(factoryId)).map(presentSecret),
  });
  const identities = useQuery({
    queryKey: ["identities"],
    queryFn: async () => (await client.listIdentities()).map(presentIdentity),
  });

  if (secrets.isPending || identities.isPending) return <p>Loading secrets</p>;
  if (secrets.isError || identities.isError) {
    return (
      <ErrorState
        title="Could not load secrets"
        message="Secret names or identities could not be loaded."
        onRetry={() => {
          void secrets.refetch();
          void identities.refetch();
        }}
      />
    );
  }

  const rows = secrets.data;
  const missing = rows.filter((row) => !row.present).length;
  return (
    <div>
      <header className="page-header mb-5">
        <div>
          <h1>Secrets</h1>
          <p className="muted">
            Factory <span className="font-mono">{factoryId}</span> · {rows.length} referenced
            {missing > 0 ? ` · ${missing} missing` : ""}
          </p>
        </div>
      </header>
      <p className="mb-3 flex items-center gap-2 border-2 border-foreground bg-card px-3 py-2 font-medium">
        <ShieldCheck className="size-5 shrink-0" aria-hidden />
        Values are never shown. This page lists names, providers and presence only.
      </p>
      {rows.length === 0 ? (
        <EmptyState
          title="No secrets referenced"
          message="This factory definition does not reference any secrets."
        />
      ) : (
        <div className="panel table-scroll">
          <table className="w-full border-collapse text-sm">
            <caption className="sr-only">Secret names and presence</caption>
            <thead className="data-head">
              <tr>
                {["Name", "Provider", "Presence", "Used by", "Last used run"].map((heading) => (
                  <th key={heading} scope="col" className="px-3 py-2 whitespace-nowrap">
                    {heading}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr
                  key={row.name}
                  className={
                    row.present
                      ? "border-b border-border last:border-b-0"
                      : "border-b border-border bg-muted last:border-b-0"
                  }
                >
                  <td className="px-3 py-2 font-mono text-xs font-semibold">{row.name}</td>
                  <td className="px-3 py-2 font-mono text-xs">{row.provider}</td>
                  <td className="px-3 py-2 whitespace-nowrap">
                    {row.present ? (
                      <span className="inline-flex items-center gap-1">
                        <CheckCircle2 className="size-4" aria-hidden /> Present
                      </span>
                    ) : (
                      <span className="inline-flex items-center gap-1">
                        <AlertCircle className="size-4 text-danger" aria-hidden /> Missing
                      </span>
                    )}
                  </td>
                  <td className="px-3 py-2">
                    <ul className="flex flex-wrap gap-1">
                      {row.used_by.map((used) => (
                        <li
                          key={`${used.kind}:${used.name}`}
                          className="inline-flex items-center gap-1 border border-border bg-muted px-1.5 text-xs"
                        >
                          {kindMark(used.kind)}
                          <span className="text-muted-foreground">{used.kind}</span>
                          <span className="font-mono">{used.name}</span>
                        </li>
                      ))}
                    </ul>
                  </td>
                  <td className="px-3 py-2">
                    {row.last_used_run_id ? (
                      <Link
                        to="/runs/$runId"
                        params={{ runId: row.last_used_run_id }}
                        className="font-mono text-xs text-link hover:underline"
                      >
                        {row.last_used_run_id}
                      </Link>
                    ) : (
                      <span className="text-muted-foreground">—</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <section
        className="mt-5 border border-border bg-card p-3"
        aria-labelledby="identities-heading"
      >
        <h2 id="identities-heading" className="mb-2 text-sm font-semibold">
          Identities (read-only)
        </h2>
        {identities.data.length === 0 ? (
          <p className="text-sm text-muted-foreground">No identities.</p>
        ) : (
          <table className="w-full border-collapse text-sm">
            <caption className="sr-only">Identities</caption>
            <thead className="text-xs text-muted-foreground">
              <tr>
                {["Subject", "Kind", "Scope", "Last used"].map((heading) => (
                  <th key={heading} scope="col" className="py-1.5 pr-3 font-medium">
                    {heading}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {identities.data.map((identity) => (
                <tr key={identity.subject} className="border-b border-border last:border-b-0">
                  <td className="py-2 pr-3 font-medium">{identity.subject}</td>
                  <td className="py-2 pr-3 font-mono text-xs">{identity.kind}</td>
                  <td className="py-2 pr-3 font-mono text-xs">{identity.scope}</td>
                  <td className="py-2 font-mono text-xs">
                    {identity.last_used_at ? (
                      <time dateTime={identity.last_used_at}>
                        {formatWhen(identity.last_used_at)}
                      </time>
                    ) : (
                      "—"
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </div>
  );
}

export default function SecretsPage() {
  const params = useParams({ strict: false });
  const factoryId = typeof params.factory === "string" ? params.factory : "";
  if (!factoryId)
    return <ErrorState title="Missing factory" message="Open secrets from a factory." />;
  return <SecretsScreen factoryId={factoryId} />;
}
