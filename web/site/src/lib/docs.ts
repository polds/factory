// The docs are the repository's own Markdown. scripts/sync-docs.mjs copies
// README.md and docs/*.md into src/content/docs before every build, and Vite
// bundles them here as raw strings.
const files = import.meta.glob("/src/content/docs/*.md", {
  query: "?raw",
  import: "default",
  eager: true,
}) as Record<string, string>;

export type Doc = { slug: string; file: string; label: string; body: string };

export const REPO = "https://github.com/polds/factory";

function toSlug(file: string) {
  return file.replace(/\.md$/i, "").toLowerCase();
}

function labelOf(file: string, body: string) {
  if (toSlug(file) === "readme") return "Overview";
  const m = body.match(/^#\s+(.+)$/m);
  return m?.[1]?.trim() ?? file;
}

export const docs: Doc[] = Object.entries(files)
  .map(([path, body]) => {
    const file = path.split("/").pop()!;
    return { slug: toSlug(file), file, label: labelOf(file, body), body };
  })
  .sort((a, b) => {
    if (a.slug === "readme") return -1;
    if (b.slug === "readme") return 1;
    return a.file.localeCompare(b.file);
  });

export const getDoc = (slug: string) => docs.find((d) => d.slug === slug);

/**
 * Rewrite a relative Markdown link to a site route or a GitHub URL.
 *
 * Relative links are resolved against the source file's directory in the
 * repository: `docs/` for the design docs and the repository root for README.
 * A link to a Markdown file in `docs/` or to README becomes `/docs/<slug>`;
 * any other repository path (`spike/m0`, `../deploy/docker-compose.yml`)
 * becomes a GitHub link on `main`. Absolute URLs and `#anchors` pass through.
 */
export function rewriteHref(href: string | undefined, fromSlug: string): string | undefined {
  if (!href || /^[a-z][a-z0-9+.-]*:|^\/\/|^#|^\//i.test(href)) return href;
  const base = fromSlug === "readme" ? "https://x/" : "https://x/docs/";
  const url = new URL(href, base);
  const path = decodeURIComponent(url.pathname).replace(/^\//, "");
  const hash = url.hash;
  const md = path.match(/^(?:docs\/)?([^/]+)\.md$/i);
  if (md && (md[1]!.toLowerCase() === "readme" ? path === "README.md" : path.startsWith("docs/"))) {
    return `/docs/${toSlug(md[1]!)}${hash}`;
  }
  const tree = /\.[a-z0-9]+$/i.test(path) ? "blob" : "tree";
  return `${REPO}/${tree}/main/${path}${hash}`;
}

export type Heading = { depth: 2 | 3; text: string; id: string };
