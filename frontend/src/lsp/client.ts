import type * as Monaco from "monaco-editor";

// ---------------------------------------------------------------------------
// Lightweight LSP-over-WebSocket client for Monaco Editor
// Supports:
//   - textDocument/completion          (补全)
//   - textDocument/hover               (悬停)
//   - textDocument/definition          (跳转定义)
//   - textDocument/references          (引用)
//   - textDocument/documentSymbol      (文档符号大纲)
//   - textDocument/codeLens            (Code Lens)
//   - textDocument/inlayHint           (Inlay Hints, Monaco 0.52+)
//   - callHierarchy/prepareCallHierarchy + incomingCalls/outgoingCalls
//   - textDocument/publishDiagnostics  → model markers
//   - textDocument/didOpen / didChange / didClose
// ---------------------------------------------------------------------------

type JsonRpcMessage =
  | { jsonrpc: "2.0"; id: number; method: string; params?: any }
  | { jsonrpc: "2.0"; id: number; result?: any; error?: any }
  | { jsonrpc: "2.0"; method: string; params?: any };

interface PendingRequest {
  resolve: (value: any) => void;
  reject: (reason: any) => void;
}

let _nextId = 1;
const nextId = () => _nextId++;

/** 当前语言是否支持 inline completion (仅对 AI 补全有意义，LSP 本身没有 inlineCompletion) */
const INLINE_CAPABLE_LANGS = new Set([
  "typescript", "javascript", "typescriptreact", "javascriptreact",
  "go", "python", "rust", "java", "cpp", "c", "csharp",
]);

export class LspClient {
  private ws: WebSocket;
  private pending = new Map<number, PendingRequest>();
  private disposables: Monaco.IDisposable[] = [];
  private initialized = false;
  private docVersions = new Map<string, number>();
  private readonly langId: string;
  private readonly monaco: typeof Monaco;

  // 文档符号缓存 (uri → symbols)
  private symbolCache = new Map<string, any[]>();
  // code lens 缓存 (uri → lenses)
  private codeLensCache = new Map<string, any[]>();

  // 供外部（面板组件）订阅的回调
  onSymbolsUpdated?: (uri: string, symbols: DocumentSymbol[]) => void;

  isReady(): boolean {
    return this.initialized;
  }

  constructor(
    port: number,
    langId: string,
    rootPath: string,
    monacoInstance: typeof Monaco
  ) {
    this.langId = langId;
    this.monaco = monacoInstance;
    const url = `ws://127.0.0.1:${port}/lsp?lang=${encodeURIComponent(langId)}&rootPath=${encodeURIComponent(rootPath)}`;
    this.ws = new WebSocket(url);
    console.log(`[LspClient:${langId}] connecting ${url}`);
    this.ws.onmessage = (e) => this.handleMessage(e.data);
    this.ws.onopen = () => { console.log(`[LspClient:${langId}] ws open`); this.doInitialize(rootPath); };
    this.ws.onerror = (e) => console.warn(`[LSP:${langId}] WebSocket error`, e);
    this.ws.onclose = (e) => { console.warn(`[LSP:${langId}] ws closed code=${e.code}`); this.cleanup(); };
  }

  // -------------------------------------------------------------------------
  // Lifecycle
  // -------------------------------------------------------------------------

  private send(msg: JsonRpcMessage): boolean {
    if (this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(msg));
      return true;
    }
    return false;
  }

  private request<T>(method: string, params?: any): Promise<T> {
    const id = nextId();
    return new Promise<T>((resolve, reject) => {
      if (this.ws.readyState !== WebSocket.OPEN && this.ws.readyState !== WebSocket.CONNECTING) {
        reject(new Error(`LSP WebSocket not connected (state=${this.ws.readyState})`));
        return;
      }
      this.pending.set(id, { resolve, reject });
      if (!this.send({ jsonrpc: "2.0", id, method, params })) {
        // ws closed between the readyState check and send — clean up immediately
        this.pending.delete(id);
        reject(new Error("LSP WebSocket closed before send"));
      }
    });
  }

  private notify(method: string, params?: any): void {
    this.send({ jsonrpc: "2.0", method, params });
  }

  private handleMessage(raw: string): void {
    let msg: any;
    try {
      msg = JSON.parse(raw);
    } catch {
      return;
    }

    if ("id" in msg && this.pending.has(msg.id)) {
      const { resolve, reject } = this.pending.get(msg.id)!;
      this.pending.delete(msg.id);
      if (msg.error) reject(msg.error);
      else resolve(msg.result);
      return;
    }

    if (msg.method === "textDocument/publishDiagnostics") {
      this.handleDiagnostics(msg.params);
    }
    // LSP server 可能主动推送 codeLens 刷新请求，忽略即可
  }

  private async doInitialize(rootPath: string): Promise<void> {
    console.log(`[LspClient:${this.langId}] doInitialize start rootPath=${rootPath}`);
    try {
      await this.request("initialize", {
        processId: null,
        clientInfo: { name: "tiancan-ai-ide", version: "1.0" },
        rootUri: pathToUri(rootPath),
        capabilities: {
          textDocument: {
            synchronization: {
              dynamicRegistration: false,
              willSave: false,
              didSave: true,
              willSaveWaitUntil: false,
            },
            completion: {
              completionItem: {
                snippetSupport: true,
                documentationFormat: ["markdown", "plaintext"],
                resolveSupport: { properties: ["documentation", "detail"] },
              },
            },
            hover: { contentFormat: ["markdown", "plaintext"] },
            definition: { dynamicRegistration: false },
            references: { dynamicRegistration: false },
            documentSymbol: {
              dynamicRegistration: false,
              hierarchicalDocumentSymbolSupport: true,
            },
            codeLens: { dynamicRegistration: false },
            inlayHint: { dynamicRegistration: false },
            callHierarchy: { dynamicRegistration: false },
            publishDiagnostics: { relatedInformation: true },
          },
          workspace: {
            applyEdit: false,
            codeLens: { refreshSupport: true },
            inlayHint: { refreshSupport: true },
          },
        },
      });
      this.notify("initialized", {});
      this.initialized = true;
      console.log(`[LspClient:${this.langId}] initialized OK`);
      this.registerMonacoProviders();
      const pending = this._pendingOpen.splice(0);
      for (const doc of pending) {
        this._doOpenDocument(doc.uri, doc.languageId, doc.text);
      }
    } catch (e) {
      console.error(`[LspClient:${this.langId}] initialize failed`, e);
    }
  }

  // -------------------------------------------------------------------------
  // Document sync helpers
  // -------------------------------------------------------------------------

  openDocument(uri: string, languageId: string, text: string): void {
    if (!this.initialized) {
      // 队列中等待初始化完成后再发送
      this._pendingOpen.push({ uri, languageId, text });
      return;
    }
    this._doOpenDocument(uri, languageId, text);
  }

  private _pendingOpen: Array<{ uri: string; languageId: string; text: string }> = [];

  private _doOpenDocument(uri: string, languageId: string, text: string): void {
    const version = 1;
    this.docVersions.set(uri, version);
    this.notify("textDocument/didOpen", {
      textDocument: { uri, languageId, version, text },
    });
    // 打开文件后立即请求符号大纲
    this.fetchDocumentSymbols(uri);
  }

  changeDocument(uri: string, text: string): void {
    if (!this.initialized) return;
    const version = (this.docVersions.get(uri) ?? 0) + 1;
    this.docVersions.set(uri, version);
    this.notify("textDocument/didChange", {
      textDocument: { uri, version },
      contentChanges: [{ text }],
    });
    // 延迟刷新符号（避免过于频繁）
    this.debouncedSymbolFetch(uri);
  }

  closeDocument(uri: string): void {
    if (!this.initialized) return;
    this.docVersions.delete(uri);
    this.notify("textDocument/didClose", { textDocument: { uri } });
    this.symbolCache.delete(uri);
    this.codeLensCache.delete(uri);
    const model = this.monaco.editor.getModel(this.monaco.Uri.parse(uri));
    if (model) {
      this.monaco.editor.setModelMarkers(model, "lsp", []);
    }
  }

  // -------------------------------------------------------------------------
  // Document Symbol
  // -------------------------------------------------------------------------

  private _symbolFetchTimers = new Map<string, ReturnType<typeof setTimeout>>();

  private debouncedSymbolFetch(uri: string) {
    if (this._symbolFetchTimers.has(uri)) {
      clearTimeout(this._symbolFetchTimers.get(uri)!);
    }
    const t = setTimeout(() => {
      this._symbolFetchTimers.delete(uri);
      this.fetchDocumentSymbols(uri);
    }, 1000);
    this._symbolFetchTimers.set(uri, t);
  }

  async fetchDocumentSymbols(uri: string): Promise<DocumentSymbol[]> {
    if (!this.initialized) return [];
    try {
      const result = await this.request<any>("textDocument/documentSymbol", {
        textDocument: { uri },
      });
      const symbols: DocumentSymbol[] = (result ?? []).map(lspSymbolToDoc);
      this.symbolCache.set(uri, symbols);
      this.onSymbolsUpdated?.(uri, symbols);
      return symbols;
    } catch {
      return [];
    }
  }

  getCachedSymbols(uri: string): DocumentSymbol[] {
    return this.symbolCache.get(uri) ?? [];
  }

  // -------------------------------------------------------------------------
  // Call Hierarchy
  // -------------------------------------------------------------------------

  async prepareCallHierarchy(uri: string, position: Monaco.Position): Promise<any[]> {
    if (!this.initialized) return [];
    return this.request<any[]>("textDocument/prepareCallHierarchy", {
      textDocument: { uri },
      position: monacoToLspPos(position),
    }).catch(() => []);
  }

  async getIncomingCalls(item: any): Promise<any[]> {
    if (!this.initialized) return [];
    return this.request<any[]>("callHierarchy/incomingCalls", { item }).catch(() => []);
  }

  async getOutgoingCalls(item: any): Promise<any[]> {
    if (!this.initialized) return [];
    return this.request<any[]>("callHierarchy/outgoingCalls", { item }).catch(() => []);
  }

  // -------------------------------------------------------------------------
  // References
  // -------------------------------------------------------------------------

  async findReferences(uri: string, position: Monaco.Position): Promise<any[]> {
    if (!this.initialized) return [];
    return this.request<any[]>("textDocument/references", {
      textDocument: { uri },
      position: monacoToLspPos(position),
      context: { includeDeclaration: true },
    }).catch(() => []);
  }

  // -------------------------------------------------------------------------
  // Diagnostics
  // -------------------------------------------------------------------------

  private handleDiagnostics(params: any): void {
    const { uri, diagnostics } = params;
    const model = this.monaco.editor.getModel(this.monaco.Uri.parse(uri));
    if (!model) return;
    const markers: Monaco.editor.IMarkerData[] = (diagnostics ?? []).map((d: any) => ({
      severity: lspSeverityToMonaco(this.monaco, d.severity),
      startLineNumber: (d.range.start.line ?? 0) + 1,
      startColumn: (d.range.start.character ?? 0) + 1,
      endLineNumber: (d.range.end.line ?? 0) + 1,
      endColumn: (d.range.end.character ?? 0) + 1,
      message: d.message,
      source: d.source,
    }));
    this.monaco.editor.setModelMarkers(model, "lsp", markers);
  }

  // -------------------------------------------------------------------------
  // Monaco provider registration
  // -------------------------------------------------------------------------

  private registerMonacoProviders(): void {
    const langId = this.langId;
    const monaco = this.monaco;

    // ── Completion ──────────────────────────────────────────────────────────
    this.disposables.push(
      monaco.languages.registerCompletionItemProvider(langId, {
        triggerCharacters: [".", "::", "(", '"', "'", "/"],
        provideCompletionItems: async (model, position) => {
          if (!this.initialized) return { suggestions: [] };
          const result = await this.request<any>("textDocument/completion", {
            textDocument: { uri: model.uri.toString() },
            position: monacoToLspPos(position),
          }).catch(() => null);
          if (!result) return { suggestions: [] };
          const items = Array.isArray(result) ? result : (result.items ?? []);
          const word = model.getWordUntilPosition(position);
          const range = {
            startLineNumber: position.lineNumber,
            endLineNumber: position.lineNumber,
            startColumn: word.startColumn,
            endColumn: word.endColumn,
          };
          return {
            suggestions: items.map((item: any) => lspCompletionToMonaco(monaco, item, range)),
          };
        },
      })
    );

    // ── Inline Completion (ghost text) ──────────────────────────────────────
    if (INLINE_CAPABLE_LANGS.has(langId)) {
      this.disposables.push(
        monaco.languages.registerInlineCompletionsProvider(langId, {
          provideInlineCompletions: async (model, position, _context, _token) => {
            if (!this.initialized) return { items: [] };
            // 利用 LSP completion 结果作为 ghost text source
            const result = await this.request<any>("textDocument/completion", {
              textDocument: { uri: model.uri.toString() },
              position: monacoToLspPos(position),
            }).catch(() => null);
            if (!result) return { items: [] };
            const items = Array.isArray(result) ? result : (result.items ?? []);
            // 取前 3 个 snippet/function 类补全作为 inline 候选
            const inlineItems = items
              .filter(
                (it: any) =>
                  it.insertTextFormat === 2 || // snippet
                  it.kind === 3 || // Function
                  it.kind === 2 || // Method
                  it.kind === 15   // Snippet
              )
              .slice(0, 3)
              .map((it: any) => ({
                insertText: it.insertText ?? it.label ?? "",
                range: {
                  startLineNumber: position.lineNumber,
                  startColumn: position.column,
                  endLineNumber: position.lineNumber,
                  endColumn: position.column,
                },
              }));
            return { items: inlineItems };
          },
          freeInlineCompletions: () => {},
        })
      );
    }

    // ── Hover ────────────────────────────────────────────────────────────────
    this.disposables.push(
      monaco.languages.registerHoverProvider(langId, {
        provideHover: async (model, position) => {
          if (!this.initialized) return null;
          const result = await this.request<any>("textDocument/hover", {
            textDocument: { uri: model.uri.toString() },
            position: monacoToLspPos(position),
          }).catch(() => null);
          if (!result?.contents) return null;
          return { contents: lspMarkupToMonaco(result.contents) };
        },
      })
    );

    // ── Go to Definition ─────────────────────────────────────────────────────
    this.disposables.push(
      monaco.languages.registerDefinitionProvider(langId, {
        provideDefinition: async (model, position) => {
          if (!this.initialized) return null;
          const result = await this.request<any>("textDocument/definition", {
            textDocument: { uri: model.uri.toString() },
            position: monacoToLspPos(position),
          }).catch(() => null);
          if (!result) return null;
          const locs = Array.isArray(result) ? result : [result];
          return locs.map((loc: any) => ({
            uri: monaco.Uri.parse(loc.uri),
            range: lspRangeToMonaco(loc.range),
          }));
        },
      })
    );

    // ── References ───────────────────────────────────────────────────────────
    this.disposables.push(
      monaco.languages.registerReferenceProvider(langId, {
        provideReferences: async (model, position, context) => {
          if (!this.initialized) return [];
          const result = await this.request<any[]>("textDocument/references", {
            textDocument: { uri: model.uri.toString() },
            position: monacoToLspPos(position),
            context: { includeDeclaration: context.includeDeclaration },
          }).catch(() => null);
          if (!result) return [];
          return result.map((loc: any) => ({
            uri: monaco.Uri.parse(loc.uri),
            range: lspRangeToMonaco(loc.range),
          }));
        },
      })
    );

    // ── Document Symbol (面包屑/大纲) ─────────────────────────────────────────
    this.disposables.push(
      monaco.languages.registerDocumentSymbolProvider(langId, {
        provideDocumentSymbols: async (model) => {
          if (!this.initialized) return [];
          const result = await this.request<any>("textDocument/documentSymbol", {
            textDocument: { uri: model.uri.toString() },
          }).catch(() => null);
          if (!result) return [];
          const symbols = (result ?? []).map(lspSymbolToDoc);
          this.symbolCache.set(model.uri.toString(), symbols);
          this.onSymbolsUpdated?.(model.uri.toString(), symbols);
          return flattenSymbolsForMonaco(monaco, symbols);
        },
      })
    );

    // ── Code Lens ────────────────────────────────────────────────────────────
    this.disposables.push(
      monaco.languages.registerCodeLensProvider(langId, {
        provideCodeLenses: async (model) => {
          if (!this.initialized) return { lenses: [], dispose: () => {} };
          const result = await this.request<any[]>("textDocument/codeLens", {
            textDocument: { uri: model.uri.toString() },
          }).catch(() => null);
          if (!result) return { lenses: [], dispose: () => {} };
          const lenses: Monaco.languages.CodeLens[] = result.map((lens: any) => ({
            range: lspRangeToMonaco(lens.range),
            id: String(lens.data ?? Math.random()),
            command: lens.command
              ? {
                  id: lens.command.command ?? "",
                  title: lens.command.title ?? "",
                  tooltip: lens.command.tooltip,
                }
              : { id: "", title: "..." },
          }));
          this.codeLensCache.set(model.uri.toString(), lenses);
          return { lenses, dispose: () => {} };
        },
        resolveCodeLens: async (_model, codeLens) => {
          // 大多数 LSP 服务器在 provideCodeLenses 时已经包含 command
          return codeLens;
        },
      })
    );

    // ── Inlay Hints ──────────────────────────────────────────────────────────
    if ((monaco.languages as any).registerInlayHintsProvider) {
      this.disposables.push(
        (monaco.languages as any).registerInlayHintsProvider(langId, {
          provideInlayHints: async (model: any, range: any) => {
            if (!this.initialized) return { hints: [], dispose: () => {} };
            const result = await this.request<any[]>("textDocument/inlayHint", {
              textDocument: { uri: model.uri.toString() },
              range: {
                start: { line: range.startLineNumber - 1, character: range.startColumn - 1 },
                end: { line: range.endLineNumber - 1, character: range.endColumn - 1 },
              },
            }).catch(() => null);
            if (!result) return { hints: [], dispose: () => {} };
            const hints = result.map((h: any) => ({
              kind: h.kind === 1 ? 1 : 2, // Type = 1, Parameter = 2
              position: {
                lineNumber: h.position.line + 1,
                column: h.position.character + 1,
              },
              label: typeof h.label === "string" ? h.label : h.label.map((p: any) => p.value).join(""),
              paddingLeft: h.paddingLeft,
              paddingRight: h.paddingRight,
            }));
            return { hints, dispose: () => {} };
          },
        })
      );
    }
  }

  // -------------------------------------------------------------------------
  // Dispose
  // -------------------------------------------------------------------------

  private cleanup(): void {
    this.initialized = false;
    this.disposables.forEach((d) => d.dispose());
    this.disposables = [];
    this.pending.forEach(({ reject }) => reject(new Error("LSP disconnected")));
    this.pending.clear();
    this._symbolFetchTimers.forEach((t) => clearTimeout(t));
    this._symbolFetchTimers.clear();
  }

  dispose(): void {
    this.cleanup();
    this.ws.close();
  }
}

// ---------------------------------------------------------------------------
// Singleton registry: one LspClient per (langId, rootPath) pair
// ---------------------------------------------------------------------------

const clients = new Map<string, LspClient>();

export function getLspClient(
  port: number,
  langId: string,
  rootPath: string,
  monaco: typeof Monaco
): LspClient {
  const key = `${langId}::${rootPath}`;
  if (!clients.has(key)) {
    clients.set(key, new LspClient(port, langId, rootPath, monaco));
  }
  return clients.get(key)!;
}

export function disposeLspClient(langId: string, rootPath: string): void {
  const key = `${langId}::${rootPath}`;
  const client = clients.get(key);
  if (client) {
    client.dispose();
    clients.delete(key);
  }
}

export function disposeAllLspClients(): void {
  clients.forEach((c) => c.dispose());
  clients.clear();
}

export function getActiveLspClient(langId: string, rootPath: string): LspClient | undefined {
  return clients.get(`${langId}::${rootPath}`);
}

// ---------------------------------------------------------------------------
// Document Symbol types (shared with UI components)
// ---------------------------------------------------------------------------

export interface DocumentSymbol {
  name: string;
  detail?: string;
  kind: number;
  range: { startLine: number; startChar: number; endLine: number; endChar: number };
  selectionRange: { startLine: number; startChar: number; endLine: number; endChar: number };
  children?: DocumentSymbol[];
}

export const SYMBOL_KIND_ICON: Record<number, string> = {
  1: "📄", // File
  2: "📦", // Module
  3: "🔲", // Namespace
  4: "📦", // Package
  5: "🏛", // Class
  6: "⚡", // Method
  7: "🔷", // Property
  8: "🔹", // Field
  9: "🔨", // Constructor
  10: "📋", // Enum
  11: "🔌", // Interface
  12: "⚙️", // Function
  13: "💊", // Variable
  14: "🔒", // Constant
  15: "🔤", // String
  16: "🔢", // Number
  17: "✅", // Boolean
  18: "📚", // Array
  19: "🗃", // Object
  20: "🔑", // Key
  21: "∅",  // Null
  22: "📋", // EnumMember
  23: "🧱", // Struct
  24: "📡", // Event
  25: "➕", // Operator
  26: "🔷", // TypeParameter
};

export const SYMBOL_KIND_LABEL: Record<number, string> = {
  5: "class", 6: "method", 7: "property", 8: "field",
  9: "constructor", 10: "enum", 11: "interface", 12: "function",
  13: "variable", 14: "constant", 22: "enum member", 23: "struct",
};

// ---------------------------------------------------------------------------
// Conversion utilities
// ---------------------------------------------------------------------------

function pathToUri(path: string): string {
  if (!path) return "";
  const p = path.startsWith("/") ? path : `/${path}`;
  return `file://${p}`;
}

function monacoToLspPos(pos: Monaco.Position): { line: number; character: number } {
  return { line: pos.lineNumber - 1, character: pos.column - 1 };
}

function lspRangeToMonaco(range: any): Monaco.IRange {
  return {
    startLineNumber: range.start.line + 1,
    startColumn: range.start.character + 1,
    endLineNumber: range.end.line + 1,
    endColumn: range.end.character + 1,
  };
}

function lspSeverityToMonaco(
  monaco: typeof Monaco,
  severity: number | undefined
): Monaco.MarkerSeverity {
  switch (severity) {
    case 1: return monaco.MarkerSeverity.Error;
    case 2: return monaco.MarkerSeverity.Warning;
    case 3: return monaco.MarkerSeverity.Info;
    default: return monaco.MarkerSeverity.Hint;
  }
}

function lspCompletionToMonaco(
  monaco: typeof Monaco,
  item: any,
  range: Monaco.IRange
): Monaco.languages.CompletionItem {
  const kind = lspCompletionKind(monaco, item.kind);
  const insertText: string = item.textEdit?.newText ?? item.insertText ?? item.label ?? "";
  const insertTextRules =
    item.insertTextFormat === 2
      ? monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet
      : undefined;
  return {
    label: item.label,
    kind,
    detail: item.detail,
    documentation: item.documentation
      ? typeof item.documentation === "string"
        ? item.documentation
        : item.documentation.value
      : undefined,
    insertText,
    insertTextRules,
    range,
    sortText: item.sortText,
    filterText: item.filterText,
  };
}

function lspCompletionKind(
  monaco: typeof Monaco,
  kind: number | undefined
): Monaco.languages.CompletionItemKind {
  const K = monaco.languages.CompletionItemKind;
  const map: Record<number, Monaco.languages.CompletionItemKind> = {
    1: K.Text, 2: K.Method, 3: K.Function, 4: K.Constructor,
    5: K.Field, 6: K.Variable, 7: K.Class, 8: K.Interface,
    9: K.Module, 10: K.Property, 11: K.Unit, 12: K.Value,
    13: K.Enum, 14: K.Keyword, 15: K.Snippet, 16: K.Color,
    17: K.File, 18: K.Reference, 19: K.Folder, 20: K.EnumMember,
    21: K.Constant, 22: K.Struct, 23: K.Event, 24: K.Operator,
    25: K.TypeParameter,
  };
  return map[kind ?? 0] ?? K.Text;
}

function lspMarkupToMonaco(contents: any): Monaco.IMarkdownString[] {
  if (!contents) return [];
  if (typeof contents === "string") return [{ value: contents }];
  if (Array.isArray(contents)) {
    return contents.map((c: any) =>
      typeof c === "string" ? { value: c } : { value: c.value ?? "" }
    );
  }
  if (contents.kind === "markdown" || contents.language) {
    return [{ value: contents.value ?? `\`\`\`${contents.language ?? ""}\n${contents.value ?? ""}\n\`\`\`` }];
  }
  return [{ value: String(contents.value ?? contents) }];
}

function lspSymbolToDoc(sym: any): DocumentSymbol {
  return {
    name: sym.name ?? "",
    detail: sym.detail,
    kind: sym.kind ?? 13,
    range: {
      startLine: (sym.range?.start?.line ?? 0),
      startChar: (sym.range?.start?.character ?? 0),
      endLine: (sym.range?.end?.line ?? 0),
      endChar: (sym.range?.end?.character ?? 0),
    },
    selectionRange: {
      startLine: (sym.selectionRange?.start?.line ?? sym.range?.start?.line ?? 0),
      startChar: (sym.selectionRange?.start?.character ?? sym.range?.start?.character ?? 0),
      endLine: (sym.selectionRange?.end?.line ?? sym.range?.end?.line ?? 0),
      endChar: (sym.selectionRange?.end?.character ?? sym.range?.end?.character ?? 0),
    },
    children: Array.isArray(sym.children) ? sym.children.map(lspSymbolToDoc) : undefined,
  };
}

function flattenSymbolsForMonaco(
  monaco: typeof Monaco,
  symbols: DocumentSymbol[]
): Monaco.languages.DocumentSymbol[] {
  return symbols.map((s) => ({
    name: s.name,
    detail: s.detail ?? "",
    kind: s.kind as Monaco.languages.SymbolKind,
    tags: [],
    range: {
      startLineNumber: s.range.startLine + 1,
      startColumn: s.range.startChar + 1,
      endLineNumber: s.range.endLine + 1,
      endColumn: s.range.endChar + 1,
    },
    selectionRange: {
      startLineNumber: s.selectionRange.startLine + 1,
      startColumn: s.selectionRange.startChar + 1,
      endLineNumber: s.selectionRange.endLine + 1,
      endColumn: s.selectionRange.endChar + 1,
    },
    children: s.children ? flattenSymbolsForMonaco(monaco, s.children) : [],
  }));
}
