export interface PluginRec {
  id: string;
  name: string;
}

// File extension → recommended plugin (only for exts Monaco shows as plaintext without a grammar extension)
export const PLUGIN_RECOMMENDATIONS: Record<string, PluginRec> = {
  // Vue
  vue:    { id: "vue.volar",                          name: "Vue - Official" },
  // Dart / Flutter
  dart:   { id: "dart-code.dart-code",               name: "Dart" },
  // Ruby
  rb:     { id: "shopify.ruby-lsp",                  name: "Ruby LSP" },
  gemspec:{ id: "shopify.ruby-lsp",                  name: "Ruby LSP" },
  // Shell
  sh:     { id: "timonwong.shellcheck",              name: "ShellCheck" },
  bash:   { id: "timonwong.shellcheck",              name: "ShellCheck" },
  zsh:    { id: "timonwong.shellcheck",              name: "ShellCheck" },
  fish:   { id: "timonwong.shellcheck",              name: "ShellCheck" },
  // Arduino / INO
  ino:    { id: "vsciot-vscode.vscode-arduino",      name: "Arduino" },
  // Kotlin
  kt:     { id: "fwcd.kotlin",                       name: "Kotlin Language" },
  kts:    { id: "fwcd.kotlin",                       name: "Kotlin Language" },
  // PHP
  php:    { id: "bmewburn.vscode-intelephense-client", name: "PHP Intelephense" },
  // Rust  (Monaco has basic, but no advanced without ext)
  rs:     { id: "rust-lang.rust-analyzer",           name: "rust-analyzer" },
  // C / C++ (Monaco has basic, but cpptools adds IntelliSense)
  c:      { id: "ms-vscode.cpptools",                name: "C/C++" },
  cpp:    { id: "ms-vscode.cpptools",                name: "C/C++" },
  cc:     { id: "ms-vscode.cpptools",                name: "C/C++" },
  cxx:    { id: "ms-vscode.cpptools",                name: "C/C++" },
  h:      { id: "ms-vscode.cpptools",                name: "C/C++" },
  hpp:    { id: "ms-vscode.cpptools",                name: "C/C++" },
  // Java
  java:   { id: "redhat.java",                       name: "Language Support for Java" },
  // Python
  py:     { id: "ms-python.python",                  name: "Python" },
  pyw:    { id: "ms-python.python",                  name: "Python" },
  // Go
  go:     { id: "golang.go",                         name: "Go" },
  // C#
  cs:     { id: "ms-dotnettools.csharp",             name: "C#" },
  csx:    { id: "ms-dotnettools.csharp",             name: "C#" },
  // Svelte
  svelte: { id: "svelte.svelte-vscode",              name: "Svelte for VS Code" },
  // TOML
  toml:   { id: "tamasfe.even-better-toml",          name: "Even Better TOML" },
  // Markdown
  md:     { id: "yzhang.markdown-all-in-one",        name: "Markdown All in One" },
  mdx:    { id: "yzhang.markdown-all-in-one",        name: "Markdown All in One" },
};
