import { DragEvent, FormEvent, KeyboardEvent, useEffect, useRef, useState } from 'react';
import type { IntakeType, PendingAttachment } from '../types';
import { Icon } from './icons';
import { autoGrowTextarea, formatBytes, getFilePath } from './shared';

interface ComposerProps {
  pendingAttachments: PendingAttachment[];
  placeholder: string;
  submitLabel: string;
  type: IntakeType;
  onAddFiles: (type: IntakeType, files: FileList | File[] | null) => void;
  onRemoveAttachment: (type: IntakeType, index: number) => void;
  onSubmit: (body: string) => Promise<boolean>;
}

export function Composer({
  pendingAttachments,
  placeholder,
  submitLabel,
  type,
  onAddFiles,
  onRemoveAttachment,
  onSubmit,
}: ComposerProps) {
  const [body, setBody] = useState('');
  const [dragOver, setDragOver] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    if (textareaRef.current) autoGrowTextarea(textareaRef.current);
  }, [body]);

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!body.trim() && !pendingAttachments.length) return;
    const succeeded = await onSubmit(body);
    if (succeeded) setBody('');
  };

  const addFiles = (files: FileList | File[] | null) => onAddFiles(type, files);

  const handleTextareaKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key !== 'Enter' || event.shiftKey || event.nativeEvent.isComposing) return;
    event.preventDefault();
    event.currentTarget.form?.requestSubmit();
  };

  const clearDragState = (event: DragEvent<HTMLElement>) => {
    const relatedTarget = event.relatedTarget as Node | null;
    if (relatedTarget && event.currentTarget.contains(relatedTarget)) return;
    setDragOver(false);
  };

  return (
    <form
      className={`composer docked-composer compact-composer${dragOver ? ' drag-over' : ''}`}
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
      <textarea
        name="body"
        onChange={(event) => setBody(event.target.value)}
        onInput={(event) => autoGrowTextarea(event.currentTarget)}
        onKeyDown={handleTextareaKeyDown}
        placeholder={placeholder}
        ref={textareaRef}
        value={body}
      />
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
          <PendingAttachmentList
            attachments={pendingAttachments}
            onRemove={(index) => onRemoveAttachment(type, index)}
          />
        </div>
        <div className="composer-actions">
          <span>Enter 发送</span>
          <button className="send-button" type="submit" aria-label={submitLabel}>
            <Icon name="send" size={20} aria-hidden="true" />
          </button>
        </div>
      </div>
    </form>
  );
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
        <div className="pending-attachment" key={`${attachment.path}-${attachment.size}`}>
          <PendingAttachmentPreview attachment={attachment} />
          <span title={attachment.name || attachment.path}>{attachment.name || attachment.path}</span>
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
      src={window.autoplan.toFileUrl(attachment.path)}
      alt={attachment.name || '附件'}
    />
  );
}
