// Entry point. Boot the worker; a fatal error exits non-zero so the container
// restart policy takes over.
import { Worker } from "./worker.js";

new Worker().run().catch((err) => {
  console.error(JSON.stringify({ ts: new Date().toISOString(), level: "fatal", msg: String(err) }));
  process.exit(1);
});
