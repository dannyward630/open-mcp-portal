/* Mark one or every notification read, then refresh the bell and the inbox. */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "../../api/client";
import { notificationsKey } from "../../api/notifications";

export function useMarkRead() {
  const qc = useQueryClient();
  const refresh = () => void qc.invalidateQueries({ queryKey: notificationsKey });
  const one = useMutation({
    mutationFn: async (notificationId: number) => unwrap(await api.POST("/v1/me/notifications/{notificationId}/read", { params: { path: { notificationId } } })),
    onSettled: refresh,
  });
  const all = useMutation({
    mutationFn: async () => unwrap(await api.POST("/v1/me/notifications/read-all")),
    onSettled: refresh,
  });
  return { one, all };
}
