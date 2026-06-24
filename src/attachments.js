const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { nowIso } = require('./database');

function saveAttachments(db, attachmentsRoot, ownerType, ownerId, files = [], projectId = null) {
  if (!Array.isArray(files) || files.length === 0) return [];

  const saved = [];
  const targetDir = path.join(attachmentsRoot, ownerType, String(ownerId));
  fs.mkdirSync(targetDir, { recursive: true });

  for (const file of files) {
    const sourcePath = file.path;
    if (!sourcePath || !fs.existsSync(sourcePath) || !fs.statSync(sourcePath).isFile()) {
      continue;
    }

    const stat = fs.statSync(sourcePath);
    const hash = hashFile(sourcePath);
    const originalName = file.name || path.basename(sourcePath);
    const storedName = `${Date.now()}-${hash.slice(0, 12)}-${safeFileName(originalName)}`;
    const storedPath = path.join(targetDir, storedName);

    fs.copyFileSync(sourcePath, storedPath);
    const id = db.insert(
      `INSERT INTO attachments
       (project_id, owner_type, owner_id, original_name, stored_path, mime_type, size, hash, created_at)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
      [
        projectId,
        ownerType,
        ownerId,
        originalName,
        storedPath,
        file.type || guessMimeType(originalName),
        stat.size,
        hash,
        nowIso(),
      ],
    );

    saved.push({
      id,
      project_id: projectId,
      owner_type: ownerType,
      owner_id: ownerId,
      original_name: originalName,
      stored_path: storedPath,
      mime_type: file.type || guessMimeType(originalName),
      size: stat.size,
      hash,
    });
  }

  return saved;
}

function hashFile(filePath) {
  return crypto.createHash('sha256').update(fs.readFileSync(filePath)).digest('hex');
}

function safeFileName(name) {
  return String(name)
    .replace(/[<>:"/\\|?*\x00-\x1F]+/g, '_')
    .replace(/\s+/g, ' ')
    .trim()
    .slice(0, 120) || 'attachment';
}

function guessMimeType(name) {
  const ext = path.extname(name).toLowerCase();
  const types = {
    '.apng': 'image/apng',
    '.avif': 'image/avif',
    '.gif': 'image/gif',
    '.jpg': 'image/jpeg',
    '.jpeg': 'image/jpeg',
    '.png': 'image/png',
    '.svg': 'image/svg+xml',
    '.webp': 'image/webp',
    '.txt': 'text/plain',
    '.md': 'text/markdown',
    '.json': 'application/json',
    '.pdf': 'application/pdf',
  };
  return types[ext] || 'application/octet-stream';
}

module.exports = { saveAttachments };
