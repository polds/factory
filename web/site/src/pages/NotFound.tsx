import { Link } from "react-router";

export function NotFound() {
  return (
    <div className="bg-grid flex min-h-screen items-center justify-center px-4">
      <div className="max-w-md text-center">
        <p className="label-mono">error 404</p>
        <h1 className="mt-3 text-3xl font-semibold">No such route</h1>
        <Link
          to="/"
          className="mt-6 inline-block bg-primary px-4 py-2 font-mono text-sm text-primary-foreground"
        >
          cd ~
        </Link>
      </div>
    </div>
  );
}
