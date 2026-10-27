"use client";

import { useParams } from "next/navigation";
import { WorkflowEditorRoute } from "@/components/workflow/WorkflowEditorRoute";
import { RequireAuth } from "@/lib/auth/RequireAuth";

export default function EditorPage() {
  const { workflowId } = useParams<{ workflowId: string }>();
  return (
    <RequireAuth>
      <WorkflowEditorRoute key={workflowId} workflowId={workflowId} />
    </RequireAuth>
  );
}
