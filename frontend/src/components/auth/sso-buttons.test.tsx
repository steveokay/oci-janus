import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SSOButtons } from "./sso-buttons";

// FUT-084 — SSOButtons renders one button per CONFIGURED provider (from
// GET /api/v1/auth/providers) and starts the redirect dance on click. When no
// providers are configured (or the list is still loading / errored) it renders
// nothing so the password form stands alone.

const useAuthProviders = vi.fn();
const beginSSOLogin = vi.fn();
vi.mock("@/lib/api/sso", () => ({
  useAuthProviders: () => useAuthProviders(),
  beginSSOLogin: (loginURL: string, from?: string) => beginSSOLogin(loginURL, from),
}));

beforeEach(() => {
  vi.clearAllMocks();
});

describe("SSOButtons", () => {
  it("renders a button per configured provider and starts the flow on click", async () => {
    useAuthProviders.mockReturnValue({
      data: [
        { id: "google", type: "oauth_google", display_name: "Google", login_url: "/auth/oauth/google/start" },
        { id: "okta", type: "saml", display_name: "Okta SAML", login_url: "/auth/saml/okta/start" },
      ],
    });
    const user = userEvent.setup();
    render(<SSOButtons from="/repositories" />);

    const google = screen.getByRole("button", { name: /google/i });
    expect(google).toBeEnabled();
    expect(screen.getByRole("button", { name: /okta saml/i })).toBeEnabled();

    await user.click(google);
    expect(beginSSOLogin).toHaveBeenCalledWith("/auth/oauth/google/start", "/repositories");
  });

  it("renders nothing when no providers are configured", () => {
    useAuthProviders.mockReturnValue({ data: [] });
    const { container } = render(<SSOButtons />);
    expect(container).toBeEmptyDOMElement();
    expect(screen.queryByText(/coming soon/i)).not.toBeInTheDocument();
  });

  it("renders nothing while the provider list is loading or errored", () => {
    useAuthProviders.mockReturnValue({ data: undefined });
    const { container } = render(<SSOButtons />);
    expect(container).toBeEmptyDOMElement();
  });
});
