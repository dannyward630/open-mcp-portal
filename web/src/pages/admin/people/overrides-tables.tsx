/*
 * The rows of a team's overrides: the platform value, the team's choice (inherit, or its own value) and what applies.
 * While a typed value isn't valid, Applies says so instead of previewing it.
 */
import { Badge } from "@/components/ui/badge/badge";
import { Field } from "@/components/ui/field/field";
import { NativeSelect } from "@/components/ui/input/input";
import { NumberInput } from "@/components/ui/number-input/number-input";
import { Table, Td, Tr } from "@/components/ui/table/table";
import { type RateLimitKey, rateText } from "@/lib/limits";
import { daysText, type RetentionKind, retentionLabels } from "@/lib/retention";
import s from "../../shared.module.css";
import { LimitName, RateInput } from "../operations/limit-fields";
import o from "../operations/operations.module.css";
import {
  blank,
  effectiveLimit,
  effectivePeriod,
  limitError,
  limitField,
  type Mode,
  type OverrideRow,
  type OverridesForm,
  periodError,
  periodField,
  type TeamLimit,
  type TeamPeriod,
} from "./overrides-form";

type Refused = { id: string; message: string } | undefined;

/** Applies while the team value is invalid: nothing to preview (the field says what's wrong). */
function NotValid() {
  return <span className={s.muted}>Fix the team value</span>;
}

function ModeSelect({ label, value, inherit, disabled, onChange }: { label: string; value: Mode; inherit: string; disabled?: boolean; onChange: (m: Mode) => void }) {
  return (
    <Field label={label} hideLabel className={o.amountField}>
      <NativeSelect size="sm" value={value} disabled={disabled} onChange={(e) => onChange(e.target.value as Mode)}>
        <option value="inherit">{`Inherit (${inherit})`}</option>
        <option value="custom">Team&apos;s own value</option>
      </NativeSelect>
    </Field>
  );
}

type LimitsProps = {
  name: string;
  items: TeamLimit[];
  form: OverridesForm;
  isAdmin: boolean;
  shown: boolean;
  refused: Refused;
  onChange: (key: RateLimitKey, row: OverrideRow) => void;
};

export function LimitsTable({ name, items, form, isAdmin, shown, refused, onChange }: LimitsProps) {
  const overridable = items.filter((l) => l.teamOverridable);
  return (
    <Table caption={`Rate limits for ${name}`} stack columns={["Limit", { label: "Platform", width: "10rem" }, { label: "Team value", width: "15rem" }, { label: "Applies", width: "10rem" }]}>
      {overridable.map((l) => {
        const f = form[limitField(l.key)] ?? blank;
        const err = refused?.id === l.key ? refused.message : shown || f.value.trim() ? limitError(l, f) : undefined;
        const capped = l.override !== null && l.ceiling !== null && l.override > l.ceiling;
        return (
          <Tr key={l.key}>
            <Td>
              <LimitName label={l.label} description={l.description} />
            </Td>
            <Td muted>
              {rateText(l.default)}
              <span className={s.secondary}>{`Ceiling: ${rateText(l.ceiling, "none")}`}</span>
            </Td>
            <Td>
              {isAdmin ? (
                <>
                  <ModeSelect label={`${l.label}: team setting`} value={f.mode} inherit={rateText(l.default)} onChange={(mode) => onChange(l.key, { ...f, mode })} />
                  {f.mode === "custom" && <RateInput label={`${l.label}: team value`} value={f.value} placeholder="Per minute" error={err} onChange={(value) => onChange(l.key, { ...f, value })} />}
                </>
              ) : l.override === null ? (
                <span className={s.muted}>Inherits</span>
              ) : (
                rateText(l.override)
              )}
            </Td>
            <Td>
              {isAdmin && limitError(l, f) ? <NotValid /> : rateText(isAdmin ? effectiveLimit(l, f) : l.effective)}
              {capped && (
                <span className={s.secondary}>
                  <Badge tone="warning" size="sm">
                    Capped by the ceiling
                  </Badge>
                </span>
              )}
            </Td>
          </Tr>
        );
      })}
    </Table>
  );
}

type PeriodsProps = {
  name: string;
  items: TeamPeriod[];
  form: OverridesForm;
  isAdmin: boolean;
  shown: boolean;
  refused: Refused;
  onChange: (kind: RetentionKind, row: OverrideRow) => void;
};

export function PeriodsTable({ name, items, form, isAdmin, shown, refused, onChange }: PeriodsProps) {
  return (
    <Table caption={`Retention for ${name}`} stack columns={["Records", { label: "Platform", width: "10rem" }, { label: "Team value", width: "15rem" }, { label: "Applies", width: "10rem" }]}>
      {items.map((p) => {
        const info = retentionLabels[p.kind];
        const f = form[periodField(p.kind)] ?? blank;
        const err = refused?.id === p.kind ? refused.message : shown || f.value.trim() ? periodError(p, f) : undefined;
        return (
          <Tr key={p.kind}>
            <Td>
              <LimitName label={info.label} description={info.description} />
            </Td>
            <Td muted>
              {daysText(p.default)}
              <span className={s.secondary}>{`Allowed: ${p.minDays.toLocaleString()}–${p.maxDays.toLocaleString()} days`}</span>
            </Td>
            <Td>
              {isAdmin ? (
                <>
                  <ModeSelect label={`${info.label}: team setting`} value={f.mode} inherit={daysText(p.default)} onChange={(mode) => onChange(p.kind, { ...f, mode })} />
                  {f.mode === "custom" && (
                    <Field label={`${info.label}: team days`} hideLabel error={err} className={o.amountField}>
                      <NumberInput size="sm" maximumFractionDigits={0} value={f.value} unit="days" onValueChange={(value) => onChange(p.kind, { ...f, value })} />
                    </Field>
                  )}
                </>
              ) : p.override === null ? (
                <span className={s.muted}>Inherits</span>
              ) : (
                daysText(p.override)
              )}
            </Td>
            <Td>{isAdmin && periodError(p, f) ? <NotValid /> : daysText(isAdmin ? effectivePeriod(p, f) : p.effective)}</Td>
          </Tr>
        );
      })}
    </Table>
  );
}
