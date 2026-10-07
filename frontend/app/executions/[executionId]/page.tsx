"use client";

import { useParams } from "next/navigation";
import { ExecutionDebugger } from "@/components/executions/ExecutionDebugger";
import { RequireAuth } from "@/lib/auth/RequireAuth";

export default function ExecutionPage() {
  const { executionId } = useParams<{ executionId: string }>();
  return (
    <RequireAuth>
      <ExecutionDebugger executionId={executionId} />
    </RequireAuth>
  );
}
