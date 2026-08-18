import en from "./en.json";
import fa from "./fa.json";
import ru from "./ru.json";
import zhCN from "./zh-CN.json";
import ar from "./ar.json";

export type Locale = "en" | "fa" | "ru" | "zh-CN" | "ar";
export type TranslationKey = keyof typeof en;

const catalogs: Record<Locale, Record<string, string>> = {
  en,
  fa,
  ru,
  "zh-CN": zhCN,
  ar,
};

/** Locales offered in the UI, with the label written in that language. */
export const locales: ReadonlyArray<{ code: Locale; label: string }> = [
  { code: "en", label: "English" },
  { code: "fa", label: "فارسی" },
  { code: "ru", label: "Русский" },
  { code: "zh-CN", label: "简体中文" },
  { code: "ar", label: "العربية" },
];

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
  return locale === "fa" || locale === "ar" ? "rtl" : "ltr";
}

/** Maps a browser language tag onto a supported locale, falling back to
 *  English. navigator.language is a full tag like "fa-IR" or "zh-Hans-CN", so
 *  an exact match on our short codes would miss almost every real browser. */
export function localeFromTag(tag: string): Locale {
  const lower = tag.toLowerCase();
  if (lower.startsWith("fa") || lower.startsWith("pes")) return "fa";
  if (lower.startsWith("ru")) return "ru";
  if (lower.startsWith("ar")) return "ar";
  if (lower.startsWith("zh")) return "zh-CN";
  return "en";
}

/** Returns the key itself when a translation is missing, so gaps are visible
 *  in the UI rather than rendering as blanks. */
export function t(key: TranslationKey): string {
  return catalogs[current][key] ?? key;
}

/** BCP 47 tag for Intl. Kept in one place so digit systems -- Persian and
 *  Arabic-Indic among them -- are decided once rather than per call site. */
function intlTag(locale: Locale): string {
  switch (locale) {
    case "fa":
      return "fa-IR";
    case "ru":
      return "ru-RU";
    case "zh-CN":
      return "zh-CN";
    case "ar":
      return "ar-EG";
    default:
      return "en-US";
  }
}

/** All number formatting goes through here, so Persian digits are handled in
 *  one place instead of scattered toLocaleString calls. */
export function formatNumber(value: number, locale: Locale = current): string {
  return new Intl.NumberFormat(intlTag(locale)).format(value);
}

export function formatTimestamp(unixSeconds: number | null, locale: Locale = current): string {
  if (!unixSeconds) return t("common.never");
  return new Intl.DateTimeFormat(intlTag(locale), {
    dateStyle: "short",
    timeStyle: "medium",
  }).format(new Date(unixSeconds * 1000));
}
