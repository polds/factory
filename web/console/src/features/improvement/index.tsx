import { EmptyState } from "../../components/EmptyState";
import { PageHeader } from "../../components/PageHeader";

export default function ImprovementPage() {
  return (
    <div className="operator-page">
      <PageHeader title="Improvement" />
      <EmptyState title="Nothing here yet" message="This page lands in a later change." />
    </div>
  );
}
