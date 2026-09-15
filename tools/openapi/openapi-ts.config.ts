import { fileURLToPath } from "node:url";
import { defineConfig } from "@hey-api/openapi-ts";

export default defineConfig({
  input: "../../api/openapi.yaml",
  output: {
    // Resolve the consumer config explicitly; do not scan operator parent directories.
    tsConfigPath: fileURLToPath(new URL("../../frontend/tsconfig.json", import.meta.url)),
    path: process.env.CPA_OPENAPI_TS_OUTPUT ?? "../../frontend/src/api/generated"
  },
  plugins: [
    {
      name: "@hey-api/typescript",
      enums: false
    }
  ]
});
