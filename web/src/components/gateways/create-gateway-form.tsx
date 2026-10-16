/*
 * Create gateway (?form=new over a gateway list): name, slug (suggested
 * from the name, never changed afterwards: it's the endpoint's path),
 * description and the switch. A team gateway (the team's owners and admins)
 * or a platform gateway (platform admins). The new record opens on its
 * Capabilities tab, where the selection is chosen.
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { api, ApiError, unwrap } from "../../api/client";
import { FormPage, FormSection } from "../templates/form-page";
import { serverSlug, slugPattern } from "../servers/labels";
import m from "../servers/servers.module.css";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Field } from "@/components/ui/field/field";
import { Input, Textarea } from "@/components/ui/input/input";
import { Switch } from "@/components/ui/switch/switch";
import { toast } from "@/components/ui/toast/toast";
import { type Gateway, whatIsAGateway, whatIsAPlatformGateway } from "./labels";
import { type GatewayScope, storeGateway } from "./scope";

const slugProblem = (slug: string) =>
  !slug ? "Enter a slug." : slugPattern.test(slug) ? undefined : "Use 1 to 48 lowercase letters, digits and hyphens, starting and ending with a letter or digit.";

export function CreateGatewayForm({ scope, onClose }: { scope: GatewayScope; onClose: () => void }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const formRef = useRef<HTMLFormElement>(null);
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugEdited, setSlugEdited] = useState(false);
  const [description, setDescription] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [submitted, setSubmitted] = useState(false);
  const [created, setCreated] = useState<Gateway | null>(null);
  const create = useMutation({
    mutationFn: async () => {
      const body = { slug, name: name.trim(), description: description.trim() || undefined, enabled };
      return scope.kind === "team"
        ? unwrap(await api.POST("/v1/teams/{team}/gateways", { params: { path: { team: scope.team } }, body }))
        : unwrap(await api.POST("/v1/admin/gateways", { body }));
    },
    onSuccess: (g) => {
      storeGateway(qc, g);
      toast.success(`${g.name} was created`, "Choose the capabilities it offers.");
      setCreated(g);
    },
  });
  // Saved: the form no longer counts as unsaved in this render, so its guard lets the navigation through.
  useEffect(() => {
    if (!created) return;
    const search = { tab: "capabilities" as const };
    if (scope.kind === "team") void navigate({ to: "/teams/$team/gateways/$gatewayId", params: { team: scope.team, gatewayId: created.id }, search, replace: true });
    else void navigate({ to: "/admin/gateways/$gatewayId", params: { gatewayId: created.id }, search, replace: true });
  }, [created, navigate, scope]);

  const taken = create.error instanceof ApiError && create.error.code === "slug_taken";
  const nameError = submitted && !name.trim() ? "Enter a name." : undefined;
  const slugError = (submitted ? slugProblem(slug) : undefined) ?? (taken ? "Another gateway uses this slug. Choose another." : undefined);
  const submit = () => {
    setSubmitted(true);
    if (!name.trim() || slugProblem(slug)) {
      requestAnimationFrame(() => formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus());
      return;
    }
    create.mutate();
  };
  const platform = scope.kind === "admin";
  return (
    <FormPage
      label={platform ? "Create platform gateway" : "Create gateway"}
      title={platform ? "Create platform gateway" : "Create gateway"}
      description={platform ? whatIsAPlatformGateway : whatIsAGateway}
      onClose={onClose}
      onSubmit={submit}
      submitLabel="Create gateway"
      busy={create.isPending}
      dirty={created ? false : undefined}
      formRef={formRef}
    >
      <FormSection title="Gateway">
        <Field label="Name" error={nameError}>
          <Input
            maxLength={100}
            value={name}
            onChange={(e) => {
              setName(e.target.value);
              if (!slugEdited) setSlug(serverSlug(e.target.value));
            }}
          />
        </Field>
        <Field label="Slug" description="Can't be changed later: the endpoint is /mcp/slug. Unique across the platform." error={slugError}>
          <Input
            spellCheck={false}
            autoComplete="off"
            maxLength={48}
            value={slug}
            onChange={(e) => {
              setSlugEdited(true);
              if (taken) create.reset();
              setSlug(e.target.value);
            }}
          />
        </Field>
        <Field label="Description" labelHint="Optional" description="What the gateway is for. People see it under My gateways." className={m.wide}>
          <Textarea maxLength={2000} value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field>
        <div className={m.wide}>
          <Switch label="Enabled" description="Turned off, the endpoint answers “not found” to everyone." checked={enabled} onCheckedChange={setEnabled} />
        </div>
      </FormSection>
      <ErrorAlert error={taken ? undefined : create.error} title="Couldn't create the gateway" />
    </FormPage>
  );
}
