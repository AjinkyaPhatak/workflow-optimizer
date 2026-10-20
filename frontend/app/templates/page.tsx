"use client";

import { TemplateGallery } from "@/components/templates/TemplateGallery";
import { TopBar } from "@/components/ui/TopBar";
import { RequireAuth } from "@/lib/auth/RequireAuth";

export default function TemplatesPage() {
  return (
    <RequireAuth>
      <TopBar />
      <TemplateGallery />
    </RequireAuth>
  );
}
