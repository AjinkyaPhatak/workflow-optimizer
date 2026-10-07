"use client";

import { useParams } from "next/navigation";
import { WorkflowEditor } from "@/components/workflow/WorkflowEditor";
import { RequireAuth } from "@/lib/auth/RequireAuth";

export default function EditorPage() {
  const { workflowId } = useParams<{ workflowId: string }>();
  return (
    <RequireAuth>
      <WorkflowEditor workflowId={workflowId} />
    </RequireAuth>
  );
}
