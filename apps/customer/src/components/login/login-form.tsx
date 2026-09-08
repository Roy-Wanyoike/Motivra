"use client";

import { useId, useState } from "react";
import { IDENTITY_ENDPOINTS } from "@/lib/api/identity";

/*
 * Sign-in form shaped to contracts/identity/openapi.yaml (1.0.0):
 *   POST /v1/auth/login — LoginRequest { email, password, device_name? }
 * Required fields: email, password. device_name is optional and left out of
 * this form to keep the first pass minimal; the client sends both required
 * fields verbatim.
 *
 * This is a foundation build: submission performs NO network call. On a valid
 * submit it shows an explicit "API integration pending" state instead.
 */

const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
/** contracts/identity/openapi.yaml — RegisterRequest.email maxLength (applies to stored emails). */
const EMAIL_MAX_LENGTH = 254;

interface FormValues {
  email: string;
  password: string;
}

type FormErrors = Partial<Record<keyof FormValues, string>>;

export function LoginForm() {
  const emailInputId = useId();
  const passwordInputId = useId();
  const [values, setValues] = useState<FormValues>({ email: "", password: "" });
  const [errors, setErrors] = useState<FormErrors>({});
  const [submittedValid, setSubmittedValid] = useState(false);

  function validate(input: FormValues): FormErrors {
    const next: FormErrors = {};
    const email = input.email.trim();
    if (!email) {
      next.email = "Enter your email address.";
    } else if (email.length > EMAIL_MAX_LENGTH) {
      next.email = `Email must be ${EMAIL_MAX_LENGTH} characters or fewer.`;
    } else if (!EMAIL_PATTERN.test(email)) {
      next.email = "Enter a valid email address.";
    }
    if (!input.password) {
      next.password = "Enter your password.";
    }
    return next;
  }

  function handleChange(event: React.ChangeEvent<HTMLInputElement>) {
    const { name, value } = event.target;
    setValues((previous) => ({ ...previous, [name]: value }));
    // Clear the field's error as soon as the user edits it again.
    setErrors((previous) => ({ ...previous, [name]: undefined }));
    setSubmittedValid(false);
  }

  function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const next = validate(values);
    setErrors(next);
    setSubmittedValid(Object.keys(next).length === 0);
  }

  return (
    <form noValidate onSubmit={handleSubmit} className="mt-8 space-y-5">
      <div>
        <label htmlFor={emailInputId} className="block text-sm font-semibold text-slate-900">
          Email address
        </label>
        <input
          id={emailInputId}
          name="email"
          type="email"
          autoComplete="email"
          required
          value={values.email}
          onChange={handleChange}
          aria-invalid={errors.email ? true : undefined}
          aria-describedby={errors.email ? `${emailInputId}-error` : `${emailInputId}-hint`}
          className="mt-1.5 block w-full rounded-md border border-slate-300 px-3 py-2 text-base text-slate-900 placeholder:text-slate-400 focus-visible:border-amber-600"
          placeholder="you@example.com"
        />
        <p id={`${emailInputId}-hint`} className="mt-1 text-xs text-slate-600">
          The address you registered with. Used only for sign-in.
        </p>
        {errors.email ? (
          <p id={`${emailInputId}-error`} role="alert" className="mt-1 text-sm font-medium text-red-700">
            {errors.email}
          </p>
        ) : null}
      </div>

      <div>
        <label htmlFor={passwordInputId} className="block text-sm font-semibold text-slate-900">
          Password
        </label>
        <input
          id={passwordInputId}
          name="password"
          type="password"
          autoComplete="current-password"
          required
          value={values.password}
          onChange={handleChange}
          aria-invalid={errors.password ? true : undefined}
          aria-describedby={errors.password ? `${passwordInputId}-error` : undefined}
          className="mt-1.5 block w-full rounded-md border border-slate-300 px-3 py-2 text-base text-slate-900 placeholder:text-slate-400 focus-visible:border-amber-600"
          placeholder="Your password"
        />
        {errors.password ? (
          <p id={`${passwordInputId}-error`} role="alert" className="mt-1 text-sm font-medium text-red-700">
            {errors.password}
          </p>
        ) : null}
      </div>

      <button
        type="submit"
        className="w-full rounded-md bg-amber-600 px-4 py-2.5 text-base font-semibold text-white hover:bg-amber-500"
      >
        Sign in
      </button>

      {submittedValid ? (
        <div
          role="status"
          className="rounded-md border border-amber-300 bg-amber-50 p-4 text-sm leading-relaxed text-slate-900"
        >
          <p className="font-semibold">API integration pending</p>
          <p className="mt-1">
            This foundation build performs no network calls. When the identity API is connected,
            this form will submit to{" "}
            <code className="rounded bg-white px-1.5 py-0.5">{IDENTITY_ENDPOINTS.login}</code>{" "}
            (field names <code className="rounded bg-white px-1.5 py-0.5">email</code> and{" "}
            <code className="rounded bg-white px-1.5 py-0.5">password</code> per the contract).
          </p>
          <p className="mt-1 font-medium">No credentials were transmitted.</p>
        </div>
      ) : null}
    </form>
  );
}
