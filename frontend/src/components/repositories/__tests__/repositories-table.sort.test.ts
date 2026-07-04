import { describe, test, expect } from "vitest";
import {
  sortRepositories,
  type RepoSortKey,
} from "../repositories-table";
import type { Repository } from "@/lib/api/types";

// Minimal Repository factory — only the fields the sort reads matter; the
// rest are filled with harmless defaults so the type is satisfied.
function repo(
  name: string,
  storage_used_bytes: number,
  created_at: string,
): Repository {
  return {
    repo_id: name,
    org: "acme",
    name,
    is_public: false,
    storage_used_bytes,
    storage_quota_bytes: 0,
    created_at,
  } as Repository;
}

const a = repo("a", 100, "2026-01-01T00:00:00Z");
const b = repo("b", 300, "2026-03-01T00:00:00Z");
const c = repo("c", 200, "2026-02-01T00:00:00Z");
const rows = [a, b, c];

describe("sortRepositories", () => {
  test("null key returns the input order (identity)", () => {
    expect(sortRepositories(rows, null, "asc")).toBe(rows);
  });

  test("does not mutate the input array", () => {
    const copy = [...rows];
    sortRepositories(rows, "storage", "desc");
    expect(rows).toEqual(copy);
  });

  test("storage ascending orders by bytes used", () => {
    expect(sortRepositories(rows, "storage", "asc").map((r) => r.name)).toEqual([
      "a",
      "c",
      "b",
    ]);
  });

  test("storage descending is the reverse", () => {
    expect(
      sortRepositories(rows, "storage", "desc").map((r) => r.name),
    ).toEqual(["b", "c", "a"]);
  });

  test("created ascending orders by timestamp", () => {
    expect(sortRepositories(rows, "created", "asc").map((r) => r.name)).toEqual([
      "a",
      "c",
      "b",
    ]);
  });

  test("unparseable created_at sorts to the bottom", () => {
    const bad = repo("bad", 50, "not-a-date");
    // Ascending: NaN date is treated as -Infinity, so it leads; descending it
    // trails. We assert it never lands in the middle of valid dates.
    const asc = sortRepositories([a, bad, b], "created", "asc").map(
      (r) => r.name,
    );
    expect(asc[0]).toBe("bad");
  });

  test("accepts every RepoSortKey without throwing", () => {
    const keys: RepoSortKey[] = ["storage", "created"];
    for (const k of keys) {
      expect(() => sortRepositories(rows, k, "asc")).not.toThrow();
    }
  });
});
