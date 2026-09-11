import { Type } from "typebox";
import { Check } from "typebox/value";
import { StringEnum } from "@earendil-works/pi-ai";
import { ForgeError } from "./errors.ts";

const text = () => Type.String({ minLength: 1 });
const ref = text();
const evidence = Type.Object({
  type: StringEnum(["commit", "pr", "test", "reviewed-paths", "external", "no-change-audit", "duplicate-of", "superseded-by"]),
  sha: Type.Optional(text()),
  url: Type.Optional(text()),
  command: Type.Optional(text()),
  paths: Type.Optional(Type.Array(text(), { minItems: 1 })),
  account: Type.Optional(text()),
  rationale: Type.Optional(text()),
  issue_ref: Type.Optional(text()),
}, { additionalProperties: false });

export const toolSchemas = {
  issue_list: Type.Object({ status: Type.Optional(StringEnum(["open", "closed"])), limit: Type.Optional(Type.Integer({ minimum: 1, maximum: 1000 })) }, { additionalProperties: false }),
  issue_get: Type.Object({ ref }, { additionalProperties: false }),
  issue_graph: Type.Object({ ref, depth: Type.Optional(Type.Integer({ minimum: 1, maximum: 10 })) }, { additionalProperties: false }),
  issue_create: Type.Object({ title: text(), body: Type.Optional(Type.String()) }, { additionalProperties: false }),
  issue_comment: Type.Object({ ref, body: text() }, { additionalProperties: false }),
  issue_link: Type.Object({ ref, type: StringEnum(["parent", "blocks", "related"]), to_ref: text() }, { additionalProperties: false }),
  issue_claim: Type.Object({ ref, purpose: Type.Optional(text()) }, { additionalProperties: false }),
  issue_renew: Type.Object({ ref }, { additionalProperties: false }),
  issue_release: Type.Object({ ref, reason: Type.Optional(text()) }, { additionalProperties: false }),
  issue_close: Type.Object({
    ref,
    reason: StringEnum(["done", "wontfix", "duplicate", "superseded", "audit-no-change"]),
    message: text(),
    evidence: Type.Optional(Type.Array(evidence)),
    if_match: Type.Optional(text()),
  }, { additionalProperties: false }),
};

export type ToolName = keyof typeof toolSchemas;
export function validateParams(name: ToolName, params: unknown): asserts params is Record<string, unknown> {
  if (!Object.hasOwn(toolSchemas, name) || !Check(toolSchemas[name], params)) {
    // Avoid echoing arbitrary model fields (including attempted credential inputs).
    throw new ForgeError("usage", "Invalid Forge tool parameters; use only the published schema");
  }
  if (name === "issue_close") {
    const fields: Record<string, string> = { commit: "sha", pr: "url", test: "command", "reviewed-paths": "paths", external: "account", "no-change-audit": "rationale", "duplicate-of": "issue_ref", "superseded-by": "issue_ref" };
    for (const item of ((params as Record<string, unknown>).evidence ?? []) as Record<string, unknown>[]) {
      const field = fields[String(item.type)];
      if (!item[field] || Object.keys(item).some((key) => key !== "type" && key !== field)) {
        throw new ForgeError("usage", "Each typed evidence item must contain only type and its corresponding evidence field");
      }
    }
  }
}
