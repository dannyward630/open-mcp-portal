/* Words for v0.2's error codes (token_not_allowed, session_required, too_many_tokens, ambiguous_credentials, URL refusals…). */
import { render, screen } from "@testing-library/react";
import { ApiError } from "../api/client";
import { V02ErrorAlert } from "../components/errors";
import { describeV02, friendly, urlReasonWording } from "../lib/error-wording";

const err = (status: number, code: string, details?: Record<string, unknown>) => new ApiError(status, code, `raw ${code}`, details);

describe("error wording", () => {
  it("says what the person can do for the token and notification codes", () => {
    expect(describeV02(err(403, "token_not_allowed"))?.title).toBe("Tokens can't do this");
    expect(describeV02(err(403, "session_required"))?.message).toMatch(/signed in as a person/);
    expect(describeV02(err(409, "too_many_tokens"))?.message).toMatch(/At most 20 tokens/);
    expect(describeV02(err(400, "invalid_scopes"))?.message).toMatch(/permission/);
    expect(describeV02(err(400, "invalid_expiry"))?.message).toMatch(/1 to 365 days/);
    expect(describeV02(err(401, "invalid_token"))?.title).toBe("The token wasn't accepted");
    expect(describeV02(err(400, "ambiguous_credentials"))?.title).toBe("Two credentials on one request");
    expect(describeV02(err(409, "name_taken"))?.title).toBe("Name already used");
    expect(describeV02(err(409, "too_many_destinations"))?.message).toMatch(/At most 10/);
    expect(describeV02(err(409, "not_signed"))?.title).toMatch(/Slack destinations aren't signed/);
    expect(describeV02(err(409, "team_archived"))?.title).toBe("The team is archived");
    expect(describeV02(err(409, "email_not_configured"))?.title).toBe("Email isn't set up");
    expect(describeV02(err(422, "not_receivable"))?.title).toMatch(/can't receive/);
    expect(describeV02(err(422, "invalid_events"))?.title).toMatch(/can't receive/);
  });

  it("explains each reason a webhook URL is refused", () => {
    for (const reason of ["invalid", "too_long", "userinfo", "fragment", "scheme", "private_address", "blocked_address"]) {
      expect(describeV02(err(422, "url_not_allowed", { reason }))?.message, reason).toBe(urlReasonWording[reason]);
    }
    expect(describeV02(err(422, "url_not_allowed"))?.message).toMatch(/can't send to that address/);
  });

  it("leaves other errors alone", () => {
    expect(describeV02(err(500, "internal"))).toBeUndefined();
    expect(describeV02(new Error("x"))).toBeUndefined();
    const other = err(404, "team_not_found");
    expect(friendly(other)).toBe(other);
    expect((friendly(err(409, "too_many_tokens")) as ApiError).message).toMatch(/At most 20 tokens/);
  });

  it("shows the code's own title in an alert, else the page's", () => {
    const { unmount } = render(<V02ErrorAlert error={err(403, "token_not_allowed")} title="Couldn't do it" />);
    expect(screen.getByText("Tokens can't do this")).toBeInTheDocument();
    expect(screen.queryByText("Couldn't do it")).toBeNull();
    unmount();
    render(<V02ErrorAlert error={err(500, "internal")} title="Couldn't do it" />);
    expect(screen.getByText("Couldn't do it")).toBeInTheDocument();
  });
});
