// 本文件为 ctxsvc（项目级会话 / 语义召回 / 提示词装配）的前端绑定。
// 由 wails 绑定生成器格式手工补齐（离线环境下 wails generate module 不可用），
// 调用签名与后端 Service 的导出方法一一对应，勿随意改名。

// eslint-disable-next-line @typescript-eslint/ban-ts-comment
// @ts-ignore: Unused imports
import { Call as $Call, CancellablePromise as $CancellablePromise } from "/wails/runtime.js";

const $ns = "github.com/linlurui/tiancan-ai-ide/backend/agent/ctxsvc.Service.";

export interface SessionInfo {
  id: string;
  projectRoot: string;
  projectHash: string;
  title: string;
  createdAt: number;
  updatedAt: number;
  messages: number;
  tokens: number;
  segments: number;
  openReqs: number;
  redlines: number;
  headSeq: number;
  anchors: number;
}

export interface MsgView {
  seq: number;
  role: string;
  content: string;
  time: number;
  segmentId?: string;
  tokens: number;
}

export interface Segment {
  id: string;
  title: string;
  summary?: string;
  status: string;
  openSeq: number;
  closeSeq?: number;
  msgSeqs?: number[];
  tokens?: number;
}

export interface Requirement {
  id: string;
  text: string;
  status: string;
  seq: number;
  source?: string;
}

export interface Redline {
  id: string;
  text: string;
  seq: number;
  source?: string;
  severity?: string;
  hits?: number;
}

export interface AnchorInfo {
  seq: number;
  coverFrom: number;
  time: number;
  title: string;
  messages: number;
  tokens: number;
  kind: string;
  summary: string;
  status: string;
  msgSeq: number;
  turnEndSeq: number;
}

export interface AnchorHitView {
  anchor: AnchorInfo;
  hitSeq: number;
  score: number;
  snippet: string;
}

// ── 审查（每轮文件改动与版本）──────────────────────────────────────────────

export interface AnchorFileView {
  path: string;
  status: string; // A 新增 | M 修改 | D 删除
  added: number;
  removed: number;
  updatedAt: number;
}

export interface AnchorHunkView {
  index: number;
  beforeStart: number;
  beforeEnd: number;
  afterStart: number;
  afterEnd: number;
  before: string[];
  after: string[];
  /** 被后续修改占用：UI 灰掉 + 删除线，禁用应用 */
  superseded: boolean;
  reason: string;
  applied: boolean;
}

export interface AnchorFileDetail {
  path: string;
  status: string;
  added: number;
  removed: number;
  beforeContent: string;
  afterContent: string;
  hunks: AnchorHunkView[];
}

export interface AuditEntry {
  seq: number;
  kind: string;
  time: number;
  role?: string;
  preview: string;
}

export interface RecallHitItem {
  seq: number;
  kind: string;
  role: string;
  text: string;
  preview: string;
  vec: { i: number[]; v: number[] };
}

export interface RecallHit extends RecallHitItem {
  score: number;
  vecScore: number;
  kwScore: number;
}

export interface PackedSection {
  name: string;
  tokens: number;
  text: string;
}

export interface Packed {
  system: string;
  messages: MsgView[];
  sections: PackedSection[];
  tokens: number;
  dropped?: string[];
}

export interface EmbeddingConfigInfo {
  baseUrl: string;
  model: string;
  apiKey: string;
  remote: boolean;
  degraded: boolean;
}

export interface RemoteConfig {
  baseUrl: string;
  model: string;
  apiKey: string;
  dim?: number;
  timeoutMs?: number;
}

function $wrap<T>($p: any): Promise<T> {
  return $p.then(($r: any) => $r as T);
}

/** SessionOpen 打开（或创建）会话并返回概览。 */
export function SessionOpen(projectRoot: string, sessionID: string): $CancellablePromise<SessionInfo> {
  return $wrap<SessionInfo>($Call.ByName($ns + "SessionOpen", projectRoot, sessionID));
}

/** SessionList 列出项目的所有会话。 */
export function SessionList(projectRoot: string): $CancellablePromise<SessionInfo[]> {
  return $wrap<SessionInfo[]>($Call.ByName($ns + "SessionList", projectRoot));
}

/** SessionMessages 取会话消息尾部（limit<=0 表示全部）。 */
export function SessionMessages(projectRoot: string, sessionID: string, limit: number): $CancellablePromise<MsgView[]> {
  return $wrap<MsgView[]>($Call.ByName($ns + "SessionMessages", projectRoot, sessionID, limit));
}

/** SessionAppend 记录一条消息，返回 journal seq。用户消息会自动抽取红线。 */
export function SessionAppend(projectRoot: string, sessionID: string, role: string, content: string): $CancellablePromise<number> {
  return $wrap<number>($Call.ByName($ns + "SessionAppend", projectRoot, sessionID, role, content));
}

export function SessionSegments(projectRoot: string, sessionID: string): $CancellablePromise<Segment[]> {
  return $wrap<Segment[]>($Call.ByName($ns + "SessionSegments", projectRoot, sessionID));
}

export function SessionOpenSegment(projectRoot: string, sessionID: string, title: string): $CancellablePromise<string> {
  return $wrap<string>($Call.ByName($ns + "SessionOpenSegment", projectRoot, sessionID, title));
}

export function SessionCloseSegment(projectRoot: string, sessionID: string, summary: string): $CancellablePromise<void> {
  return $wrap<void>($Call.ByName($ns + "SessionCloseSegment", projectRoot, sessionID, summary));
}

export function SessionRequirements(projectRoot: string, sessionID: string): $CancellablePromise<Requirement[]> {
  return $wrap<Requirement[]>($Call.ByName($ns + "SessionRequirements", projectRoot, sessionID));
}

export function SessionAddRequirement(projectRoot: string, sessionID: string, text: string): $CancellablePromise<number> {
  return $wrap<number>($Call.ByName($ns + "SessionAddRequirement", projectRoot, sessionID, text));
}

export function SessionDoneRequirement(projectRoot: string, sessionID: string, id: string): $CancellablePromise<void> {
  return $wrap<void>($Call.ByName($ns + "SessionDoneRequirement", projectRoot, sessionID, id));
}

/** SessionRedlines 列出会话红线。 */
export function SessionRedlines(projectRoot: string, sessionID: string): $CancellablePromise<Redline[]> {
  return $wrap<Redline[]>($Call.ByName($ns + "SessionRedlines", projectRoot, sessionID));
}

/** SessionAddRedline 手工登记一条红线。 */
export function SessionAddRedline(projectRoot: string, sessionID: string, text: string): $CancellablePromise<number> {
  return $wrap<number>($Call.ByName($ns + "SessionAddRedline", projectRoot, sessionID, text));
}

/** SessionRecall 语义召回历史片段。 */
export function SessionRecall(projectRoot: string, sessionID: string, query: string, k: number): $CancellablePromise<RecallHit[]> {
  return $wrap<RecallHit[]>($Call.ByName($ns + "SessionRecall", projectRoot, sessionID, query, k));
}

/** SessionAudit 取审计流水。 */
export function SessionAudit(projectRoot: string, sessionID: string, fromSeq: number, limit: number): $CancellablePromise<AuditEntry[]> {
  return $wrap<AuditEntry[]>($Call.ByName($ns + "SessionAudit", projectRoot, sessionID, fromSeq, limit));
}

/** SessionAnchors 列出锚点。 */
export function SessionAnchors(projectRoot: string, sessionID: string): $CancellablePromise<AnchorInfo[]> {
  return $wrap<AnchorInfo[]>($Call.ByName($ns + "SessionAnchors", projectRoot, sessionID));
}

/** SessionDropAnchor 打一个锚点快照。 */
export function SessionDropAnchor(projectRoot: string, sessionID: string): $CancellablePromise<AnchorInfo> {
  return $wrap<AnchorInfo>($Call.ByName($ns + "SessionDropAnchor", projectRoot, sessionID));
}

/** SessionRollback 回退到指定 seq（审计流水保留）。 */
export function SessionRollback(projectRoot: string, sessionID: string, target: number, note: string): $CancellablePromise<void> {
  return $wrap<void>($Call.ByName($ns + "SessionRollback", projectRoot, sessionID, target, note));
}

/** SessionArchiveAnchor 在 active / archived 之间翻转一个工作单元。归档后不再带入 prompt 全文。 */
export function SessionArchiveAnchor(projectRoot: string, sessionID: string, seq: number, archived: boolean): $CancellablePromise<void> {
  return $wrap<void>($Call.ByName($ns + "SessionArchiveAnchor", projectRoot, sessionID, seq, archived));
}

/** SessionSearchAnchors 按内容检索历史工作单元（"像之前那个做法一样"）。 */
export function SessionSearchAnchors(projectRoot: string, sessionID: string, query: string, limit: number): $CancellablePromise<AnchorHitView[] | null> {
  return $wrap<AnchorHitView[] | null>($Call.ByName($ns + "SessionSearchAnchors", projectRoot, sessionID, query, limit));
}

/** SessionAnchorFiles 列出某一轮（锚点）改动过的文件（先补抓再返回）。 */
export function SessionAnchorFiles(projectRoot: string, sessionID: string, seq: number): $CancellablePromise<AnchorFileView[] | null> {
  return $wrap<AnchorFileView[] | null>($Call.ByName($ns + "SessionAnchorFiles", projectRoot, sessionID, seq));
}

/** SessionAnchorFileDetail 某个文件在该轮的完整审查内容：两个版本 + 逐段 diff + 每段能否应用。 */
export function SessionAnchorFileDetail(projectRoot: string, sessionID: string, seq: number, path: string): $CancellablePromise<AnchorFileDetail> {
  return $wrap<AnchorFileDetail>($Call.ByName($ns + "SessionAnchorFileDetail", projectRoot, sessionID, seq, path));
}

/** SessionApplyAnchorHunk 把某一轮的某一段改动还原回工作区（局部回退）。 */
export function SessionApplyAnchorHunk(projectRoot: string, sessionID: string, seq: number, path: string, hunk: number): $CancellablePromise<void> {
  return $wrap<void>($Call.ByName($ns + "SessionApplyAnchorHunk", projectRoot, sessionID, seq, path, hunk));
}

/** SessionStats 会话与索引诊断信息。 */
export function SessionStats(projectRoot: string, sessionID: string): $CancellablePromise<{ [key: string]: any }> {
  return $wrap<{ [key: string]: any }>($Call.ByName($ns + "SessionStats", projectRoot, sessionID));
}

/** PromptPreview 装配（但不发送）一次提示词，用于查看"到底喂了什么"。 */
export function PromptPreview(projectRoot: string, sessionID: string, query: string): $CancellablePromise<Packed> {
  return $wrap<Packed>($Call.ByName($ns + "PromptPreview", projectRoot, sessionID, query));
}

/** EmbeddingConfig 当前 embedding 配置（密钥已脱敏）。 */
export function EmbeddingConfig(): $CancellablePromise<EmbeddingConfigInfo> {
  return $wrap<EmbeddingConfigInfo>($Call.ByName($ns + "EmbeddingConfig"));
}

/** Now 返回当前毫秒时间戳。 */
export function Now(): $CancellablePromise<number> {
  return $wrap<number>($Call.ByName($ns + "Now"));
}
