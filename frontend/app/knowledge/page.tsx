import { redirect } from "next/navigation";

// Knowledge bases now live in the Library, together with files.
export default function KnowledgePage() {
  redirect("/library");
}
