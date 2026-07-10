# Docs screenshots + GIFs

Captures the dashboard screenshots and short flow GIFs used by the docs site
(`docs/assets/screenshots/*.png`, `docs/assets/gifs/*.gif`) by driving a **live
stack** with Playwright.

These are **not** drift-guarded in CI (unlike the OpenAPI spec / env reference)
because they need a running frontend + real data. Regenerate them by hand when
the UI changes materially.

## Regenerate

```bash
# 1. Bring the stack up and make sure it has some data (a couple of pushed
#    images so the repository/tag/security pages aren't empty).
cd infra/docker-compose && docker compose up -d

# 2. Install the capture toolchain (one-time).
cd tools/screenshots
npm install
npx playwright install chromium   # browser binary
#   ffmpeg must also be on PATH (used for webm → GIF).

# 3. Capture. Writes into ../../docs/assets/{screenshots,gifs}.
npm run capture                    # screenshots + GIFs
ONLY=shots npm run capture         # screenshots only
ONLY=gifs  npm run capture         # GIFs only
```

## Config (env vars)

| Var | Default | Notes |
|---|---|---|
| `JANUS_BASE_URL` | `http://localhost:3000` | Frontend origin. |
| `JANUS_ADMIN_USER` | `admin` | Dev-stack admin (public compose cred). |
| `JANUS_ADMIN_PASSWORD` | `Admin1234!` | Dev-stack admin (public compose cred). |
| `OUT_DIR` | `../../docs/assets` | Output root. |
| `ONLY` | *(unset)* | `shots` or `gifs` to run one pass. |

## How it works

The dashboard JWT is held **in memory only** (never `localStorage` — a frontend
security choice), so a full page reload logs out. The script logs in once via
the form, then navigates by clicking in-app `<a href>` links (TanStack Router
does client-side nav, preserving the token) instead of `page.goto`. It waits for
the app's loading skeletons (`.skeleton-shimmer`) to clear before each shot so
no half-loaded state is captured. GIF flows record to webm, then ffmpeg converts
with a two-pass palette for clean colours.
