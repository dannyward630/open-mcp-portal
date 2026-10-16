/* Email validation: name@localhost (the dev personas) is accepted; other single-label domains aren't. */
import { emailProblem, looksLikeEmail } from "../lib/validation";
import { urlProblem } from "../components/servers/server-fields";
import { serverSlug } from "../components/servers/labels";

describe("emailProblem", () => {
  it("accepts ordinary addresses and name@localhost", () => {
    for (const ok of ["una@example.com", "a.b+c@sub.example.org", "admin@localhost", "Alex@LOCALHOST", "  owner@localhost  "]) expect(emailProblem(ok), ok).toBeUndefined();
  });

  it("rejects other single-label domains and malformed text", () => {
    for (const bad of ["una@example", "una@intranet", "una", "@example.com", "una@", "una@@example.com", "u na@example.com", "una@example.", "una@.com"]) {
      expect(looksLikeEmail(bad), bad).toBe(false);
      expect(emailProblem(bad), bad).toBe("Enter an email address like name@example.org.");
    }
    expect(emailProblem("  ")).toBe("Enter an email address.");
  });
});

describe("server fields", () => {
  it("allows https, plain http on loopback in development, and leaves other http hosts to the server's allowlist", () => {
    expect(urlProblem("https://tools.example.com/mcp", false)).toBeUndefined();
    expect(urlProblem("http://localhost:9000/mcp", true)).toBeUndefined();
    expect(urlProblem("http://127.0.0.1:9000/mcp", true)).toBeUndefined();
    expect(urlProblem("http://localhost:9000/mcp", false)).toBe("Use an https address.");
    expect(urlProblem("http://127.0.0.1:9000/mcp", false)).toBe("Use an https address.");
    // An internal host may be allowed plain http by a platform admin: the server decides.
    expect(urlProblem("http://mcp.corp.example.com/mcp", false)).toBeUndefined();
    expect(urlProblem("http://tools.example.com/mcp", true)).toBeUndefined();
    expect(urlProblem("ftp://tools.example.com/mcp", false)).toBe("Use an https address.");
    expect(urlProblem("tools.example.com", false)).toMatch(/full URL/);
    expect(urlProblem("", false)).toBe("Enter the server's URL.");
  });

  it("suggests a slug of at most 48 characters", () => {
    expect(serverSlug("Service Status!")).toBe("service-status");
    expect(serverSlug(" --GitHub (internal) ")).toBe("github-internal");
    const long = serverSlug("a".repeat(47) + " b c");
    expect(long.length).toBeLessThanOrEqual(48);
    expect(long.endsWith("-")).toBe(false);
  });
});
