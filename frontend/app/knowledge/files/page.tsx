"use client";
import { Files } from "@/components/settings/Files";
import { KnowledgeTabs } from "@/components/settings/KnowledgeTabs";
import { TopBar } from "@/components/ui/TopBar";
import { useFiles } from "@/features/files/useFiles";
import { RequireAuth } from "@/lib/auth/RequireAuth";
function Page() { const state = useFiles(); return <><TopBar /><KnowledgeTabs /><Files state={state} /></>; }
export default function KnowledgeFilesPage() { return <RequireAuth><Page /></RequireAuth>; }
