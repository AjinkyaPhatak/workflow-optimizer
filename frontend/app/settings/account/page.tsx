"use client";

import { AccountSettings } from "@/components/settings/Account";
import { TopBar } from "@/components/ui/TopBar";
import { RequireAuth } from "@/lib/auth/RequireAuth";

export default function AccountPage() {
  return (
    <RequireAuth>
      <TopBar />
      <AccountSettings />
    </RequireAuth>
  );
}
