// "Show technical details" (Phase H5): off by default, so the editor shows
// plain language only; on, it also shows error codes, data types and step
// IDs. Remembered per browser (a convenience; it works without storage).

import { create } from "zustand";

const KEY = "wo.showTechnicalDetails";

function stored(): boolean {
  try {
    return typeof window !== "undefined" && window.localStorage.getItem(KEY) === "1";
  } catch {
    return false;
  }
}

interface DetailsState {
  show: boolean;
  setShow: (show: boolean) => void;
}

export const useTechnicalDetails = create<DetailsState>((set) => ({
  show: stored(),
  setShow: (show) => {
    try {
      if (show) window.localStorage.setItem(KEY, "1");
      else window.localStorage.removeItem(KEY);
    } catch {
      // Not remembered; it still applies now.
    }
    set({ show });
  },
}));

/** Whether technical details are shown. */
export const useShowDetails = () => useTechnicalDetails((s) => s.show);
