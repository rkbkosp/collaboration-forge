import assert from "node:assert/strict";
import test from "node:test";
import { ForgeError, forgeErrorEnvelope } from "./errors.ts";

test("error envelopes remove repeated code prefixes and redact private fields", () => {
  const envelope = forgeErrorEnvelope(new ForgeError("claim_denied", "claim_denied: another execution", 409, false, {
    hint: "Bearer private-token",
    data: { cleanup_pending: false, execution_token: "private-token", attempt_id: "private-attempt" },
  }));
  assert.deepEqual(envelope, {
    status: 409,
    error: { code: "claim_denied", message: "another execution", ambiguous: false, hint: "Bearer [REDACTED]", data: { cleanup_pending: false } },
  });
});
