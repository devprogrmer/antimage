import en from "./en.json";
import fa from "./fa.json";

export type Locale = "en" | "fa";
export type TranslationKey = keyof typeof en;

const catalogs: Record<Locale, Record<string, string>> = { en, fa };

let current: Locale = "en";

export function setLocale(locale: Locale): void {
  current = locale;
  if (typeof document !== "undefined") {
    document.documentElement.lang = locale;
    // Flipping dir here is what makes every logical CSS property resolve
    // correctly; no component needs to know the direction.
    document.documentElement.dir = dirFor(locale);
  }
}

export function getLocale(): Locale {
  return current;
}

export function dirFor(locale: Locale): "ltr" | "rtl" {
  return locale === "fa" ? "rtl" : "ltr";
}

/** Returns the key itself when a translation is missing, so gaps are visible
 *  in the UI rather than rendering as blanks. */
export function t(key: TranslationKey): string {
  return catalogs[current][key] ?? key;
}

/** All number formatting goes through here, so Persian digits are handled in
 *  one place instead of scattered toLocaleString calls. */
export function formatNumber(value: number, locale: Locale = current): string {
  return new Intl.NumberFormat(locale === "fa" ? "fa-IR" : "en-US").format(value);
}

export function formatTimestamp(unixSeconds: number | null, locale: Locale = current): string {
  if (!unixSeconds) return t("common.never");
  return new Intl.DateTimeFormat(locale === "fa" ? "fa-IR" : "en-US", {
    dateStyle: "short",
    timeStyle: "medium",
  }).format(new Date(unixSeconds * 1000));
}
