import { redirect } from "next/navigation";

// Preserve bookmarks to the old Files section of Knowledge.
export default function KnowledgeFilesPage() {
  redirect("/library");
}
