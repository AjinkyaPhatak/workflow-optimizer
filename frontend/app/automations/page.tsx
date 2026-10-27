"use client";
import { Automations } from "@/components/assistant/Automations";
import { TopBar } from "@/components/ui/TopBar";
import { RequireAuth } from "@/lib/auth/RequireAuth";
export default function AutomationsPage() { return <RequireAuth><TopBar /><Automations /></RequireAuth>; }
