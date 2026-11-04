import { lazy, Suspense, useEffect } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router";

import { Home } from "./pages/Home.tsx";
import { NotFound } from "./pages/NotFound.tsx";

// The docs page carries the Markdown pipeline and highlight.js; keep it off the
// home page's bundle.
const DocPage = lazy(() => import("./pages/Doc.tsx").then((m) => ({ default: m.DocPage })));

function ScrollToTop() {
  const { pathname, hash } = useLocation();
  useEffect(() => {
    if (hash) {
      document.getElementById(decodeURIComponent(hash.slice(1)))?.scrollIntoView();
      return;
    }
    window.scrollTo(0, 0);
  }, [pathname, hash]);
  return null;
}

export function App() {
  return (
    <>
      <ScrollToTop />
      <Routes>
        <Route path="/" element={<Home />} />
        <Route path="/docs" element={<Navigate to="/docs/readme" replace />} />
        <Route
          path="/docs/:slug"
          element={
            <Suspense fallback={<div className="min-h-screen" />}>
              <DocPage />
            </Suspense>
          }
        />
        <Route path="*" element={<NotFound />} />
      </Routes>
    </>
  );
}
