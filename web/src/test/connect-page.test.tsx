/*
 * The connect page (/connect/{provider}?request=…): the request's tool, gateway, provider, account and each scope
 * (granted or new); Continue starts connecting and leaves for the provider, Cancel doesn't; another person's or an
 * unknown link (404), an expired one (410), a mismatched or incomplete link; signing in first. And the result page
 * (/connect/done) for each outcome.
 */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import { redirectTo } from "../components/providers/labels";
import { missingScopes, resultText, safeMissingScopes } from "../pages/connect/done";
import { authConfig } from "./fixtures";
import { connectRequest, providerId, providerSummaries, requestId } from "./fixtures-providers";
import { type Handler, Reply, mockApi, renderApp, shellRoutes } from "./harness";

const path = `/connect/github?request=${requestId}`;
const authorizeUrl = "https://github.com/login/oauth/authorize?client_id=Iv1.abc123&scope=read%3Aorg+repo&state=s1";

function open(routes: Record<string, Handler>, at = path) {
  const calls = mockApi({ ...shellRoutes(), ...routes });
  return { calls, ...renderApp(at) };
}

afterEach(() => vi.restoreAllMocks());

describe("Connect page", () => {
  it("shows who's signed in, the tool and gateway that asked, the provider, the account, and each scope, without the app's shell", async () => {
    const go = vi.spyOn(redirectTo, "go").mockImplementation(() => {});
    const { calls, container } = open({ [`GET /v1/connect/requests/${requestId}`]: () => connectRequest() });
    expect(await screen.findByRole("heading", { level: 1, name: "Connect your GitHub account" })).toBeInTheDocument();
    expect(screen.getByRole("main", { name: "Connect your GitHub account" })).toBeInTheDocument();
    expect(screen.getByText(/needs more access to your GitHub account than you allowed before/)).toBeInTheDocument();
    const request = screen.getByRole("region", { name: "The request" });
    expect(request).toHaveTextContent("una@example.com");
    expect(request).toHaveTextContent("github-tools__create_issue (Create issue)");
    expect(request).toHaveTextContent("Support tools");
    expect(request).toHaveTextContent("octocat");
    const scopes = screen.getByRole("list", { name: "GitHub will ask you to allow" });
    const items = within(scopes).getAllByRole("listitem");
    expect(items[0]).toHaveTextContent("read:org");
    expect(items[0]).toHaveTextContent("Already allowed");
    expect(items[1]).toHaveTextContent("repo");
    expect(items[1]).toHaveTextContent("New");
    expect(screen.queryByRole("navigation", { name: "Main" })).toBeNull();
    await waitFor(() => expect(document.title).toBe("Connect GitHub · Open MCP Portal"));
    // Nothing is started before the person continues.
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    expect(go).not.toHaveBeenCalled();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("continues: starts connecting with the request (CSRF token), then sends the browser to the provider", async () => {
    const u = userEvent.setup();
    const go = vi.spyOn(redirectTo, "go").mockImplementation(() => {});
    const { calls } = open({
      [`GET /v1/connect/requests/${requestId}`]: () => connectRequest(),
      "POST /v1/me/connections/start": () => ({ authorizeUrl, scopes: ["read:org", "repo"], expiresAt: "2026-10-16T08:10:00Z" }),
    });
    await u.click(await screen.findByRole("button", { name: "Continue to GitHub" }));
    await waitFor(() => expect(go).toHaveBeenCalledWith(authorizeUrl));
    const post = calls.find((c) => c.method === "POST" && c.url === "/v1/me/connections/start")!;
    expect(post.body).toEqual({ providerId, requestId });
    expect(post.headers.get("X-CSRF-Token")).toBe("csrf-123");
    expect(screen.getByText("Taking you to GitHub…")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Continue to GitHub" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
  });

  it("cancels without contacting anyone", async () => {
    const u = userEvent.setup();
    const go = vi.spyOn(redirectTo, "go").mockImplementation(() => {});
    const { calls } = open({ [`GET /v1/connect/requests/${requestId}`]: () => connectRequest({ reason: "not_connected", grantedScopes: [], accountLabel: null }) });
    expect(await screen.findByText(/needs your GitHub account to act as you/)).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "The request" })).toHaveTextContent("No account");
    await u.click(screen.getByRole("button", { name: "Cancel" }));
    expect(await screen.findByRole("heading", { level: 1, name: "Nothing was connected" })).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    expect(go).not.toHaveBeenCalled();
  });

  it("doesn't call an account connected once its tokens were erased (needs reconnecting)", async () => {
    open({
      [`GET /v1/connect/requests/${requestId}`]: () => connectRequest({ reason: "needs_reconsent", grantedScopes: [], requestScopes: ["repo"], accountLabel: "octocat" }),
    });
    expect(await screen.findByText(/Your GitHub connection has to be renewed/)).toBeInTheDocument();
    const request = screen.getByRole("region", { name: "The request" });
    expect(request).toHaveTextContent("None: octocat has to be reconnected");
    const scopes = screen.getByRole("list", { name: "GitHub will ask you to allow" });
    expect(within(scopes).getByRole("listitem")).toHaveTextContent("New");
  });

  it("says a link made for someone else (404) can't be used, and never redirects", async () => {
    const go = vi.spyOn(redirectTo, "go").mockImplementation(() => {});
    const { container } = open({ [`GET /v1/connect/requests/${requestId}`]: () => Reply.error(404, "connect_request_not_found", "Not found") });
    expect(await screen.findByRole("heading", { level: 1, name: "This link was made for someone else" })).toBeInTheDocument();
    expect(screen.getByText(/sign out and sign in as yourself/)).toBeInTheDocument();
    expect(screen.getByText("una@example.com")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Continue/ })).toBeNull();
    await waitFor(() => expect(document.title).toBe("This link was made for someone else · Open MCP Portal"));
    expect(go).not.toHaveBeenCalled();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("says an expired or used link (410) needs the tool to run again", async () => {
    open({ [`GET /v1/connect/requests/${requestId}`]: () => Reply.error(410, "connect_request_expired", "Expired") });
    expect(await screen.findByRole("heading", { level: 1, name: "This link expired" })).toBeInTheDocument();
    expect(screen.getByText(/Run the tool again in your AI client to get a new link/)).toBeInTheDocument();
  });

  it("says so when the link expires between opening and Continue", async () => {
    const u = userEvent.setup();
    const go = vi.spyOn(redirectTo, "go").mockImplementation(() => {});
    open({
      [`GET /v1/connect/requests/${requestId}`]: () => connectRequest(),
      "POST /v1/me/connections/start": () => Reply.error(410, "connect_request_expired", "Expired"),
    });
    await u.click(await screen.findByRole("button", { name: "Continue to GitHub" }));
    expect(await screen.findByRole("heading", { level: 1, name: "This link expired" })).toBeInTheDocument();
    expect(go).not.toHaveBeenCalled();
  });

  it("refuses a link whose provider doesn't match its request, and an incomplete one", async () => {
    const { unmount } = open({ [`GET /v1/connect/requests/${requestId}`]: () => connectRequest() }, `/connect/google?request=${requestId}`);
    expect(await screen.findByRole("heading", { level: 1, name: "This link isn't valid" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Continue/ })).toBeNull();
    unmount();
    const { calls } = open({}, "/connect/github");
    expect(await screen.findByRole("heading", { level: 1, name: "This link is incomplete" })).toBeInTheDocument();
    expect(calls.some((c) => c.url.startsWith("/v1/connect/"))).toBe(false);
  });

  it("signed out, shows the sign-in page, which returns here with the whole link", async () => {
    const calls = mockApi({
      ...shellRoutes(),
      "GET /v1/me": () => Reply.error(401, "unauthorized"),
      "GET /v1/auth/config": () => authConfig({ oidcEnabled: true }),
    });
    window.history.replaceState({}, "", path);
    renderApp(path);
    const signIn = await screen.findByRole("link", { name: /Sign in with single sign-on/ });
    expect(signIn).toHaveAttribute("href", "/auth/login?next=" + encodeURIComponent(path));
    expect(screen.getByText("A tool needs one of your accounts")).toBeInTheDocument();
    expect(calls.some((c) => c.url.startsWith("/v1/connect/"))).toBe(false);
    window.history.replaceState({}, "", "/");
  });
});

describe("Connect result page", () => {
  it("says connected and to run the tool again", async () => {
    const { container } = open({ "GET /v1/auth-providers": () => providerSummaries }, "/connect/done?provider=github&result=connected");
    expect(await screen.findByRole("heading", { level: 1, name: "Connected to GitHub" })).toBeInTheDocument();
    expect(screen.getByText(/Go back to your AI client and run the tool again/)).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Account › My connections" })).toHaveAttribute("href", "/settings/connections");
    expect(await axe(container)).toHaveNoViolations();
  });

  it("lists the scopes the provider didn't grant", async () => {
    open({ "GET /v1/auth-providers": () => providerSummaries }, "/connect/done?provider=github&result=missing_scopes&missing=repo&missing=admin%3Aorg");
    expect(await screen.findByRole("heading", { level: 1, name: "Connected, but GitHub didn't allow everything" })).toBeInTheDocument();
    const missing = screen.getByRole("region", { name: "Not allowed" });
    expect(missing).toHaveTextContent("repo");
    expect(missing).toHaveTextContent("admin:org");
  });

  it("explains a denial, an expired attempt, another person and an unknown result", async () => {
    for (const [result, title] of [
      ["denied", "Not connected"],
      ["expired", "This attempt expired"],
      ["wrong_person", "Sign in as the person who started connecting"],
      ["something_new", "Couldn't connect"],
    ] as const) {
      const { unmount } = open({}, `/connect/done?provider=github&result=${result}`);
      expect(await screen.findByRole("heading", { level: 1, name: title })).toBeInTheDocument();
      unmount();
    }
  });

  it("works signed out, without naming a provider it can't check", async () => {
    mockApi({ ...shellRoutes(), "GET /v1/me": () => Reply.error(401, "unauthorized") });
    renderApp("/connect/done?provider=github&result=denied");
    expect(await screen.findByText(/Access wasn't allowed at the provider/)).toBeInTheDocument();
    expect(screen.queryByText(/GitHub|github/)).toBeNull();
    expect(screen.queryByRole("link", { name: /Sign in/ })).toBeNull();
  });

  it("names only a known provider: anything else in the address gets generic words", async () => {
    const { unmount } = open({ "GET /v1/auth-providers": () => providerSummaries }, "/connect/done?provider=Evil%20Corp%20Support&result=connected");
    expect(await screen.findByRole("heading", { level: 1, name: "Connected to the provider" })).toBeInTheDocument();
    expect(screen.queryByText(/Evil/)).toBeNull();
    unmount();
    open({ "GET /v1/auth-providers": () => providerSummaries }, "/connect/done?provider=google&result=connected");
    expect(await screen.findByRole("heading", { level: 1, name: "Connected to Google" })).toBeInTheDocument();
  });

  it("lists left-out scopes only when they look like scopes, else says so generically", async () => {
    const bad = [
      "missing=call%20(555)%200100",
      "missing=" + "a".repeat(101),
      Array.from({ length: 21 }, (_, i) => `missing=s${i}`).join("&"),
      "missing=repo&missing=%3Cscript%3E",
    ];
    for (const q of bad) {
      const { unmount } = open({ "GET /v1/auth-providers": () => providerSummaries }, `/connect/done?provider=github&result=missing_scopes&${q}`);
      expect(await screen.findByRole("heading", { level: 1, name: "Connected, but GitHub didn't allow everything" })).toBeInTheDocument();
      expect(screen.getByText("Some of the permissions the tool needs weren't allowed.")).toBeInTheDocument();
      expect(screen.queryByRole("region", { name: "Not allowed" })).toBeNull();
      expect(screen.queryByText(/555|script|aaaa|s20/)).toBeNull();
      unmount();
    }
    open({ "GET /v1/auth-providers": () => providerSummaries }, "/connect/done?provider=google&result=missing_scopes&missing=https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fdrive.readonly");
    expect(await screen.findByRole("region", { name: "Not allowed" })).toHaveTextContent("https://www.googleapis.com/auth/drive.readonly");
  });

  it("helpers", () => {
    expect(missingScopes("repo, admin:org  user")).toEqual(["repo", "admin:org", "user"]);
    expect(missingScopes(undefined)).toEqual([]);
    expect(missingScopes(["repo", "admin:org", "repo"])).toEqual(["repo", "admin:org"]);
    expect(safeMissingScopes(["repo", "read:org", "a.b_c/d-e"])).toEqual(["repo", "read:org", "a.b_c/d-e"]);
    expect(safeMissingScopes("x".repeat(100))).toEqual(["x".repeat(100)]);
    expect(safeMissingScopes("x".repeat(101))).toBeNull();
    expect(safeMissingScopes(["repo", "<b>"])).toBeNull();
    expect(safeMissingScopes(Array.from({ length: 20 }, (_, i) => `s${i}`))).toHaveLength(20);
    expect(safeMissingScopes(Array.from({ length: 21 }, (_, i) => `s${i}`))).toBeNull();
    expect(resultText("connected", "Slack").title).toBe("Connected to Slack");
  });
});
