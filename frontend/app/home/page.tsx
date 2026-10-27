"use client";
import { AssistantHome } from "@/components/assistant/AssistantHome";
import { TopBar } from "@/components/ui/TopBar";
import { RequireAuth } from "@/lib/auth/RequireAuth";
export default function HomePage() { return <RequireAuth><TopBar /><AssistantHome /></RequireAuth>; }
