import { Link } from "react-router";

import { Footer, SiteNav } from "../components/SiteNav.tsx";

export function NotFound() {
  return (
    <div className="flex min-h-screen flex-col">
      <SiteNav />
      <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-16 sm:px-6">
        <h1 className="text-xl font-medium">Not found</h1>
        <p className="mt-3 text-muted-foreground">There is no page at this address.</p>
        <p className="mt-6 font-mono text-sm">
          <Link to="/" className="text-link underline underline-offset-4">
            home
          </Link>
          {" · "}
          <Link to="/docs" className="text-link underline underline-offset-4">
            docs
          </Link>
        </p>
      </main>
      <Footer />
    </div>
  );
}
