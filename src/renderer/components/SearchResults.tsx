import type {
  WorkspaceSearchGroup,
  WorkspaceSearchResult,
  WorkspaceSearchSourceType,
  WorkspaceSearchState,
  WorkspaceTab,
} from '../types';
import { formatChinaDateTime } from '../utils/time';
import { Icon, type IconName } from './icons';

const GROUP_RESULT_LIMIT = 4;

const sourceIconNames: Record<WorkspaceSearchSourceType, IconName> = {
  requirement: 'requirement',
  feedback: 'feedback',
  plan: 'plan',
  task: 'tasks',
  event: 'events',
};

interface SearchResultsProps {
  onClear: () => void;
  onSelectGroup: (targetTab: WorkspaceTab) => void;
  onSelectResult: (result: WorkspaceSearchResult) => void;
  searchState: WorkspaceSearchState;
}

export function SearchResults({ onClear, onSelectGroup, onSelectResult, searchState }: SearchResultsProps) {
  if (searchState.query.isEmpty) return null;

  const queryLabel = searchState.query.raw.trim().replace(/\s+/g, ' ') || searchState.query.normalized;

  return (
    <section className="search-results card" aria-label="统一搜索结果概览" aria-live="polite">
      <div className="search-results-head">
        <div>
          <h2>搜索结果概览</h2>
          <p>
            “{queryLabel}” 命中 <b>{searchState.total}</b> 条结果
          </p>
        </div>
        <button type="button" className="search-results-clear" onClick={onClear}>
          清空搜索
        </button>
      </div>

      <div className="search-source-strip" aria-label="按来源统计的命中数量">
        {searchState.groups.map((group) => (
          <button
            type="button"
            className={`search-source-chip${group.count ? '' : ' is-empty'}`}
            disabled={group.count === 0}
            key={group.source}
            onClick={() => onSelectGroup(group.targetTab)}
          >
            <Icon name={sourceIconNames[group.source]} size={14} aria-hidden="true" />
            <span>{group.label}</span>
            <b>{group.count}</b>
          </button>
        ))}
      </div>

      {searchState.total === 0 ? (
        <div className="search-results-empty">
          <div>没有找到与“{queryLabel}”匹配的记录。</div>
          <button type="button" className="btn btn-sm" onClick={onClear}>
            清空搜索，恢复全部列表
          </button>
        </div>
      ) : (
        <div className="search-results-body">
          <div className="search-result-groups">
            {searchState.groups.map((group) => (
              <SearchResultGroup
                group={group}
                key={group.source}
                onSelectGroup={onSelectGroup}
                onSelectResult={onSelectResult}
              />
            ))}
          </div>
        </div>
      )}
    </section>
  );
}

function SearchResultGroup({
  group,
  onSelectGroup,
  onSelectResult,
}: {
  group: WorkspaceSearchGroup;
  onSelectGroup: (targetTab: WorkspaceTab) => void;
  onSelectResult: (result: WorkspaceSearchResult) => void;
}) {
  const visibleResults = group.results.slice(0, GROUP_RESULT_LIMIT);
  const hiddenCount = Math.max(0, group.count - visibleResults.length);

  return (
    <section className={`search-result-group${group.count ? '' : ' is-empty'}`}>
      <div className="search-result-group-head">
        <div className="search-result-group-title">
          <Icon name={sourceIconNames[group.source]} size={16} aria-hidden="true" />
          <span>{group.label}</span>
        </div>
        <span className="search-result-group-count">{group.count} 条</span>
      </div>

      {visibleResults.length ? (
        <div className="search-result-items">
          {visibleResults.map((result) => (
            <SearchResultItem key={result.id} result={result} onSelect={onSelectResult} />
          ))}
          {hiddenCount ? (
            <button type="button" className="search-result-more" onClick={() => onSelectGroup(group.targetTab)}>
              在{group.label}中查看其余 {hiddenCount} 条
            </button>
          ) : null}
        </div>
      ) : (
        <div className="search-result-group-empty">无匹配</div>
      )}
    </section>
  );
}

function SearchResultItem({
  onSelect,
  result,
}: {
  onSelect: (result: WorkspaceSearchResult) => void;
  result: WorkspaceSearchResult;
}) {
  const primaryMatch = result.matches[0];
  const snippet = primaryMatch?.snippet || result.summary;
  const updatedAt = formatChinaDateTime(result.updatedAt);

  return (
    <button type="button" className="search-result-item" onClick={() => onSelect(result)}>
      <span className="search-result-item-top">
        <span className="search-result-title">{result.title}</span>
        {result.status ? <span className="search-result-status">{result.status}</span> : null}
      </span>
      <span className="search-result-snippet">
        {primaryMatch ? `${primaryMatch.label}：` : ''}
        {snippet}
      </span>
      <span className="search-result-meta">
        <span>{primaryMatch ? '点击后切换到对应列表' : '点击查看匹配记录'}</span>
        {updatedAt ? <span>{updatedAt}</span> : null}
      </span>
    </button>
  );
}
