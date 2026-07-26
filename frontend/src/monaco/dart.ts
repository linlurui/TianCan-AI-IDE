import type * as Monaco from "monaco-editor";

export function registerDartLanguage(monaco: typeof Monaco): void {
  const languages = monaco.languages.getLanguages();
  if (!languages.some((lang) => lang.id === "dart")) {
    monaco.languages.register({
      id: "dart",
      extensions: [".dart"],
      aliases: ["Dart", "dart"],
      mimetypes: ["text/x-dart-source"],
    });
  }

  monaco.languages.setLanguageConfiguration("dart", {
    comments: {
      lineComment: "//",
      blockComment: ["/*", "*/"],
    },
    brackets: [
      ["{", "}"],
      ["[", "]"],
      ["(", ")"],
    ],
    autoClosingPairs: [
      { open: "{", close: "}" },
      { open: "[", close: "]" },
      { open: "(", close: ")" },
      { open: "'", close: "'", notIn: ["string", "comment"] },
      { open: '"', close: '"', notIn: ["string", "comment"] },
    ],
    surroundingPairs: [
      { open: "{", close: "}" },
      { open: "[", close: "]" },
      { open: "(", close: ")" },
      { open: "'", close: "'" },
      { open: '"', close: '"' },
    ],
  });

  monaco.languages.setMonarchTokensProvider("dart", {
    defaultToken: "",
    tokenPostfix: ".dart",
    keywords: [
      "abstract", "as", "assert", "async", "await", "base", "break", "case", "catch",
      "class", "const", "continue", "covariant", "default", "deferred", "do", "dynamic",
      "else", "enum", "export", "extends", "extension", "external", "factory", "false",
      "final", "finally", "for", "Function", "get", "hide", "if", "implements",
      "import", "in", "interface", "is", "late", "library", "mixin", "new", "null",
      "on", "operator", "part", "required", "rethrow", "return", "sealed", "set",
      "show", "static", "super", "switch", "sync", "this", "throw", "true", "try",
      "typedef", "var", "void", "when", "while", "with", "yield",
    ],
    typeKeywords: [
      "bool", "double", "int", "num", "Object", "String", "List", "Map", "Set",
      "Future", "Stream", "Iterable", "Never", "Null", "Record", "Symbol",
    ],
    operators: [
      "=", ">", "<", "!", "~", "?", ":", "==", "<=", ">=", "!=", "&&", "||", "++",
      "--", "+", "-", "*", "/", "%", "~/", "&", "|", "^", "<<", ">>", ">>>", "+=",
      "-=", "*=", "/=", "%=", "~/=", "&=", "|=", "^=", "<<=", ">>=", ">>>=", "=>",
      "..", "...", "...?", "??", "??=", "?..", "?.", "!",
    ],
    symbols: /[=><!~?:&|+\-*\/\^%]+/,
    escapes: /\\(?:[nrtbfv\\"']|x[0-9A-Fa-f]{2}|u[0-9A-Fa-f]{4}|u\{[0-9A-Fa-f]+\})/,
    tokenizer: {
      root: [
        [/[a-z_$][\w$]*/, {
          cases: {
            "@typeKeywords": "type",
            "@keywords": "keyword",
            "@default": "identifier",
          },
        }],
        [/[A-Z][\w$]*/, "type.identifier"],
        [/@[A-Za-z_$][\w$]*/, "annotation"],
        [/\/\/.*$/, "comment"],
        [/\/\*/, "comment", "@comment"],
        [/r?'''/, "string", "@tripleSingleString"],
        [/r?"""/, "string", "@tripleDoubleString"],
        [/r?'/, "string", "@singleString"],
        [/r?"/, "string", "@doubleString"],
        [/\d*\.\d+([eE][\-+]?\d+)?/, "number.float"],
        [/0[xX][0-9a-fA-F]+/, "number.hex"],
        [/\d+/, "number"],
        [/[{}()\[\]]/, "@brackets"],
        [/[;,.]/, "delimiter"],
        [/@symbols/, {
          cases: {
            "@operators": "operator",
            "@default": "",
          },
        }],
      ],
      comment: [
        [/[^\/*]+/, "comment"],
        [/\/\*/, "comment", "@push"],
        [/\*\//, "comment", "@pop"],
        [/[\/*]/, "comment"],
      ],
      singleString: [
        [/[^\\'$]+/, "string"],
        [/@escapes/, "string.escape"],
        [/\\./, "string.escape.invalid"],
        [/\$\{/, "delimiter.bracket", "@bracketCounting"],
        [/\$[a-zA-Z_$][\w$]*/, "variable"],
        [/'/, "string", "@pop"],
      ],
      doubleString: [
        [/[^\\"$]+/, "string"],
        [/@escapes/, "string.escape"],
        [/\\./, "string.escape.invalid"],
        [/\$\{/, "delimiter.bracket", "@bracketCounting"],
        [/\$[a-zA-Z_$][\w$]*/, "variable"],
        [/"/, "string", "@pop"],
      ],
      tripleSingleString: [
        [/[^'$]+/, "string"],
        [/\$\{/, "delimiter.bracket", "@bracketCounting"],
        [/\$[a-zA-Z_$][\w$]*/, "variable"],
        [/'''/, "string", "@pop"],
        [/['$]/, "string"],
      ],
      tripleDoubleString: [
        [/[^"$]+/, "string"],
        [/\$\{/, "delimiter.bracket", "@bracketCounting"],
        [/\$[a-zA-Z_$][\w$]*/, "variable"],
        [/"""/, "string", "@pop"],
        [/["$]/, "string"],
      ],
      bracketCounting: [
        [/\}/, "delimiter.bracket", "@pop"],
        [/\{/, "delimiter.bracket", "@bracketCounting"],
        [/[a-zA-Z_$][\w$]*/, {
          cases: {
            "@keywords": "keyword",
            "@default": "identifier",
          },
        }],
        [/\d+/, "number"],
        [/"([^"\\]|\\.)*$/, "string.invalid"],
        [/"/, "string", "@doubleString"],
        [/'/, "string", "@singleString"],
      ],
    },
  });
}
