import GithubSlugger from "github-slugger";
import { ArrowLeft, ArrowRight, Menu, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import ReactMarkdown from "react-markdown";
import { Link, useParams } from "react-router";
import rehypeHighlight from "rehype-highlight";
import rehypeSlug from "rehype-slug";
import remarkGfm from "remark-gfm";
import type { Root } from "mdast";
import { visit } from "unist-util-visit";

import { Footer, SiteNav } from "../components/SiteNav.tsx";
import { docs, getDoc, rewriteHref, type Heading } from "../lib/docs.ts";
import { NotFound } from "./NotFound.tsx";

// The docs write placeholders like `<name>` in prose. Raw HTML is not rendered
// on purpose; turn those nodes into text so the words still show.
function remarkHtmlAsText() {
  return (tree: Root) => {
    visit(tree, "html", (node) => {
      const n = node as unknown as { type: string; value: string };
      n.type = "text";
    });
  };
}

function extractHeadings(md: string): Heading[] {
  const slugger = new GithubSlugger();
  const out: Heading[] = [];
  let inFence = false;
  for (const line of md.split("\n")) {
    if (/^\s*(```|~~~)/.test(line)) inFence = !inFence;
    if (inFence) continue;
    const m = line.match(/^(#{1,3})\s+(.+?)\s*#*$/);
    if (!m) continue;
    const text = (m[2] ?? "").replace(/\[([^\]]+)\]\([^)]*\)/g, "$1").replace(/[`*_]/g, "");
    const id = slugger.slug(text);
    const depth = m[1]?.length ?? 0;
    if (depth >= 2) out.push({ depth: depth as 2 | 3, text, id });
  }
  return out;
}

function Sidebar({ slug, onNavigate }: { slug: string; onNavigate?: () => void }) {
  return (
    <nav className="space-y-0.5" aria-label="Documentation">
      <p className="label-mono mb-3">documentation</p>
      {docs.map((d) => (
        <Link
          key={d.slug}
          to={`/docs/${d.slug}`}
          onClick={onNavigate}
          aria-current={d.slug === slug ? "page" : undefined}
          className={`block border-l px-3 py-1.5 text-sm ${
            d.slug === slug
              ? "border-primary text-primary"
              : "border-border text-muted-foreground hover:border-foreground hover:text-foreground"
          }`}
        >
          {d.label}
        </Link>
      ))}
    </nav>
  );
}

function useDocumentTitle(title: string) {
  useEffect(() => {
    document.title = title;
  }, [title]);
}

export function DocPage() {
  const { slug = "" } = useParams();
  const doc = getDoc(slug);
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState<string>();
  const headings = useMemo(() => (doc ? extractHeadings(doc.body) : []), [doc]);
  useDocumentTitle(doc ? `${doc.label} — factory docs` : "Not found — factory docs");

  useEffect(() => {
    const els = headings
      .map((h) => document.getElementById(h.id))
      .filter((el): el is HTMLElement => el !== null);
    const obs = new IntersectionObserver(
      (entries) => entries.forEach((e) => e.isIntersecting && setActive(e.target.id)),
      { rootMargin: "-80px 0px -70% 0px" },
    );
    els.forEach((el) => obs.observe(el));
    return () => obs.disconnect();
  }, [headings]);

  useEffect(() => {
    document.body.style.overflow = open ? "hidden" : "";
    return () => {
      document.body.style.overflow = "";
    };
  }, [open]);

  if (!doc) return <NotFound />;

  const idx = docs.findIndex((d) => d.slug === slug);
  const prev = docs[idx - 1];
  const next = docs[idx + 1];

  return (
    <div className="min-h-screen">
      <SiteNav
        left={
          <button
            type="button"
            onClick={() => setOpen(true)}
            aria-label="Open docs menu"
            className="-ml-1 p-1 text-muted-foreground lg:hidden"
          >
            <Menu size={18} />
          </button>
        }
      />

      {open && (
        <div className="fixed inset-0 z-50 lg:hidden">
          <div
            className="absolute inset-0 bg-background/80 backdrop-blur-sm"
            onClick={() => setOpen(false)}
          />
          <div className="absolute inset-y-0 left-0 w-72 max-w-[85vw] overflow-y-auto border-r border-border bg-background p-5">
            <button
              type="button"
              onClick={() => setOpen(false)}
              aria-label="Close menu"
              className="mb-4 text-muted-foreground"
            >
              <X size={18} />
            </button>
            <Sidebar slug={slug} onNavigate={() => setOpen(false)} />
          </div>
        </div>
      )}

      <div className="mx-auto grid max-w-7xl gap-10 px-4 sm:px-6 lg:grid-cols-[14rem_minmax(0,1fr)] xl:grid-cols-[14rem_minmax(0,1fr)_12rem]">
        <aside className="sticky top-14 hidden h-[calc(100vh-3.5rem)] overflow-y-auto py-10 lg:block">
          <Sidebar slug={slug} />
        </aside>

        <main className="min-w-0 py-10">
          <p className="label-mono mb-6 lg:hidden">
            <span className="text-primary">docs</span> / {doc.file}
          </p>
          <article className="prose-doc">
            <ReactMarkdown
              remarkPlugins={[remarkGfm, remarkHtmlAsText]}
              rehypePlugins={[rehypeSlug, [rehypeHighlight, { detect: false }]]}
              components={{
                a: ({ href, children, node: _n, ...rest }) => {
                  const h = rewriteHref(href, slug);
                  if (h?.startsWith("/docs/")) {
                    return (
                      <Link to={h} {...rest}>
                        {children}
                      </Link>
                    );
                  }
                  return (
                    <a href={h} {...rest}>
                      {children}
                    </a>
                  );
                },
                table: ({ node: _n, ...p }) => (
                  <div className="table-wrap">
                    <table {...p} />
                  </div>
                ),
              }}
            >
              {doc.body}
            </ReactMarkdown>
          </article>

          <div className="mt-16 grid gap-3 border-t border-border pt-6 sm:grid-cols-2">
            {prev ? (
              <Link
                to={`/docs/${prev.slug}`}
                className="border border-border p-4 hover:border-primary"
              >
                <span className="label-mono flex items-center gap-1">
                  <ArrowLeft size={12} /> previous
                </span>
                <span className="mt-1 block text-sm">{prev.label}</span>
              </Link>
            ) : (
              <span />
            )}
            {next && (
              <Link
                to={`/docs/${next.slug}`}
                className="border border-border p-4 text-right hover:border-primary"
              >
                <span className="label-mono flex items-center justify-end gap-1">
                  next <ArrowRight size={12} />
                </span>
                <span className="mt-1 block text-sm">{next.label}</span>
              </Link>
            )}
          </div>
        </main>

        <aside className="sticky top-14 hidden h-[calc(100vh-3.5rem)] overflow-y-auto py-10 xl:block">
          {headings.length > 0 && (
            <>
              <p className="label-mono mb-3">on this page</p>
              <ul className="space-y-1.5 text-xs">
                {headings.map((h) => (
                  <li key={h.id} className={h.depth === 3 ? "pl-3" : ""}>
                    <a
                      href={`#${h.id}`}
                      className={
                        active === h.id
                          ? "text-primary"
                          : "text-muted-foreground hover:text-foreground"
                      }
                    >
                      {h.text}
                    </a>
                  </li>
                ))}
              </ul>
            </>
          )}
        </aside>
      </div>
      <Footer />
    </div>
  );
}
