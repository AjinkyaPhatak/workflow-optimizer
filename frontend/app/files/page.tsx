import { redirect } from "next/navigation";

// Files now live in the Library, together with knowledge bases.
export default function FilesPage() {
  redirect("/library");
}
