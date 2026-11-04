# web/site

The public website, https://factory.polds.dev. A static Vite + React SPA:
a home page and the design docs rendered from the repository's own
Markdown.

The initial design was produced with Lovable and vendored here; the
repository is the source of truth from this point on.

## Content

`npm run build` (and `npm run dev`) first runs `scripts/sync-docs.mjs`,
which copies `README.md` and `docs/*.md` into `src/content/docs/`. That
directory is generated and ignored; edit the repository docs, not the
copies. The sidebar order is the filename order; the label is each file's
first `#` heading.

## Publishing

`.github/workflows/site.yml` builds on pull requests and deploys on push
to `main` through GitHub Pages (source: GitHub Actions). The build writes
`dist/CNAME` (`factory.polds.dev`), `dist/404.html` (SPA fallback) and
`dist/.nojekyll`.

One-time settings in the repository: Pages source set to GitHub Actions,
custom domain `factory.polds.dev`, and a DNS CNAME from `factory` to
`polds.github.io`.

## Local

```bash
npm ci
npm run dev
```
