import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import LandingPage from "@/app/page";

describe("Landing page", () => {
  it("renders the hero headline", () => {
    render(<LandingPage />);
    const hero = screen.getByRole("heading", {
      level: 1,
      name: /the garage comes to you — and the vehicle never forgets/i,
    });
    expect(hero).toBeInTheDocument();
  });

  it("carries the real narrative sections (problem, products, loop, honest status)", () => {
    render(<LandingPage />);
    expect(
      screen.getByRole("heading", {
        level: 2,
        name: /vehicle ownership runs on a broken service layer/i,
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { level: 2, name: /one platform\. nine product surfaces\./i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { level: 2, name: /the first loop we ship/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { level: 2, name: /honest status/i }),
    ).toBeInTheDocument();
  });

  it("links the GitHub repository in the footer", () => {
    render(<LandingPage />);
    const repoLink = screen.getByRole("link", { name: "GitHub repository" });
    expect(repoLink).toHaveAttribute("href", "https://github.com/Roy-Wanyoike/Motivra");
  });

  it("renders the product surface table with Motivra Drive highlighted", () => {
    render(<LandingPage />);
    const table = screen.getByRole("table", { name: /motivra product surfaces/i });
    expect(table).toBeInTheDocument();
    expect(screen.getByRole("rowheader", { name: /motivra drive/i })).toBeInTheDocument();
    expect(screen.getByText("this app")).toBeInTheDocument();
  });
});
