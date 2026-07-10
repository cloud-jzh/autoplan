import type {
  AppSnapshot,
  Feedback,
  IntakeMentionCandidate,
  IntakeMentionQuery,
  IntakeMentionReference,
  IntakeMentionType,
  IntakeType,
  Requirement,
} from '../types';

const INTAKE_MENTION_PATTERN = /@(需求|反馈|requirement|feedback)\s*[#＃]\s*(\d+)/gi;
const ACTIVE_QUERY_MAX_LENGTH = 80;
const CANDIDATE_SUMMARY_MAX_LENGTH = 120;

const INTAKE_TYPE_LABELS: Record<IntakeType, string> = {
  requirement: '需求',
  feedback: '反馈',
};

const INTAKE_TYPE_ALIASES: Record<IntakeType, string[]> = {
  requirement: ['需求', 'requirement'],
  feedback: ['反馈', 'feedback'],
};

type IntakeMentionRecord = Pick<
  Requirement | Feedback,
  'id' | 'project_id' | 'title' | 'body' | 'status' | 'updated_at'
>;

export function formatIntakeMentionReference(type: IntakeMentionType, id: number): string {
  const normalizedId = normalizePositiveInteger(id);
  return `@${INTAKE_TYPE_LABELS[type]}#${normalizedId ?? id}`;
}

export function parseIntakeMentions(text: string | null | undefined): IntakeMentionReference[] {
  const input = String(text ?? '');
  if (!input) return [];

  const mentions: IntakeMentionReference[] = [];
  INTAKE_MENTION_PATTERN.lastIndex = 0;

  let match = INTAKE_MENTION_PATTERN.exec(input);
  while (match) {
    const rawText = match[0];
    const start = match.index;
    const type = normalizeIntakeMentionType(match[1]);
    const id = normalizePositiveInteger(match[2]);

    if (type && id !== null && !isBlockedMentionBoundary(input[start - 1])) {
      mentions.push({
        type,
        id,
        rawText,
        start,
        end: start + rawText.length,
      });
    }

    match = INTAKE_MENTION_PATTERN.exec(input);
  }

  return mentions;
}

export function resolveIntakeMentions(
  text: string | null | undefined,
  candidates: readonly IntakeMentionCandidate[],
): IntakeMentionReference[] {
  const candidateKeys = new Set(candidates.map(createMentionKey));
  return parseIntakeMentions(text).filter((mention) => candidateKeys.has(createMentionKey(mention)));
}


export type IntakeMentionTextSegment =
  | { kind: 'text'; text: string }
  | { kind: 'mention'; text: string; mention: IntakeMentionReference; candidate: IntakeMentionCandidate };

export function splitIntakeMentionText(
  text: string | null | undefined,
  candidates: readonly IntakeMentionCandidate[],
): IntakeMentionTextSegment[] {
  const input = String(text ?? '');
  if (!input) return [];

  const candidateLookup = new Map(candidates.map((candidate) => [createMentionKey(candidate), candidate]));
  const segments: IntakeMentionTextSegment[] = [];
  let cursor = 0;

  for (const mention of parseIntakeMentions(input)) {
    if (mention.start < cursor) continue;

    const candidate = candidateLookup.get(createMentionKey(mention));
    if (!candidate) continue;

    if (mention.start > cursor) {
      segments.push({ kind: 'text', text: input.slice(cursor, mention.start) });
    }

    segments.push({ kind: 'mention', text: mention.rawText, mention, candidate });
    cursor = mention.end;
  }

  if (cursor < input.length) {
    segments.push({ kind: 'text', text: input.slice(cursor) });
  }

  return segments.length ? segments : [{ kind: 'text', text: input }];
}
export function buildIntakeMentionCandidates(
  snapshot: AppSnapshot | null | undefined,
  projectId?: number | null,
): IntakeMentionCandidate[] {
  if (!snapshot) return [];

  const currentProjectId = normalizePositiveInteger(projectId) ?? readSnapshotProjectId(snapshot);
  if (currentProjectId === null) return [];

  const requirements = (snapshot.requirements ?? [])
    .filter((requirement) => Number(requirement.project_id) === currentProjectId)
    .map((requirement) => createIntakeMentionCandidate('requirement', requirement));
  const feedback = (snapshot.feedback ?? [])
    .filter((item) => Number(item.project_id) === currentProjectId)
    .map((item) => createIntakeMentionCandidate('feedback', item));

  return [...requirements, ...feedback].sort(compareIntakeMentionCandidates);
}

export function filterIntakeMentionCandidates(
  candidates: readonly IntakeMentionCandidate[],
  keyword: string | null | undefined,
): IntakeMentionCandidate[] {
  const normalized = normalizeCandidateSearchText(keyword);
  if (!normalized) return [...candidates];

  const terms = normalized.split(' ').filter(Boolean);
  return candidates.filter((candidate) => {
    const searchText = createCandidateSearchText(candidate);
    return terms.every((term) => searchText.includes(term));
  });
}

export function findActiveIntakeMentionQuery(
  text: string | null | undefined,
  cursorIndex: number | null | undefined,
): IntakeMentionQuery | null {
  const input = String(text ?? '');
  const cursor = clampCursorIndex(cursorIndex, input.length);
  const prefix = input.slice(0, cursor);
  const start = prefix.lastIndexOf('@');

  if (start < 0 || isBlockedMentionBoundary(input[start - 1])) return null;

  const rawText = input.slice(start, cursor);
  if (rawText.length > ACTIVE_QUERY_MAX_LENGTH + 1 || /[\r\n]/.test(rawText)) return null;

  const completeMentions = parseIntakeMentions(rawText);
  if (completeMentions.length === 1 && completeMentions[0].start === 0 && completeMentions[0].end === rawText.length) {
    return null;
  }

  return {
    rawText,
    query: rawText.slice(1),
    start,
    end: cursor,
  };
}

function normalizeIntakeMentionType(value: string | null | undefined): IntakeMentionType | null {
  const normalized = String(value ?? '').trim().toLowerCase();
  if (normalized === '需求' || normalized === 'requirement') return 'requirement';
  if (normalized === '反馈' || normalized === 'feedback') return 'feedback';
  return null;
}

function createIntakeMentionCandidate(
  type: IntakeMentionType,
  record: IntakeMentionRecord,
): IntakeMentionCandidate {
  const id = normalizePositiveInteger(record.id) ?? 0;
  const title = normalizeDisplayText(record.title) || `${INTAKE_TYPE_LABELS[type]} #${id}`;

  return {
    type,
    id,
    projectId: normalizePositiveInteger(record.project_id) ?? 0,
    title,
    summary: summarizeIntakeBody(record.body),
    status: normalizeNullableText(record.status),
    updatedAt: normalizeDisplayText(record.updated_at),
    canonicalText: formatIntakeMentionReference(type, id),
  };
}

function readSnapshotProjectId(snapshot: AppSnapshot): number | null {
  return (
    normalizePositiveInteger(snapshot.activeProjectId) ??
    normalizePositiveInteger(snapshot.activeProject?.id) ??
    normalizePositiveInteger(snapshot.state?.project_id)
  );
}

function compareIntakeMentionCandidates(
  left: IntakeMentionCandidate,
  right: IntakeMentionCandidate,
): number {
  const updatedDiff = Date.parse(right.updatedAt || '') - Date.parse(left.updatedAt || '');
  if (Number.isFinite(updatedDiff) && updatedDiff !== 0) return updatedDiff;

  const typeDiff = left.type.localeCompare(right.type);
  if (typeDiff !== 0) return typeDiff;

  return right.id - left.id;
}

function createCandidateSearchText(candidate: IntakeMentionCandidate): string {
  const aliases = INTAKE_TYPE_ALIASES[candidate.type] ?? [];
  return normalizeCandidateSearchText([
    ...aliases,
    candidate.type,
    String(candidate.id),
    `#${candidate.id}`,
    `${INTAKE_TYPE_LABELS[candidate.type]}#${candidate.id}`,
    `${candidate.type}#${candidate.id}`,
    candidate.canonicalText,
    candidate.title,
    candidate.summary,
    candidate.status,
  ].join(' '));
}

function summarizeIntakeBody(value: unknown): string {
  const normalized = normalizeDisplayText(value);
  if (!normalized) return '暂无正文';
  if (normalized.length <= CANDIDATE_SUMMARY_MAX_LENGTH) return normalized;
  return `${normalized.slice(0, CANDIDATE_SUMMARY_MAX_LENGTH - 1)}…`;
}

function normalizeDisplayText(value: unknown): string {
  return String(value ?? '').trim().replace(/\s+/g, ' ');
}

function normalizeNullableText(value: unknown): string | null {
  const normalized = normalizeDisplayText(value);
  return normalized || null;
}

function normalizeCandidateSearchText(value: unknown): string {
  return normalizeDisplayText(value).replace(/＃/g, '#').toLowerCase();
}

function normalizePositiveInteger(value: unknown): number | null {
  const numberValue = Number(value);
  return Number.isInteger(numberValue) && numberValue > 0 ? numberValue : null;
}

function createMentionKey(mention: Pick<IntakeMentionReference | IntakeMentionCandidate, 'type' | 'id'>): string {
  return `${mention.type}:${mention.id}`;
}

function clampCursorIndex(value: number | null | undefined, max: number): number {
  const cursor = Number(value);
  if (!Number.isFinite(cursor)) return max;
  return Math.min(Math.max(Math.trunc(cursor), 0), max);
}

function isBlockedMentionBoundary(value: string | undefined): boolean {
  return Boolean(value && /[A-Za-z0-9_.-]/.test(value));
}
