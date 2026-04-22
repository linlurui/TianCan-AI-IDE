import React, { createContext, useContext, useState, useCallback } from "react";
import zh from "./locales/zh.json";
import en from "./locales/en.json";

type Lang = "zh" | "en";
type Locale = typeof zh;

const LOCALES: Record<Lang, Locale> = { zh, en };

interface I18nCtx {
  t: (key: string, params?: Record<string, string | number>) => string;
  lang: Lang;
  toggle: () => void;
}

const I18nContext = createContext<I18nCtx>({
  t: (k) => k,
  lang: "zh",
  toggle: () => {},
});

export function I18nProvider({ children }: { children: React.ReactNode }) {
  const init = (localStorage.getItem("tiancan-lang") ?? "zh") as Lang;
  const [lang, setLang] = useState<Lang>(init);

  const toggle = useCallback(() => {
    setLang((prev) => {
      const next: Lang = prev === "zh" ? "en" : "zh";
      localStorage.setItem("tiancan-lang", next);
      return next;
    });
  }, []);

  const t = useCallback(
    (key: string, params?: Record<string, string | number>): string => {
      const parts = key.split(".");
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      let node: any = LOCALES[lang];
      for (const p of parts) node = node?.[p];
      if (typeof node !== "string") return key;
      if (!params) return node;
      return node.replace(/\{\{(\w+)\}\}/g, (_, k) => String(params[k] ?? `{{${k}}}`));
    },
    [lang],
  );

  return React.createElement(I18nContext.Provider, { value: { t, lang, toggle } }, children);
}

export function useTranslation() {
  const { t, lang, toggle } = useContext(I18nContext);
  return { t, i18n: { language: lang }, toggleLang: toggle };
}
