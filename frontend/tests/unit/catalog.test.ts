import { describe, expect, it } from "vitest";
import { groupByCategory } from "@/features/nodes/catalog";
import { portTypesCompatible } from "@/lib/workflow/ports";
import { catalogList } from "./fixtures";

describe("catalog-driven helpers", () => {
  it("groups whatever categories the backend reports", () => {
    const groups = groupByCategory([...catalogList, { ...catalogList[0], type: "x.new", name: "Brand New", category: "knowledge_base" }]);
    expect(groups.map(([c]) => c)).toEqual(["general", "ai", "integration", "knowledge_base"]);
    expect(groups[0][1].map((d) => d.type)).toEqual(["input", "merge", "output"]);
  });

  it("mirrors the backend port rule (UX only)", () => {
    expect(portTypesCompatible("string", "string")).toBe(true);
    expect(portTypesCompatible("json", "string")).toBe(true);
    expect(portTypesCompatible("string", "json")).toBe(true);
    expect(portTypesCompatible("string", "object")).toBe(false);
    expect(portTypesCompatible("number", "string")).toBe(false);
  });
});
