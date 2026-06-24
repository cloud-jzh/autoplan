import { WORKSPACE_SEARCH_SOURCE_TYPES } from '../types';
import type {
  WorkspaceSearchGroup,
  WorkspaceSearchQuery,
  WorkspaceSearchSourceConfig,
  WorkspaceSearchSourceType,
  WorkspaceSearchState,
  WorkspaceTab,
} from '../types';

export const WORKSPACE_SEARCH_SOURCE_CONFIGS: WorkspaceSearchSourceConfig[] = [
  {
    type: WORKSPACE_SEARCH_SOURCE_TYPES.REQUIREMENT,
    label: '需求',
    targetTab: 'requirement',
  },
  {
    type: WORKSPACE_SEARCH_SOURCE_TYPES.FEEDBACK,
    label: '反馈',
    targetTab: 'feedback',
  },
  {
    type: WORKSPACE_SEARCH_SOURCE_TYPES.PLAN_DRAFT,
    label: '计划草稿',
    targetTab: 'tasks',
  },
  {
    type: WORKSPACE_SEARCH_SOURCE_TYPES.PLAN,
    label: 'Plan',
    targetTab: 'tasks',
  },
  {
    type: WORKSPACE_SEARCH_SOURCE_TYPES.TASK,
    label: '任务',
    targetTab: 'tasks',
  },
  {
    type: WORKSPACE_SEARCH_SOURCE_TYPES.EVENT,
    label: '事件流',
    targetTab: 'events',
  },
];

export const WORKSPACE_SEARCH_SOURCE_CONFIG_BY_TYPE = WORKSPACE_SEARCH_SOURCE_CONFIGS.reduce(
  (configs, config) => {
    configs[config.type] = config;
    return configs;
  },
  {} as Record<WorkspaceSearchSourceType, WorkspaceSearchSourceConfig>,
);

export function normalizeWorkspaceSearchQuery(query: string | null | undefined): WorkspaceSearchQuery {
  const raw = typeof query === 'string' ? query : '';
  const normalized = raw.trim().replace(/\s+/g, ' ').toLowerCase();

  return {
    raw,
    normalized,
    terms: normalized === '' ? [] : normalized.split(' '),
    isEmpty: normalized === '',
  };
}

export function isWorkspaceSearchSourceType(source: unknown): source is WorkspaceSearchSourceType {
  return (
    typeof source === 'string' &&
    Object.prototype.hasOwnProperty.call(WORKSPACE_SEARCH_SOURCE_CONFIG_BY_TYPE, source)
  );
}

export function getWorkspaceSearchSourceConfig(source: unknown): WorkspaceSearchSourceConfig | null {
  if (!isWorkspaceSearchSourceType(source)) {
    return null;
  }

  return WORKSPACE_SEARCH_SOURCE_CONFIG_BY_TYPE[source];
}

export function getWorkspaceSearchTargetTab(source: unknown): WorkspaceTab {
  return getWorkspaceSearchSourceConfig(source)?.targetTab ?? 'overview';
}

export function createEmptyWorkspaceSearchGroups(): WorkspaceSearchGroup[] {
  return WORKSPACE_SEARCH_SOURCE_CONFIGS.map((config) => ({
    source: config.type,
    label: config.label,
    targetTab: config.targetTab,
    count: 0,
    results: [],
  }));
}

export function createEmptyWorkspaceSearchState(query?: string | null): WorkspaceSearchState {
  return {
    query: normalizeWorkspaceSearchQuery(query),
    total: 0,
    results: [],
    groups: createEmptyWorkspaceSearchGroups(),
  };
}
