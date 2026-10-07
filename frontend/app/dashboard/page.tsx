import { redirect } from "next/navigation";

// The workflow list is the dashboard for this phase.
export default function Dashboard() {
  redirect("/workflows");
}
