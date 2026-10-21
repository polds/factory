import { useParams } from "@tanstack/react-router";
import { EmptyState } from "../../components/EmptyState";
import { PageHeader } from "../../components/PageHeader";

export default function ScorersPage() {
  const params = useParams({ strict: false });
  const name = typeof params.name === "string" ? params.name : "";
  return (
    <div className="operator-page">
      <PageHeader title="Scorers" subtitle={name || undefined} />
      <EmptyState title="Nothing here yet" message="This page lands in a later change." />
    </div>
  );
}
