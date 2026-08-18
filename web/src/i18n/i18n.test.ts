import { describe, expect, it } from "vitest";
import en from "./en.json";
import fa from "./fa.json";
import { dirFor, formatNumber, t, setLocale } from "./index";

describe("i18n", () => {
  it("has identical key sets in every locale", () => {
    const enKeys = Object.keys(en).sort();
    const faKeys = Object.keys(fa).sort();
    expect(faKeys).toEqual(enKeys);
  });

  it("has no empty translations", () => {
    for (const [key, value] of Object.entries(fa)) {
      expect(value, `fa.${key} is empty`).not.toBe("");
    }
  });

  it("maps locales to text direction", () => {
    expect(dirFor("en")).toBe("ltr");
    expect(dirFor("fa")).toBe("rtl");
  });

  it("returns the key itself when a translation is missing", () => {
    setLocale("en");
    expect(t("no.such.key" as never)).toBe("no.such.key");
  });

  it("formats numbers per locale, including Persian digits", () => {
    expect(formatNumber(1234, "en")).toBe("1,234");
    expect(formatNumber(1234, "fa")).toMatch(/[۰-۹]/);
  });
});
