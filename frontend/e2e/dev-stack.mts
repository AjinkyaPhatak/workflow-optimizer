// Manual testing helper: `node e2e/dev-stack.mts` starts the same backend as
// the E2E run and prints the seeded login. Ctrl+C stops it.
import { startStack } from "./stack.ts";

const { seed, stop } = await startStack();
console.log(JSON.stringify(seed, null, 2));
process.on("SIGINT", () => void stop().then(() => process.exit(0)));
