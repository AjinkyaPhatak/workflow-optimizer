// One automatic doctor request per draft revision. The server bounds the model
// repair/check passes inside that request. Remember exhausted attempts across
// reloads so an unchanged failure cannot cause an endless request loop.
const attempts = new Map<string, number>();
const prefix = "workflow-optimizer.doctor.";

export function wasDoctored(id: string, revision: number): boolean {
  if (attempts.get(id) === revision) return true;
  try { return window.sessionStorage.getItem(prefix + id) === String(revision); }
  catch { return false; }
}

export function markDoctored(id: string, revision: number): void {
  attempts.set(id, revision);
  try { window.sessionStorage.setItem(prefix + id, String(revision)); }
  catch { /* The in-memory guard still prevents repeated attempts. */ }
}

export const DOCTOR_STOPPED = "I couldn’t finish checking this workflow automatically. Your request and answers are saved. Nothing will run until the checks pass.";
