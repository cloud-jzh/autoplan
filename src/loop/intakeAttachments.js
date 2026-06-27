const fs = require('node:fs');
const path = require('node:path');

function intakeAttachmentOwnerTypes(intakeType) {
  return intakeType === 'feedback' ? ['feedback'] : ['requirement', 'requirements'];
}

function describeIntakeAttachment(workspace, attachment, index) {
  const name = attachmentField(attachment, ['original_name', 'originalName', 'name', 'filename', 'file_name']) || `附件 ${index + 1}`;
  const mime = attachmentField(attachment, ['mime', 'mime_type', 'mimeType', 'content_type', 'contentType']) || 'unknown';
  const declaredSize = attachmentField(attachment, ['size', 'file_size', 'fileSize']);
  const hash = attachmentField(attachment, ['sha256', 'hash', 'file_hash', 'fileHash']) || 'unknown';
  const storedPath = attachmentField(attachment, [
    'stored_path',
    'storedPath',
    'persistent_path',
    'persistentPath',
    'file_path',
    'filePath',
    'path',
  ]);
  const resolvedPath = resolveAttachmentPath(workspace, storedPath);
  let readable = false;
  let readError = '';
  let actualSize = null;

  if (!resolvedPath) {
    readError = '缺少持久化本地路径';
  } else {
    try {
      fs.accessSync(resolvedPath, fs.constants.R_OK);
      const stat = fs.statSync(resolvedPath);
      readable = stat.isFile();
      actualSize = stat.size;
      if (!readable) readError = '路径不是文件';
    } catch (error) {
      readError = error?.message || String(error);
    }
  }

  return {
    id: attachment.id,
    number: index + 1,
    name,
    mime,
    size: declaredSize ?? actualSize,
    actualSize,
    hash,
    path: resolvedPath || storedPath || '',
    readable,
    readError,
  };
}

function formatIntakeAttachmentEntry(entry) {
  const lines = [
    `- 附件 ${entry.number}: ${entry.name}`,
    `  - MIME: ${entry.mime}`,
    `  - 大小: ${formatAttachmentSize(entry.size)}`,
    `  - SHA256: ${entry.hash}`,
    `  - 持久化本地路径: ${entry.path || '（缺失）'}`,
    `  - 读取方式: 工具可以通过上述本地路径读取附件内容`,
    `  - 可读性: ${entry.readable ? '已确认可读' : `不可读：${entry.readError || '未知错误'}`}`,
  ];
  if (entry.actualSize != null && String(entry.actualSize) !== String(entry.size ?? '')) {
    lines.push(`  - 实际文件大小: ${entry.actualSize} bytes`);
  }
  return lines;
}

function attachmentField(attachment, keys) {
  for (const key of keys) {
    if (attachment?.[key] !== undefined && attachment[key] !== null && attachment[key] !== '') {
      return attachment[key];
    }
  }
  return '';
}

function resolveAttachmentPath(workspace, storedPath) {
  const value = String(storedPath || '').trim();
  if (!value) return '';
  return path.isAbsolute(value) ? value : path.resolve(workspace, value);
}

function formatAttachmentSize(size) {
  return size === undefined || size === null || size === '' ? 'unknown' : `${size} bytes`;
}

module.exports = {
  intakeAttachmentOwnerTypes,
  describeIntakeAttachment,
  formatIntakeAttachmentEntry,
};
