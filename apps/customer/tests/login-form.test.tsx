import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { LoginForm } from "@/components/login/login-form";

describe("Login form", () => {
  it("renders labeled email and password fields", () => {
    render(<LoginForm />);

    const emailInput = screen.getByLabelText(/email address/i);
    const passwordInput = screen.getByLabelText(/^password$/i);

    expect(emailInput).toHaveAttribute("type", "email");
    expect(emailInput).toHaveAttribute("name", "email");
    expect(passwordInput).toHaveAttribute("type", "password");
    expect(passwordInput).toHaveAttribute("name", "password");
  });

  it("shows validation errors when submitted empty", () => {
    render(<LoginForm />);

    fireEvent.submit(submitForm());

    // role="alert" is name-from-author per ARIA, so match by text and assert the role.
    expect(screen.getByText(/enter your email address\./i)).toHaveRole("alert");
    expect(screen.getByText(/enter your password\./i)).toHaveRole("alert");
    expect(screen.queryByText(/API integration pending/i)).not.toBeInTheDocument();
  });

  it("rejects a malformed email address", () => {
    render(<LoginForm />);

    fireEvent.change(screen.getByLabelText(/email address/i), {
      target: { value: "not-an-email" },
    });
    fireEvent.change(screen.getByLabelText(/^password$/i), {
      target: { value: "correct-horse-battery" },
    });
    fireEvent.submit(submitForm());

    expect(screen.getByText(/enter a valid email address\./i)).toHaveRole("alert");
  });

  it("shows the explicit API-integration-pending state on a valid submit without network calls", () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    render(<LoginForm />);

    fireEvent.change(screen.getByLabelText(/email address/i), {
      target: { value: "owner@example.com" },
    });
    fireEvent.change(screen.getByLabelText(/^password$/i), {
      target: { value: "correct-horse-battery" },
    });
    fireEvent.submit(submitForm());

    const status = screen.getByRole("status");
    expect(status).toHaveTextContent(/API integration pending/i);
    // The contract path and field names are surfaced, not invented.
    expect(status).toHaveTextContent(/POST \/v1\/auth\/login/);
    expect(status).toHaveTextContent(/No credentials were transmitted\./i);

    expect(fetchSpy).not.toHaveBeenCalled();
    fetchSpy.mockRestore();
  });
});

/** Dispatch a submit event on the form node (reliable in jsdom). */
function submitForm(): HTMLFormElement {
  const form = screen.getByRole("button", { name: /sign in/i }).closest("form");
  if (!form) {
    throw new Error("login form not found");
  }
  return form;
}
