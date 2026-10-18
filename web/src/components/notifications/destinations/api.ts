/*
 * Webhook destinations of a team or of the platform: the two scopes have the same operations on different paths,
 * so one set of functions picks the path by scope and the panel never cares which it is showing.
 */
import { queryOptions } from "@tanstack/react-query";
import { api, ifMatch, type Schemas, unwrap } from "../../../api/client";

export type Destination = Schemas["NotificationDestination"];
export type DestinationScope = { kind: "team"; team: string } | { kind: "platform" };

export const destinationsKey = (scope: DestinationScope) => (scope.kind === "team" ? ["team", scope.team, "notification-destinations"] : ["admin", "notification-destinations"]);

export const destinationsQuery = (scope: DestinationScope) =>
  queryOptions({
    queryKey: [...destinationsKey(scope), "list"],
    queryFn: async (): Promise<Destination[]> =>
      scope.kind === "team"
        ? unwrap(await api.GET("/v1/teams/{team}/notification-destinations", { params: { path: { team: scope.team } } }))
        : unwrap(await api.GET("/v1/admin/notification-destinations")),
  });

export const deliveriesQuery = (scope: DestinationScope, destinationId: string) =>
  queryOptions({
    queryKey: [...destinationsKey(scope), destinationId, "deliveries"],
    queryFn: async (): Promise<Schemas["NotificationDelivery"][]> =>
      scope.kind === "team"
        ? unwrap(await api.GET("/v1/teams/{team}/notification-destinations/{destinationId}/deliveries", { params: { path: { team: scope.team, destinationId } } }))
        : unwrap(await api.GET("/v1/admin/notification-destinations/{destinationId}/deliveries", { params: { path: { destinationId } } })),
  });

export const createDestination = async (scope: DestinationScope, body: Schemas["NotificationDestinationCreate"]): Promise<Schemas["NotificationDestinationCreated"]> =>
  scope.kind === "team"
    ? unwrap(await api.POST("/v1/teams/{team}/notification-destinations", { params: { path: { team: scope.team } }, body }))
    : unwrap(await api.POST("/v1/admin/notification-destinations", { body }));

export const updateDestination = async (scope: DestinationScope, d: Destination, body: Schemas["NotificationDestinationUpdate"]): Promise<Destination> =>
  scope.kind === "team"
    ? unwrap(await api.PATCH("/v1/teams/{team}/notification-destinations/{destinationId}", { params: { path: { team: scope.team, destinationId: d.id }, header: ifMatch(d.revision) }, body }))
    : unwrap(await api.PATCH("/v1/admin/notification-destinations/{destinationId}", { params: { path: { destinationId: d.id }, header: ifMatch(d.revision) }, body }));

export const deleteDestination = async (scope: DestinationScope, d: Destination) =>
  scope.kind === "team"
    ? unwrap(await api.DELETE("/v1/teams/{team}/notification-destinations/{destinationId}", { params: { path: { team: scope.team, destinationId: d.id } } }))
    : unwrap(await api.DELETE("/v1/admin/notification-destinations/{destinationId}", { params: { path: { destinationId: d.id } } }));

export const testDestination = async (scope: DestinationScope, d: Destination): Promise<Schemas["NotificationTestResult"]> =>
  scope.kind === "team"
    ? unwrap(await api.POST("/v1/teams/{team}/notification-destinations/{destinationId}/test", { params: { path: { team: scope.team, destinationId: d.id } } }))
    : unwrap(await api.POST("/v1/admin/notification-destinations/{destinationId}/test", { params: { path: { destinationId: d.id } } }));

export const rotateSecret = async (scope: DestinationScope, d: Destination, graceHours: number): Promise<Schemas["NotificationDestinationSecret"]> =>
  scope.kind === "team"
    ? unwrap(await api.POST("/v1/teams/{team}/notification-destinations/{destinationId}/rotate-secret", { params: { path: { team: scope.team, destinationId: d.id } }, body: { graceHours } }))
    : unwrap(await api.POST("/v1/admin/notification-destinations/{destinationId}/rotate-secret", { params: { path: { destinationId: d.id } }, body: { graceHours } }));
