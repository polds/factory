import { useParams } from "@tanstack/react-router";
import { RunDetailPage } from "./detail-page";
import { RunsListPage } from "./list-page";
import "./runs.css";

export default function RunsPage() {
  const params = useParams({ strict: false });
  const runId = "runId" in params && typeof params.runId === "string" ? params.runId : "";
  if (runId) return <RunDetailPage runId={runId} />;
  return <RunsListPage />;
}
