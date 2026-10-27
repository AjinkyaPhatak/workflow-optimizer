import type { Metadata } from "next";
import { Bricolage_Grotesque } from "next/font/google";
import { Landing } from "@/components/landing/Landing";
import "./landing.css";

// Display font for the landing page's headings.
const display = Bricolage_Grotesque({ subsets: ["latin"], weight: ["400", "500", "600", "700", "800"], variable: "--lp-display" });

export const metadata: Metadata = {
  title: "Workflow Optimizer | AI workflows for everyday work",
  description: "Connect Gmail, Calendar, Sheets and AI into simple step-by-step workflows. No code, and free to start.",
};

export default function Home() {
  return <Landing fontClass={display.variable} />;
}
