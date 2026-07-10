export {};

import type { AppSnapshot, IntakeMentionCandidate } from '../types';

type TestRegistrar = (name: string, fn: () => void) => void;

declare function require(id: string): unknown;

const { describe, it } = require('node:test') as { describe: TestRegistrar; it: TestRegistrar };
const {
  buildIntakeMentionCandidates,
  filterIntakeMentionCandidates,
  findActiveIntakeMentionQuery,
  formatIntakeMentionReference,
  parseIntakeMentions,
  resolveIntakeMentions,
  splitIntakeMentionText,
} = require('./intakeMentions.ts') as typeof import('./intakeMentions');

function expect(condition: unknown, message: string) {
  if (!condition) throw new Error(message);
}

function expectEqual(actual: unknown, expected: unknown, message: string) {
  if (actual !== expected) throw new Error(`${message}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
}

function expectDeepEqual(actual: unknown, expected: unknown, message: string) {
  const actualJson = JSON.stringify(actual);
  const expectedJson = JSON.stringify(expected);
  if (actualJson !== expectedJson) throw new Error(`${message}: expected ${expectedJson}, got ${actualJson}`);
}

function snapshotFixture(): AppSnapshot {
  return {
    activeProjectId: 1,
    activeProject: { id: 1 },
    state: { project_id: 1 },
    requirements: [
      {
        id: 12,
        project_id: 1,
        title: '\u767b\u5f55\u6539\u9020',
        body: '\u9700\u8981\u652f\u6301\u90ae\u7bb1\u767b\u5f55\uff0c\u5e76\u4fdd\u7559\u624b\u673a\u53f7\u767b\u5f55\u3002',
        status: 'open',
        updated_at: '2026-07-04T10:00:00.000Z',
      },
      {
        id: 99,
        project_id: 2,
        title: '\u5176\u4ed6\u9879\u76ee\u9700\u6c42',
        body: '\u4e0d\u5e94\u51fa\u73b0\u5728\u5f53\u524d\u9879\u76ee\u5019\u9009\u4e2d',
        status: 'open',
        updated_at: '2026-07-05T10:00:00.000Z',
      },
    ],
    feedback: [
      {
        id: 7,
        project_id: 1,
        title: '\u7528\u6237\u53cd\u9988\u767b\u5f55\u6162',
        body: '\u53cd\u9988\u9996\u5c4f\u54cd\u5e94\u6162\uff0c\u9700\u8981\u6392\u67e5\u8d44\u6e90\u52a0\u8f7d\u3002',
        status: 'triaged',
        updated_at: '2026-07-06T10:00:00.000Z',
      },
    ],
  } as AppSnapshot;
}

function candidate(type: IntakeMentionCandidate['type'], id: number): IntakeMentionCandidate {
  return {
    type,
    id,
    projectId: 1,
    title: type === 'requirement' ? '\u767b\u5f55\u6539\u9020' : '\u7528\u6237\u53cd\u9988',
    summary: type === 'requirement' ? '\u652f\u6301\u90ae\u7bb1\u767b\u5f55' : '\u9996\u5c4f\u6162',
    status: 'open',
    updatedAt: '2026-07-04T10:00:00.000Z',
    canonicalText: formatIntakeMentionReference(type, id),
  };
}

describe('intake mention utility coverage', () => {
  it('parses Chinese, English, full-width hash, positions, and invalid boundaries', () => {
    const text = 'start @\u9700\u6c42#12 middle @feedback\uff037 end x@y.com skip mail@\u53cd\u9988#9';
    const mentions = parseIntakeMentions(text);

    expectEqual(mentions.length, 2, 'valid mention count');
    expectDeepEqual(
      mentions.map((mention) => ({ type: mention.type, id: mention.id, rawText: mention.rawText })),
      [
        { type: 'requirement', id: 12, rawText: '@\u9700\u6c42#12' },
        { type: 'feedback', id: 7, rawText: '@feedback\uff037' },
      ],
      'parsed mentions should preserve type/id/raw text',
    );
    expectEqual(text.slice(mentions[0].start, mentions[0].end), '@\u9700\u6c42#12', 'first mention range should match source text');
  });

  it('formats canonical mention text and resolves only known candidates', () => {
    expectEqual(formatIntakeMentionReference('requirement', 12), '@\u9700\u6c42#12', 'requirement canonical text');
    expectEqual(formatIntakeMentionReference('feedback', 7), '@\u53cd\u9988#7', 'feedback canonical text');

    const known = [candidate('requirement', 12), candidate('feedback', 7)];
    const resolved = resolveIntakeMentions('use @\u9700\u6c42#12 and @\u53cd\u9988#404 and @feedback#7', known);
    expectDeepEqual(resolved.map((mention) => `${mention.type}:${mention.id}`), ['requirement:12', 'feedback:7'], 'resolve should drop unknown references');
  });

  it('builds current-project candidates, summaries, canonical references, and search filters', () => {
    const candidates = buildIntakeMentionCandidates(snapshotFixture(), 1);

    expectDeepEqual(candidates.map((item) => `${item.type}:${item.id}`), ['feedback:7', 'requirement:12'], 'candidates should include current project records sorted by update time');
    expect(candidates.every((item) => item.projectId === 1), 'candidates should not include other projects');
    expectEqual(candidates.find((item) => item.type === 'requirement')?.canonicalText, '@\u9700\u6c42#12', 'requirement candidate should expose canonical text');
    expectEqual(filterIntakeMentionCandidates(candidates, '\u90ae\u7bb1').map((item) => item.id).join(','), '12', 'filter should match body summary text');
    expectEqual(filterIntakeMentionCandidates(candidates, 'feedback #7').map((item) => item.id).join(','), '7', 'filter should match English alias and id');
  });

  it('detects active queries and ignores complete or invalid mentions', () => {
    expectDeepEqual(findActiveIntakeMentionQuery('before @\u9700', 9), { rawText: '@\u9700', query: '\u9700', start: 7, end: 9 }, 'partial query should be active');
    expectEqual(findActiveIntakeMentionQuery('done @\u9700\u6c42#12', 'done @\u9700\u6c42#12'.length), null, 'complete mention should not keep popover open');
    expectEqual(findActiveIntakeMentionQuery('email@example.com', 'email@example.com'.length), null, 'email-like text should not be a query');
    expectEqual(findActiveIntakeMentionQuery('@\u9700\n\u6c42', '@\u9700\n'.length), null, 'multi-line query should be ignored');
  });

  it('splits resolvable mention text and leaves invalid references as plain text', () => {
    const known = [candidate('requirement', 12), candidate('feedback', 7)];
    const segments = splitIntakeMentionText('A @\u9700\u6c42#12 B @\u53cd\u9988#404 C @feedback#7', known);

    expectDeepEqual(
      segments.map((segment) => segment.kind === 'mention' ? `mention:${segment.mention.type}:${segment.mention.id}` : `text:${segment.text}`),
      ['text:A ', 'mention:requirement:12', 'text: B @\u53cd\u9988#404 C ', 'mention:feedback:7'],
      'split should only lift resolvable mentions',
    );
    expectDeepEqual(splitIntakeMentionText('plain @\u53cd\u9988#404', known), [{ kind: 'text', text: 'plain @\u53cd\u9988#404' }], 'unknown-only text should stay plain');
  });
});
