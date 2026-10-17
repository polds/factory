import { useParams } from "@tanstack/react-router";
import { WorkItemBoard } from "./board";
import { WorkItemDetail } from "./detail";

export default function WorkItemsPage() {
  const params = useParams({ strict: false });
  const factoryId = typeof params.factory === "string" ? params.factory : "";
  const id = typeof params.id === "string" ? params.id : "";
  if (id) return <WorkItemDetail factoryId={factoryId} id={id} />;
  return <WorkItemBoard factoryId={factoryId} />;
}
