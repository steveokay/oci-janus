import { createFileRoute, redirect } from "@tanstack/react-router";

// /helm retired (unified-artifact-catalog): Helm charts are no longer a
// separate catalog. They live in the environments → repository → tag
// structure, reachable via the "Charts" filter on a repository list. This
// route redirects to the environments overview so old links/bookmarks
// don't 404.
export const Route = createFileRoute("/_authenticated/helm")({
  beforeLoad: () => {
    throw redirect({ to: "/repositories" });
  },
});
