import { describe, expect, it, vi } from "vitest";
import { render } from "preact";
import { ThemeToggle } from "../components/ThemeToggle.tsx";

describe("ThemeToggle", () => {
  function mount(theme: "dark" | "light", onToggle = vi.fn()) {
    const container = document.createElement("div");
    document.body.appendChild(container);
    render(<ThemeToggle theme={theme} onToggle={onToggle} />, container);
    return { container, onToggle };
  }

  it("renders without throwing in dark mode", () => {
    expect(() => mount("dark")).not.toThrow();
  });

  it("renders without throwing in light mode", () => {
    expect(() => mount("light")).not.toThrow();
  });

  it("renders a button with role=switch", () => {
    const { container } = mount("dark");
    const btn = container.querySelector('button[role="switch"]');
    expect(btn).not.toBeNull();
  });

  it("sets aria-checked=false when dark", () => {
    const { container } = mount("dark");
    const btn = container.querySelector('button[role="switch"]');
    expect(btn?.getAttribute("aria-checked")).toBe("false");
  });

  it("sets aria-checked=true when light", () => {
    const { container } = mount("light");
    const btn = container.querySelector('button[role="switch"]');
    expect(btn?.getAttribute("aria-checked")).toBe("true");
  });

  it("calls onToggle when button is clicked", () => {
    const { container, onToggle } = mount("dark");
    const btn = container.querySelector("button")!;
    btn.click();
    expect(onToggle).toHaveBeenCalledOnce();
  });

  it("renders the toggle-track and toggle-thumb elements", () => {
    const { container } = mount("dark");
    expect(container.querySelector(".toggle-track")).not.toBeNull();
    expect(container.querySelector(".toggle-thumb")).not.toBeNull();
  });

  it("applies toggle-thumb--light class when light theme", () => {
    const { container } = mount("light");
    expect(container.querySelector(".toggle-thumb--light")).not.toBeNull();
  });

  it("does not apply toggle-thumb--light class when dark theme", () => {
    const { container } = mount("dark");
    expect(container.querySelector(".toggle-thumb--light")).toBeNull();
  });
});
