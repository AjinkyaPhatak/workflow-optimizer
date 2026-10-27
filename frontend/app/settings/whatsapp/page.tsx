"use client";
import { WhatsAppSettings } from "@/components/settings/WhatsApp";
import { TopBar } from "@/components/ui/TopBar";
import { RequireAuth } from "@/lib/auth/RequireAuth";
export default function WhatsAppPage() { return <RequireAuth><TopBar /><WhatsAppSettings /></RequireAuth>; }
