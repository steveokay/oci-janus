import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach } from "vitest";

// FUT-016 added a TanStack-Router <Link> wrapping the image cell. Unit
// tests render CachedManifestRow without a router context, so we replace
// the router module with a stub that renders a plain anchor. Keeps the
// row-expander tests focused on the FUT-015 contract — toggle, copy,
// timestamps — without dragging in the routing layer.
vi.mock("@tanstack/react-router", async () => {
  const actual = await vi.importActual<Record<string, unknown>>(
    "@tanstack/react-router",
  );
  return {
    ...actual,
    Link: ({
      to,
      params,
      children,
      className,
    }: {
      to?: string;
      params?: Record<string, unknown>;
      children: React.ReactNode;
      className?: string;
    }) => {
      const href = String(to ?? "").replace(/\$(\w+)/g, (_match, key: string) =>
        String(params?.[key] ?? ""),
      );
      return (
        <a href={href} className={className}>
          {children}
        </a>
      );
    },
    createFileRoute: () => () => ({
      useParams: () => ({}),
    }),
  };
});

import * as React from "react";
import {
  CachedManifestRow,
  resolvePullHost,
  dockerPullCommand,
} from "../_authenticated.workspace.proxy-cache";
import type { CachedManifest } from "@/lib/api/proxy-cache";
import {
  Table,
  TableBody,
} from "@/components/ui/table";

// ---------------------------------------------------------------------------
// FUT-015 — row expander + `docker pull` copy command on /workspace/proxy-cache.
//
// We test the row component in isolation (rather than mounting the whole
// route + router) because the expand behaviour, copy-command construction,
// and timestamp surfacing all live on CachedManifestRow. Mounting the full
// route would drag in TanStack Router + Query plus the useWorkspace fetch,
// none of which add coverage for the row contract.
//
// The clipboard is stubbed so the CopyButton flash transitions correctly
// without jsdom complaining about a missing `navigator.clipboard.writeText`.
// ---------------------------------------------------------------------------

const writeText = vi.fn().mockResolvedValue(undefined);
Object.defineProperty(navigator, "clipboard", {
  value: { writeText },
  writable: true,
});

const fetchedAt = "2026-06-20T10:30:00Z";
const lastPulledAt = "2026-06-23T14:45:00Z";

const tagRow: CachedManifest = {
  id: "row-1",
  upstream_id: "u-1",
  upstream_name: "docker.io",
  image: "library/nginx",
  reference: "1.27-alpine",
  digest: "sha256:" + "a".repeat(64),
  media_type: "application/vnd.oci.image.manifest.v1+json",
  size_bytes: 4 * 1024 * 1024,
  fetched_at: fetchedAt,
  last_pulled_at: lastPulledAt,
  pull_count: 12,
};

// A second row with no digest — covers the "hide digest copy button" branch.
// Real-world this happens for cache entries that pre-date FUT-013 having a
// non-null digest column populated.
const tagRowNoDigest: CachedManifest = {
  ...tagRow,
  id: "row-2",
  digest: "",
};

// Helper — CachedManifestRow renders a <tr>, so we have to wrap it in a
// <table><tbody> to keep the DOM valid (and to keep jsdom from warning
// about orphaned table rows).
function renderRow(props: {
  m: CachedManifest;
  pullHost?: string;
  expanded: boolean;
  onToggleExpand?: () => void;
}) {
  return render(
    <Table>
      <TableBody>
        <CachedManifestRow
          m={props.m}
          pullHost={props.pullHost ?? "registry.example.com"}
          expanded={props.expanded}
          onToggleExpand={props.onToggleExpand ?? (() => {})}
          onEvict={() => {}}
        />
      </TableBody>
    </Table>,
  );
}

describe("CachedManifestRow — FUT-015 expander", () => {
  beforeEach(() => {
    writeText.mockClear();
  });

  test("chevron toggle has the right aria-label per state", () => {
    const onToggle = vi.fn();
    const { rerender } = renderRow({
      m: tagRow,
      expanded: false,
      onToggleExpand: onToggle,
    });

    // Collapsed: aria-label is "Show pull command", aria-expanded="false".
    const showBtn = screen.getByRole("button", { name: /show pull command/i });
    expect(showBtn).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(showBtn);
    expect(onToggle).toHaveBeenCalledTimes(1);

    // Expanded: aria-label flips to "Hide pull command".
    rerender(
      <Table>
        <TableBody>
          <CachedManifestRow
            m={tagRow}
            pullHost="registry.example.com"
            expanded={true}
            onToggleExpand={onToggle}
            onEvict={() => {}}
          />
        </TableBody>
      </Table>,
    );
    const hideBtn = screen.getByRole("button", { name: /hide pull command/i });
    expect(hideBtn).toHaveAttribute("aria-expanded", "true");
  });

  test("expanded panel renders both `docker pull` commands (tag + digest)", () => {
    renderRow({ m: tagRow, expanded: true });

    // Tag form: `docker pull <host>/cache/<upstream>/<image>:<reference>`.
    const tagCmd =
      "docker pull registry.example.com/cache/docker.io/library/nginx:1.27-alpine";
    expect(screen.getByText(tagCmd)).toBeInTheDocument();

    // Digest form uses `@` as separator.
    const digestCmd =
      "docker pull registry.example.com/cache/docker.io/library/nginx@sha256:" +
      "a".repeat(64);
    expect(screen.getByText(digestCmd)).toBeInTheDocument();
  });

  test("digest copy field is hidden when row.digest is empty", () => {
    renderRow({ m: tagRowNoDigest, expanded: true });

    // Tag command still renders.
    expect(
      screen.getByText(
        "docker pull registry.example.com/cache/docker.io/library/nginx:1.27-alpine",
      ),
    ).toBeInTheDocument();

    // Digest label is absent — the whole field is skipped, not just the button.
    expect(screen.queryByText(/docker pull \(by digest\)/i)).not.toBeInTheDocument();
    // Defence-in-depth: no `@sha256` shows up anywhere in the panel.
    expect(screen.queryByText(/@sha256:/)).not.toBeInTheDocument();
  });

  test("copy button writes the tag pull command to the clipboard", async () => {
    renderRow({ m: tagRow, expanded: true });

    // Two copy buttons in this panel — tag + digest, in DOM order. Click the
    // first one to verify the tag command is the one sent to the clipboard.
    const copyButtons = screen.getAllByRole("button", { name: /^copy$/i });
    expect(copyButtons.length).toBe(2);

    await act(async () => {
      fireEvent.click(copyButtons[0]);
    });

    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith(
        "docker pull registry.example.com/cache/docker.io/library/nginx:1.27-alpine",
      );
    });
  });

  test("copy button writes the digest pull command when clicked", async () => {
    renderRow({ m: tagRow, expanded: true });

    const copyButtons = screen.getAllByRole("button", { name: /^copy$/i });
    await act(async () => {
      fireEvent.click(copyButtons[1]);
    });

    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith(
        "docker pull registry.example.com/cache/docker.io/library/nginx@sha256:" +
          "a".repeat(64),
      );
    });
  });

  test("absolute ISO timestamps render in the expanded panel", () => {
    renderRow({ m: tagRow, expanded: true });

    // formatAbsoluteDate uses "MMM d, yyyy HH:mm". The exact rendered string
    // depends on the local timezone (date-fns has no TZ override), so we
    // assert against a generous day window — fetched_at is Jun 20 UTC, so
    // any TZ from UTC-12 to UTC+14 yields Jun 19, 20, or 21 — and require
    // both labels ("Cached at" + "Last pulled at") to be present.
    expect(screen.getByText(/Cached at/i)).toBeInTheDocument();
    expect(screen.getByText(/Last pulled at/i)).toBeInTheDocument();
    // Both timestamps should mention "Jun" and "2026" — combine into a
    // single matcher so we don't break across timezones.
    expect(
      screen.getAllByText(/Jun (19|20|21), 2026/).length,
    ).toBeGreaterThanOrEqual(1);
    expect(
      screen.getAllByText(/Jun (22|23|24), 2026/).length,
    ).toBeGreaterThanOrEqual(1);

    // Media-type label + value are also visible.
    expect(screen.getByText(/Media type/i)).toBeInTheDocument();
    expect(
      screen.getByText("application/vnd.oci.image.manifest.v1+json"),
    ).toBeInTheDocument();
  });

  test("expander content is absent when expanded=false", () => {
    renderRow({ m: tagRow, expanded: false });

    expect(
      screen.queryByText(
        "docker pull registry.example.com/cache/docker.io/library/nginx:1.27-alpine",
      ),
    ).not.toBeInTheDocument();
    expect(screen.queryByText(/Media type/i)).not.toBeInTheDocument();
  });
});

describe("resolvePullHost", () => {
  test("returns the workspace host when set (custom-domain case)", () => {
    expect(resolvePullHost("registry.acme.com")).toBe("registry.acme.com");
  });

  // jsdom only allows redefining `window.location.host` ONCE — second
  // attempt throws `Cannot redefine property: host`. Wrap the whole
  // location object instead so each test gets a fresh stub.
  const stubLocationHost = (host: string) => {
    const originalLocation = window.location;
    Object.defineProperty(window, "location", {
      configurable: true,
      value: { ...originalLocation, host },
    });
    return () => {
      Object.defineProperty(window, "location", {
        configurable: true,
        value: originalLocation,
      });
    };
  };

  test("rewrites the Vite dev port to the gateway port", () => {
    const restore = stubLocationHost("localhost:5173");
    try {
      expect(resolvePullHost(undefined)).toBe("localhost:8084");
    } finally {
      restore();
    }
  });

  test("falls through to window.location.host when not :5173", () => {
    const restore = stubLocationHost("registry.staging.internal");
    try {
      expect(resolvePullHost(undefined)).toBe("registry.staging.internal");
    } finally {
      restore();
    }
  });
});

describe("dockerPullCommand", () => {
  test("emits `:tag` form for tag refs", () => {
    expect(
      dockerPullCommand("h", "docker.io", "library/nginx", { reference: "1.27" }),
    ).toBe("docker pull h/cache/docker.io/library/nginx:1.27");
  });

  test("emits `@digest` form for digest refs", () => {
    expect(
      dockerPullCommand("h", "docker.io", "library/nginx", {
        digest: "sha256:" + "b".repeat(64),
      }),
    ).toBe("docker pull h/cache/docker.io/library/nginx@sha256:" + "b".repeat(64));
  });
});
