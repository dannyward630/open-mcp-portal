/*
 * Automation tokens of a team (publisher tokens) or of the platform (platform-admin tokens): the same operations on
 * different paths. Created and revoked in a browser session only; the secret comes back once, from create.
 */
import { queryOptions } from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "../../api/client";

export type AutomationToken = Schemas["AutomationToken"];
export type TokenScopeName = Schemas["AutomationTokenScope"];
export type TokenScope = { kind: "team"; team: string } | { kind: "platform" };

export const tokensKey = (scope: TokenScope) => (scope.kind === "team" ? ["team", scope.team, "automation-tokens"] : ["admin", "automation-tokens"]);

export const tokensQuery = (scope: TokenScope) =>
  queryOptions({
    queryKey: tokensKey(scope),
    queryFn: async (): Promise<AutomationToken[]> =>
      scope.kind === "team"
        ? unwrap(await api.GET("/v1/teams/{team}/automation-tokens", { params: { path: { team: scope.team } } }))
        : unwrap(await api.GET("/v1/admin/automation-tokens")),
  });

export const createToken = async (scope: TokenScope, body: Schemas["AutomationTokenCreate"]): Promise<Schemas["AutomationTokenCreated"]> =>
  scope.kind === "team"
    ? unwrap(await api.POST("/v1/teams/{team}/automation-tokens", { params: { path: { team: scope.team } }, body }))
    : unwrap(await api.POST("/v1/admin/automation-tokens", { body }));

export const revokeToken = async (scope: TokenScope, token: AutomationToken) =>
  scope.kind === "team"
    ? unwrap(await api.DELETE("/v1/teams/{team}/automation-tokens/{tokenId}", { params: { path: { team: scope.team, tokenId: token.id } } }))
    : unwrap(await api.DELETE("/v1/admin/automation-tokens/{tokenId}", { params: { path: { tokenId: token.id } } }));
