import { useParams } from "@tanstack/react-router";
import { EmptyState } from "../../components/EmptyState";
import { PageHeader } from "../../components/PageHeader";

export default function BenchmarksPage() {
  const params = useParams({ strict: false });
  const suite = typeof params.suite === "string" ? params.suite : "";
  return (
    <div className="operator-page">
      <PageHeader title="Benchmarks" subtitle={suite || undefined} />
      <EmptyState title="Nothing here yet" message="This page lands in a later change." />
    </div>
  );
}
