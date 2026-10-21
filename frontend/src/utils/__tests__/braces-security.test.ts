import { createRequire } from "node:module";
import { describe, expect, it } from "vitest";

const braces = createRequire(import.meta.url)("braces");

describe("locally patched braces (GHSA-vfj7-8cjw-p6xm)", () => {
  it("rejects deeply nested braces before recursive compilation", () => {
    expect(() => braces("{".repeat(1000) + "x" + "}".repeat(1000))).toThrow(
      RangeError
    );
  });
  it("bounds parenthesis nesting too", () => {
    expect(() => braces("(".repeat(1000) + "x" + ")".repeat(1000))).toThrow(
      RangeError
    );
  });
  it("still expands ordinary patterns", () => {
    expect(braces.expand("src/{one,two}.ts")).toEqual([
      "src/one.ts",
      "src/two.ts",
    ]);
  });
});
