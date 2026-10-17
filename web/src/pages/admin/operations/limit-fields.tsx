/* A requests-per-minute input (empty: unlimited) and a limit's name cell, shared by Admin › Limits and a team's overrides. */
import type { ReactNode } from "react";
import { Field } from "@/components/ui/field/field";
import { NumberInput } from "@/components/ui/number-input/number-input";
import s from "../../shared.module.css";
import o from "./operations.module.css";

type RateInputProps = {
  label: string;
  value: string;
  placeholder: string;
  error?: string;
  disabled?: boolean;
  onChange: (v: string) => void;
};

/** A whole number of requests per minute; the placeholder says what empty means. */
export function RateInput({ label, value, placeholder, error, disabled, onChange }: RateInputProps) {
  return (
    <Field label={`${label} (requests per minute)`} hideLabel error={error} className={o.amountField}>
      <NumberInput size="sm" maximumFractionDigits={0} placeholder={placeholder} value={value} disabled={disabled} onValueChange={onChange} unit="/ min" />
    </Field>
  );
}

export function LimitName({ label, description, children }: { label: string; description: string; children?: ReactNode }) {
  return (
    <>
      <span className={s.primary}>{label}</span>
      <span className={s.secondary}>{description}</span>
      {children}
    </>
  );
}
