import { startStack } from "./stack";

// Starts the real backend once for the E2E run and shares the seeded login
// and workflow with the tests through the environment.
export default async function globalSetup() {
  const { seed, stop } = await startStack();
  process.env.E2E_SEED = JSON.stringify(seed);
  return stop;
}
