import {
  ClipboardEvent,
  createContext,
  DragEvent,
  FormEvent,
  KeyboardEvent,
  type ReactNode,
  type CSSProperties,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { PENDING_ATTACHMENT_SOURCES, PLAN_GENERATION_STRATEGIES } from '../types';
import type {
  AgentCliOption,
  CodexReasoningEffort,
  IntakeMentionCandidate,
  IntakeMentionQuery,
  IntakeType,
  PendingAttachment,
  PlanBackendProvider,
  PlanGenerationInputFields,
  PlanGenerationStrategy,
} from '../types';
import { Icon } from './icons';
import { autoGrowTextarea, formatBytes, getFilePath, toSafeFileUrl } from './shared';
import {
  defaultComposerPlanGenerationSelections,
  isCodexPlanBackendProvider,
  planGenerationInputFromComposerSelection,
  type ComposerPlanGenerationSelection,
} from '../utils/workspaceForms';
import {
  filterIntakeMentionCandidates,
  findActiveIntakeMentionQuery,
} from '../utils/intakeMentions';

interface ComposerCliSelectionValue {
  options: AgentCliOption[];
  reasoningOptions: AgentCliOption[];
  selectedByType: Record<IntakeType, ComposerPlanGenerationSelection>;
  onStrategyChange: (type: IntakeType, strategy: PlanGenerationStrategy) => void;
  onProviderChange: (type: IntakeType, provider: PlanBackendProvider) => void;
  onReasoningChange: (type: IntakeType, effort: CodexReasoningEffort) => void;
}

const ComposerCliSelectionContext = createContext<ComposerCliSelectionValue | null>(null);

export function ComposerCliSelectionProvider({
  children,
  value,
}: {
  children: ReactNode;
  value: ComposerCliSelectionValue;
}) {
  return <ComposerCliSelectionContext.Provider value={value}>{children}</ComposerCliSelectionContext.Provider>;
}

interface ComposerProps {
  identityKey?: string;
  pendingAttachments: PendingAttachment[];
  mentionCandidates?: IntakeMentionCandidate[];
  placeholder: string;
  submitLabel: string;
  type: IntakeType;
  value: string;
  onValueChange: (next: string) => void;
  onAddFiles: (type: IntakeType, files: FileList | File[] | null) => void;
  onRemoveAttachment: (type: IntakeType, index: number) => void;
  onSubmit: (body: string | ComposerSubmitPayload) => Promise<boolean>;
}

export interface ComposerSubmitPayload {
  body: string;
  createAsDraft: boolean;
  planGenerationStrategy?: PlanGenerationInputFields['planGenerationStrategy'];
  planGenerationProvider?: PlanGenerationInputFields['planGenerationProvider'];
  planGenerationCommand?: PlanGenerationInputFields['planGenerationCommand'];
  planGenerationModel?: PlanGenerationInputFields['planGenerationModel'];
  planGenerationCodexReasoningEffort?: PlanGenerationInputFields['planGenerationCodexReasoningEffort'];
}

function getClipboardImageFiles(event: ClipboardEvent<HTMLTextAreaElement>) {
  const itemFiles = Array.from(event.clipboardData.items || [])
    .filter((item) => item.kind === 'file' && item.type.startsWith('image/'))
    .map((item) => item.getAsFile())
    .filter((file): file is File => Boolean(file));

  if (itemFiles.length) return itemFiles;
  return Array.from(event.clipboardData.files || []).filter((file) => file.type.startsWith('image/'));
}

export function Composer({
  identityKey,
  pendingAttachments,
  mentionCandidates = [],
  placeholder,
  submitLabel,
  type,
  value,
  onValueChange,
  onAddFiles,
  onRemoveAttachment,
  onSubmit,
}: ComposerProps) {
  const [createAsDraft, setCreateAsDraft] = useState(false);
  const [dragOver, setDragOver] = useState(false);
  const [mentionQuery, setMentionQuery] = useState<IntakeMentionQuery | null>(null);
  const [mentionSelectedIndex, setMentionSelectedIndex] = useState(0);
  const cliSelection = useContext(ComposerCliSelectionContext);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const composerIdentityKey = identityKey || type;

  useEffect(() => {
    setCreateAsDraft(false);
    setDragOver(false);
    setMentionQuery(null);
    setMentionSelectedIndex(0);
    if (fileInputRef.current) fileInputRef.current.value = '';
    if (textareaRef.current) autoGrowTextarea(textareaRef.current);
  }, [composerIdentityKey]);

  useEffect(() => {
    if (textareaRef.current) autoGrowTextarea(textareaRef.current);
  }, [value]);

  const selectedGeneration = cliSelection?.selectedByType[type] || defaultComposerPlanGenerationSelections[type];
  const selectedStrategy = selectedGeneration.strategy;
  const cliProviderOptions = cliSelection?.options || [];
  const selectedProvider = cliProviderOptions.some((option) => option.value === selectedGeneration.provider)
    ? selectedGeneration.provider
    : 'codex';
  const isCodexProvider = isCodexPlanBackendProvider(selectedProvider);
  const selectedProviderOption = cliProviderOptions.find((option) => option.value === selectedProvider);
  const selectedReasoning = selectedGeneration.codexReasoningEffort || cliSelection?.reasoningOptions[1]?.value || 'medium';
  const selectedReasoningOption = cliSelection?.reasoningOptions.find((option) => option.value === selectedReasoning);
  const draftHelp = '创建为草稿后只生成计划，不会立即进入执行队列；确认后可在计划与任务中手动执行。';
  const filteredMentionCandidates = useMemo(
    () => (mentionQuery ? filterIntakeMentionCandidates(mentionCandidates, mentionQuery.query).slice(0, 8) : []),
    [mentionCandidates, mentionQuery],
  );

  useEffect(() => {
    setMentionSelectedIndex((current) => {
      if (!filteredMentionCandidates.length) return 0;
      return Math.min(current, filteredMentionCandidates.length - 1);
    });
  }, [filteredMentionCandidates.length]);

  const changePlanGenerationProvider = (provider: PlanBackendProvider) => {
    if (!cliSelection) return;
    if (selectedStrategy === PLAN_GENERATION_STRATEGIES.BUILTIN_LLM_STRUCTURED) {
      cliSelection.onStrategyChange(type, PLAN_GENERATION_STRATEGIES.EXTERNAL_CLI_MARKDOWN);
    }
    cliSelection.onProviderChange(type, provider);
  };

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!value.trim() && !pendingAttachments.length) return;
    const payload = cliSelection
      ? {
          body: value,
          createAsDraft,
          ...planGenerationInputFromComposerSelection(selectedGeneration),
        }
      : createAsDraft ? { body: value, createAsDraft } : value;
    const succeeded = await onSubmit(payload);
    if (succeeded) {
      onValueChange('');
      setCreateAsDraft(false);
      setMentionQuery(null);
      setMentionSelectedIndex(0);
    }
  };

  const addFiles = (files: FileList | File[] | null) => onAddFiles(type, files);

  const updateMentionQuery = (text: string, cursorIndex: number | null | undefined) => {
    const nextQuery = findActiveIntakeMentionQuery(text, cursorIndex);
    setMentionQuery(nextQuery);
    setMentionSelectedIndex(0);
  };

  const insertMentionCandidate = (candidate: IntakeMentionCandidate) => {
    if (!mentionQuery) return;

    const prefix = value.slice(0, mentionQuery.start);
    const suffix = value.slice(mentionQuery.end);
    const trailingSpace = suffix && /^\s/.test(suffix) ? '' : ' ';
    const nextValue = `${prefix}${candidate.canonicalText}${trailingSpace}${suffix}`;
    const nextCursor = prefix.length + candidate.canonicalText.length + trailingSpace.length;

    onValueChange(nextValue);
    setMentionQuery(null);
    setMentionSelectedIndex(0);

    window.requestAnimationFrame(() => {
      const textarea = textareaRef.current;
      if (!textarea) return;
      textarea.focus();
      textarea.setSelectionRange(nextCursor, nextCursor);
      autoGrowTextarea(textarea);
    });
  };

  const closeMentionCandidates = () => {
    setMentionQuery(null);
    setMentionSelectedIndex(0);
  };

  const handleTextareaKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.nativeEvent.isComposing) return;

    if (mentionQuery) {
      if (event.key === 'Escape') {
        event.preventDefault();
        closeMentionCandidates();
        return;
      }

      if (event.key === 'ArrowDown' && filteredMentionCandidates.length) {
        event.preventDefault();
        setMentionSelectedIndex((current) => (current + 1) % filteredMentionCandidates.length);
        return;
      }

      if (event.key === 'ArrowUp' && filteredMentionCandidates.length) {
        event.preventDefault();
        setMentionSelectedIndex((current) => (
          current <= 0 ? filteredMentionCandidates.length - 1 : current - 1
        ));
        return;
      }

      if ((event.key === 'Enter' || event.key === 'Tab') && filteredMentionCandidates.length) {
        event.preventDefault();
        insertMentionCandidate(filteredMentionCandidates[mentionSelectedIndex] || filteredMentionCandidates[0]);
        return;
      }
    }

    if (event.key !== 'Enter' || event.shiftKey) return;
    event.preventDefault();
    event.currentTarget.form?.requestSubmit();
  };

  const handleTextareaPaste = (event: ClipboardEvent<HTMLTextAreaElement>) => {
    const imageFiles = getClipboardImageFiles(event);
    if (!imageFiles.length) return;
    addFiles(imageFiles);
  };

  const clearDragState = (event: DragEvent<HTMLElement>) => {
    const relatedTarget = event.relatedTarget as Node | null;
    if (relatedTarget && event.currentTarget.contains(relatedTarget)) return;
    setDragOver(false);
  };

  return (
    <form
      className={`composer docked-composer compact-composer${dragOver ? ' drag-over' : ''}`}
      data-composer-identity={composerIdentityKey}
      onDragLeave={clearDragState}
      onDragOver={(event) => {
        event.preventDefault();
        setDragOver(true);
      }}
      onDrop={(event) => {
        event.preventDefault();
        setDragOver(false);
        addFiles(event.dataTransfer.files);
      }}
      onSubmit={submit}
    >
      <div className="composer-mention-anchor" style={mentionAnchorStyle}>
        <textarea
          key={composerIdentityKey}
          name="body"
          onChange={(event) => {
            onValueChange(event.target.value);
            updateMentionQuery(event.target.value, event.target.selectionStart);
          }}
          onInput={(event) => autoGrowTextarea(event.currentTarget)}
          onBlur={() => {
            window.requestAnimationFrame(() => {
              if (document.activeElement !== textareaRef.current) closeMentionCandidates();
            });
          }}
          onKeyDown={handleTextareaKeyDown}
          onPaste={handleTextareaPaste}
          onSelect={(event) => updateMentionQuery(event.currentTarget.value, event.currentTarget.selectionStart)}
          placeholder={placeholder}
          ref={textareaRef}
          value={value}
        />
        {mentionQuery ? (
          <div
            className="composer-mention-popover"
            role="listbox"
            aria-label="引用候选"
            style={mentionPopoverStyle}
          >
            {filteredMentionCandidates.length ? (
              filteredMentionCandidates.map((candidate, index) => (
                <button
                  type="button"
                  key={`${candidate.type}:${candidate.id}`}
                  role="option"
                  aria-selected={index === mentionSelectedIndex}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => insertMentionCandidate(candidate)}
                  style={mentionOptionStyle(index === mentionSelectedIndex)}
                >
                  <span style={mentionReferenceStyle}>{candidate.canonicalText}</span>
                  <span style={mentionTitleStyle}>{candidate.title}</span>
                  <span style={mentionSummaryStyle}>{candidate.summary}</span>
                  <span style={mentionMetaStyle}>{formatMentionCandidateMeta(candidate)}</span>
                </button>
              ))
            ) : (
              <div className="composer-mention-empty" style={mentionEmptyStyle}>
                无匹配引用
              </div>
            )}
          </div>
        ) : null}
      </div>
      <div className="composer-bottom">
        <div className="composer-tools">
          <div
            className={`attachment-dropzone attachment-trigger${dragOver ? ' drag-over' : ''}`}
            onClick={() => fileInputRef.current?.click()}
            onKeyDown={(event) => {
              if (event.key === 'Enter' || event.key === ' ') {
                event.preventDefault();
                fileInputRef.current?.click();
              }
            }}
            role="button"
            tabIndex={0}
            aria-label="添加附件"
            title="添加附件"
          >
            <input
              multiple
              onChange={(event) => {
                addFiles(event.target.files);
                event.target.value = '';
              }}
              ref={fileInputRef}
              type="file"
            />
            <Icon name="attachment" size={20} className="attachment-trigger-icon" aria-hidden="true" />
          </div>
          {cliSelection ? (
            <div className="composer-cli-row composer-plan-row composer-cli-controls">
              <label
                className="composer-icon-select composer-cli-select"
                title={`计划生成 CLI：${selectedProviderOption?.label || selectedProvider}`}
              >
                <Icon name="cli" size={18} aria-hidden="true" />
                <span className="composer-select-label">{selectedProviderOption?.label || selectedProvider}</span>
                <select
                  aria-label="选择计划生成 CLI 后端"
                  value={selectedProvider}
                  onChange={(event) => changePlanGenerationProvider(event.target.value as PlanBackendProvider)}
                >
                  {cliProviderOptions.map((option) => (
                    <option key={option.value} value={option.value}>
                      {option.label}
                    </option>
                  ))}
                </select>
              </label>
              {isCodexProvider ? (
                <label
                  className="composer-icon-select composer-reasoning-select"
                  title={`Codex 思考深度：${selectedReasoningOption?.label || selectedReasoning}`}
                >
                  <Icon name="thinking" size={18} aria-hidden="true" />
                  <span className="composer-select-label">{selectedReasoningOption?.label || selectedReasoning}</span>
                  <select
                    aria-label="选择 Codex 思考深度"
                    value={selectedReasoning}
                    onChange={(event) => cliSelection.onReasoningChange(type, event.target.value as CodexReasoningEffort)}
                  >
                    {cliSelection.reasoningOptions.map((option) => (
                      <option key={option.value} value={option.value}>
                        {option.label}
                      </option>
                    ))}
                  </select>
                </label>
              ) : null}
            </div>
          ) : null}
          <PendingAttachmentList
            attachments={pendingAttachments}
            onRemove={(index) => onRemoveAttachment(type, index)}
          />
        </div>
        <div className="composer-actions">
          <div className="composer-draft-option">
            <label className="composer-draft-toggle" title={draftHelp}>
              <input
                checked={createAsDraft}
                onChange={(event) => setCreateAsDraft(event.target.checked)}
                type="checkbox"
              />
              <span>创建为草稿</span>
            </label>
            <span className="composer-help-trigger" title={draftHelp} aria-label={draftHelp}>
              <Icon name="help" size={14} className="composer-help-icon" aria-hidden="true" />
            </span>
          </div>
          <span>Enter 发送</span>
          <button className="send-button" type="submit" aria-label={submitLabel}>
            <Icon name="send" size={20} aria-hidden="true" />
          </button>
        </div>
      </div>
    </form>
  );
}

const mentionAnchorStyle: CSSProperties = {
  minWidth: 0,
  position: 'relative',
  width: '100%',
};

const mentionPopoverStyle: CSSProperties = {
  background: 'var(--bg-elevated)',
  border: '1px solid var(--line)',
  borderRadius: 'var(--r-md)',
  bottom: 'calc(100% + 8px)',
  boxShadow: 'var(--shadow-lg)',
  display: 'grid',
  gap: 4,
  left: 0,
  maxHeight: 280,
  overflowY: 'auto',
  padding: 6,
  position: 'absolute',
  right: 0,
  zIndex: 30,
};

const mentionReferenceStyle: CSSProperties = {
  color: 'var(--brand-600)',
  flex: '0 0 auto',
  fontFamily: 'var(--font-mono)',
  fontSize: 12,
  fontWeight: 800,
  lineHeight: 1.35,
};

const mentionTitleStyle: CSSProperties = {
  color: 'var(--text-1)',
  fontSize: 13,
  fontWeight: 750,
  lineHeight: 1.35,
  minWidth: 0,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
};

const mentionSummaryStyle: CSSProperties = {
  color: 'var(--text-2)',
  gridColumn: '1 / -1',
  fontSize: 12,
  lineHeight: 1.35,
  minWidth: 0,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
};

const mentionMetaStyle: CSSProperties = {
  color: 'var(--text-3)',
  gridColumn: '1 / -1',
  fontSize: 11.5,
  lineHeight: 1.35,
  minWidth: 0,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
};

const mentionEmptyStyle: CSSProperties = {
  color: 'var(--text-3)',
  fontSize: 12.5,
  padding: '10px 12px',
};

function mentionOptionStyle(active: boolean): CSSProperties {
  return {
    alignItems: 'start',
    background: active ? 'var(--brand-50)' : 'transparent',
    border: active ? '1px solid var(--brand-500)' : '1px solid transparent',
    borderRadius: 'var(--r-sm)',
    color: 'inherit',
    cursor: 'pointer',
    display: 'grid',
    gap: 3,
    gridTemplateColumns: 'auto minmax(0, 1fr)',
    minWidth: 0,
    padding: '8px 10px',
    textAlign: 'left',
    width: '100%',
  };
}

function formatMentionCandidateMeta(candidate: IntakeMentionCandidate) {
  const typeLabel = candidate.type === 'requirement' ? '需求' : '反馈';
  const status = candidate.status || '无状态';
  const updatedAt = candidate.updatedAt ? ` · ${candidate.updatedAt}` : '';
  return `${typeLabel} · ${status}${updatedAt}`;
}
function PendingAttachmentList({
  attachments,
  onRemove,
}: {
  attachments: PendingAttachment[];
  onRemove: (index: number) => void;
}) {
  return (
    <div className="attachment-list pending">
      {attachments.map((attachment, index) => (
        <div className="pending-attachment" key={attachment.id}>
          <PendingAttachmentPreview attachment={attachment} />
          <span title={attachment.name}>{attachment.name}</span>
          <small>{formatBytes(attachment.size)}</small>
          <button type="button" onClick={() => onRemove(index)}>
            移除
          </button>
        </div>
      ))}
    </div>
  );
}

function PendingAttachmentPreview({ attachment }: { attachment: PendingAttachment }) {
  if (!attachment.type.startsWith('image/')) {
    return (
      <span className="pending-file-icon" aria-hidden="true">
        <Icon name="file" size={22} />
      </span>
    );
  }
  return (
    <img
      className="pending-thumb"
      src={
        attachment.source === PENDING_ATTACHMENT_SOURCES.PATH
          ? toSafeFileUrl(attachment.path)
          : attachment.previewUrl
      }
      alt={attachment.name || '附件'}
    />
  );
}
