'use strict';

const fs = require('node:fs');
const path = require('node:path');

/**
 * 正式版本更新检查核心模块（需求 #24）。
 *
 * 职责拆分：
 * - 纯解析层（不触网，便于单测）：parseVersion / compareVersions / parseLatestRelease / hasStableUpdate。
 * - 抓取与调度层（依赖注入 app/net/db）：createUpdateChecker，负责请求 GitHub releases/latest、
 *   持久化 update.* 设置、周期调度与并发安全。
 *
 * 设计要点：抓取与解析解耦；网络异常/超时/非 2xx/JSON 解析失败一律返回结构化 { ok:false, error }，
 * 不抛崩、不影响 GUI；可自动下载安装包但不自动安装（避免与三端签名/公证冲突）。
 */

const REQUEST_TIMEOUT_MS = 15000;
const DEFAULT_REPO = 'lyming99/autoplan';
const DEFAULT_INTERVAL_MINUTES = 360;
const GITHUB_ACCEPT = 'application/vnd.github+json';

// 严格 semver 主版本号/修订号/补丁号，可选 prerelease（点分标识符），可选 build 元数据（忽略）。
// 调用前已剥离前导 v/V/=，故此处从 major 起匹配。
const VERSION_RE =
  /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+[0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*)?$/;

// prerelease 纯数字标识符：无前导零的 0 或正整数（semver §9）。
const NUMERIC_ID_RE = /^(0|[1-9]\d*)$/;

const INSTALLER_ASSET_STATUS = {
  AVAILABLE: 'available',
  UNAVAILABLE: 'unavailable',
  NOT_APPLICABLE: 'not_applicable',
};

const DOWNLOAD_PHASE = {
  IDLE: 'idle',
  UNAVAILABLE: 'unavailable',
  SKIPPED: 'skipped',
  PENDING: 'pending',
  DOWNLOADING: 'downloading',
  DOWNLOADED: 'downloaded',
  FAILED: 'failed',
};

const DOWNLOAD_PROGRESS_INTERVAL_MS = 500;

/**
 * 解析版本号为结构化对象。
 * 兼容 `v0.2.2`、`0.2.1-beta.6`、无前缀、含 build 元数据等输入。
 * @param {string|*} tag
 * @returns {{major:number,minor:number,patch:number,prerelease:string[]}|null} 非法输入返回 null，绝不抛错。
 */
function parseVersion(tag) {
  if (tag === null || tag === undefined) return null;
  const raw = String(tag).trim().replace(/^[vV=]+/, '');
  const match = VERSION_RE.exec(raw);
  if (!match) return null;
  return {
    major: Number(match[1]),
    minor: Number(match[2]),
    patch: Number(match[3]),
    prerelease: match[4] ? match[4].split('.') : [],
  };
}

/**
 * 比较 A 与 B（可为版本字符串或已解析对象）的语义版本先后。
 * @returns {-1|0|1} A<B 返回 -1，相等 0，A>B 返回 1。任一不可解析返回 0（保守视为相等，不误报更新）。
 *
 * prerelease 段遵循 semver §11：有 prerelease 者优先级低于同号稳定版（`0.2.1-beta.6 < 0.2.1`），
 * 纯数字标识符按数值比较，数字标识符低于字母标识符，更长前置相等的 prerelease 列表优先级更高。
 */
function compareVersions(a, b) {
  const pa = toParsed(a);
  const pb = toParsed(b);
  if (!pa || !pb) return 0;
  if (pa.major !== pb.major) return pa.major < pb.major ? -1 : 1;
  if (pa.minor !== pb.minor) return pa.minor < pb.minor ? -1 : 1;
  if (pa.patch !== pb.patch) return pa.patch < pb.patch ? -1 : 1;
  return comparePrerelease(pa.prerelease, pb.prerelease);
}

/**
 * 从 GitHub Release JSON 提取正式版判定所需字段和当前平台安装包资产。
 * `version` 去 tag 前导 v；`draft`/`prerelease=true`/非法版本标记为非正式版（isStable=false）。
 * @param {*} json
 * @param {{platform?:string,arch?:string}} [runtime]
 * @returns {{version:string,name:string,htmlUrl:string,publishedAt:string,body:string,summary:string,isPrerelease:boolean,isDraft:boolean,isStable:boolean,isVersionValid:boolean,installerAsset:object|null,installerAssetAvailable:boolean,installerAssetStatus:string,installerAssetReason:string}|null}
 */
function parseLatestRelease(json, runtime = {}) {
  if (!json || typeof json !== 'object') return null;
  const tag = String(json.tag_name || '').trim();
  if (!tag) return null;
  const version = stripLeadingV(tag);
  const prerelease = json.prerelease === true;
  const draft = json.draft === true;
  const isVersionValid = parseVersion(version) !== null;
  const isStable = !prerelease && !draft && isVersionValid;
  const body = typeof json.body === 'string' ? json.body : '';
  const installer = resolveInstallerAsset(json, { prerelease, draft, isVersionValid, isStable }, runtime);
  return {
    version,
    name: String(json.name || tag || '').trim(),
    htmlUrl: String(json.html_url || '').trim(),
    publishedAt: String(json.published_at || '').trim(),
    body,
    summary: summarizeBody(body),
    isPrerelease: prerelease,
    isDraft: draft,
    isStable,
    isVersionValid,
    ...installer,
  };
}

/**
 * 仅当 release 为非 prerelease、非 draft 且版本号严格大于本地版本时返回 true。
 * 保证「beta → 同号稳定版」视为有更新。
 * @param {string} currentVersion
 * @param {*} release parseLatestRelease 产物或等价对象
 * @returns {boolean}
 */
function hasStableUpdate(currentVersion, release) {
  if (!releaseIsStable(release)) return false;
  const current = parseVersion(currentVersion);
  const latest = parseVersion(release.version);
  if (!current || !latest) return false;
  return compareVersions(latest, current) > 0;
}

/**
 * 创建更新检查器实例（依赖注入以便单测）。
 * @param {{app?:{getVersion?:Function,getPath?:Function}, net?:{request?:Function}, db?:object, repo?:string, fetch?:Function, onCheck?:Function, runtime?:{platform?:string,arch?:string}, downloadDir?:string, downloadFile?:Function}} opts
 */
function createUpdateChecker(opts = {}) {
  const { app, net, db, repo, fetch, onCheck, runtime, downloadDir, downloadFile } = opts;
  const repoSlug = repo || DEFAULT_REPO;

  let inflight = null; // 进行中的检查 Promise，重复触发直接复用，避免并发重复请求
  let activeDownload = null; // 当前安装包下载任务，按 version + asset 去重
  const timer = { handle: null };

  function localVersion() {
    return typeof app?.getVersion === 'function' ? String(app.getVersion() || '0.0.0') : '0.0.0';
  }

  function userAgent() {
    return `autoplan/${localVersion()}`;
  }

  function readSetting(key, fallback) {
    return db && typeof db.getSetting === 'function' ? db.getSetting(key, fallback) : fallback;
  }

  function writeSetting(key, value) {
    if (db && typeof db.setSetting === 'function') db.setSetting(key, value);
  }

  function writeSettings(entries) {
    const normalized = entries.map(([key, value]) => [key, String(value)]);
    if (db && typeof db.runBatch === 'function') {
      db.runBatch(
        normalized.map(([key, value]) => ({
          sql: 'INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)',
          params: [key, value],
        }))
      );
      return;
    }
    for (const [key, value] of normalized) writeSetting(key, value);
  }

  function emitStatus() {
    if (typeof onCheck !== 'function') return;
    try {
      onCheck(status());
    } catch {
      /* 状态推送失败不影响下载状态机 */
    }
  }

  function autoCheckEnabled() {
    // 默认开启：仅在显式 'false' 时关闭。
    return readSetting('update.autoCheck', 'true') !== 'false';
  }

  function intervalMinutes() {
    const value = Number(readSetting('update.intervalMinutes', String(DEFAULT_INTERVAL_MINUTES)));
    return Number.isFinite(value) && value > 0 ? value : DEFAULT_INTERVAL_MINUTES;
  }

  function installerDownloadRoot() {
    if (downloadDir) return downloadDir;
    if (app && typeof app.getPath === 'function') return path.join(app.getPath('userData'), 'updates', 'downloads');
    return defaultDownloadDir();
  }

  /**
   * 触发一次检查（并发安全：进行中则复用同一 Promise）。
   * 成功持久化 update.lastCheckedAt 与最新正式版缓存；失败返回结构化错误。
   * @returns {Promise<object>} 始终 resolve，含 ok/error 及 status() 快照。
   */
  function check() {
    if (inflight) return inflight; // 进行中则复用同一 Promise，避免并发重复请求
    const run = async () => {
      let head;
      try {
        const json = await fetchLatestRelease();
        const release = parseLatestRelease(json, runtime);
        persistAfterCheck(release);
        maybeStartInstallerDownload(release);
        head = { ok: true, error: null, release: release || null };
      } catch (error) {
        head = { ok: false, error: errorMessage(error), release: null };
      }
      const result = { ...head, ...status() };
      // 检查完成回调（主进程据此向渲染进程推送 updates:status），覆盖手动与周期检查。
      if (typeof onCheck === 'function') {
        try {
          onCheck(result);
        } catch {
          /* 回调异常不影响检查结果 */
        }
      }
      return result;
    };
    inflight = run().finally(() => {
      inflight = null;
    });
    return inflight;
  }

  function persistAfterCheck(release) {
    writeSetting('update.lastCheckedAt', nowIsoSafe());
    if (release && release.version) {
      writeSetting('update.latestVersion', release.version);
      writeSetting('update.latestVersionName', release.name || '');
      writeSetting('update.latestHtmlUrl', release.htmlUrl || '');
      writeSetting('update.latestPublishedAt', release.publishedAt || '');
      writeSetting('update.latestIsPrerelease', String(release.isPrerelease === true));
      writeSetting('update.latestIsDraft', String(release.isDraft === true));
      writeSetting('update.latestIsStable', String(release.isStable === true));
      persistInstallerAsset(release);
    }
  }

  function persistInstallerAsset(release) {
    const asset = release && release.installerAsset ? release.installerAsset : null;
    writeSettings([
      ['update.installerAssetAvailable', String(Boolean(asset))],
      ['update.installerAssetStatus', release?.installerAssetStatus || INSTALLER_ASSET_STATUS.UNAVAILABLE],
      ['update.installerAssetReason', release?.installerAssetReason || ''],
      ['update.installerAssetName', asset?.name || ''],
      ['update.installerAssetDownloadUrl', asset?.downloadUrl || ''],
      ['update.installerAssetSize', asset ? String(asset.size || 0) : ''],
      ['update.installerAssetPlatform', asset?.platform || ''],
      ['update.installerAssetArch', asset?.arch || ''],
      ['update.installerAssetKind', asset?.kind || ''],
    ]);
  }

  function maybeStartInstallerDownload(release) {
    if (!release || !release.version) return;
    const currentVersion = localVersion();
    if (!hasStableUpdate(currentVersion, release)) {
      abortActiveDownloadForVersion(release.version);
      persistDownloadState({
        phase: DOWNLOAD_PHASE.IDLE,
        reason: release.isStable ? 'no_update' : release.installerAssetReason || 'not_stable',
        version: release.version,
      });
      return;
    }

    if (release.version === readSetting('update.dismissedVersion', '')) {
      abortActiveDownloadForVersion(release.version);
      persistDownloadState({
        phase: DOWNLOAD_PHASE.SKIPPED,
        reason: 'dismissed',
        version: release.version,
      });
      return;
    }

    const asset = release.installerAsset || null;
    if (!asset || !asset.downloadUrl) {
      abortActiveDownloadForVersion(release.version);
      persistDownloadState({
        phase: DOWNLOAD_PHASE.UNAVAILABLE,
        reason: release.installerAssetReason || 'no_installer_asset',
        version: release.version,
      });
      return;
    }

    let target;
    try {
      target = installerDownloadTarget(release.version, asset, installerDownloadRoot());
    } catch (error) {
      persistDownloadState({
        phase: DOWNLOAD_PHASE.FAILED,
        reason: 'invalid_download_path',
        error: errorMessage(error),
        version: release.version,
        totalBytes: asset.size || 0,
      });
      emitStatus();
      return;
    }

    const key = installerDownloadKey(release.version, asset, target.filePath);
    if (installerFileExistsSync(target.filePath, asset)) {
      persistDownloadState({
        phase: DOWNLOAD_PHASE.DOWNLOADED,
        reason: 'existing_file',
        version: release.version,
        assetKey: key,
        localPath: target.filePath,
        progress: 100,
        totalBytes: asset.size || 0,
        bytesReceived: asset.size || 0,
        completedAt: nowIsoSafe(),
      });
      return;
    }

    const stored = status();
    if (
      stored.downloadPhase === DOWNLOAD_PHASE.DOWNLOADED &&
      stored.downloadAssetKey === key &&
      stored.localInstallerPath &&
      path.resolve(stored.localInstallerPath) === path.resolve(target.filePath) &&
      installerFileExistsSync(stored.localInstallerPath, asset)
    ) {
      persistDownloadState({
        phase: DOWNLOAD_PHASE.DOWNLOADED,
        reason: 'existing_file',
        version: release.version,
        assetKey: key,
        localPath: stored.localInstallerPath,
        progress: 100,
        totalBytes: asset.size || stored.downloadTotalBytes || 0,
        bytesReceived: stored.downloadBytesReceived || asset.size || 0,
        completedAt: stored.downloadCompletedAt || nowIsoSafe(),
      });
      return;
    }

    if (activeDownload && activeDownload.key === key) return;
    abortActiveDownload();

    const controller = { aborted: false, abort: null };
    persistDownloadState({
      phase: DOWNLOAD_PHASE.PENDING,
      reason: 'queued',
      version: release.version,
      assetKey: key,
      progress: 0,
      totalBytes: asset.size || 0,
      bytesReceived: 0,
      startedAt: nowIsoSafe(),
    });

    const promise = runInstallerDownload(release, asset, target, key, controller).finally(() => {
      if (activeDownload && activeDownload.key === key) activeDownload = null;
    });
    activeDownload = {
      key,
      version: release.version,
      abort: () => {
        controller.aborted = true;
        if (typeof controller.abort === 'function') controller.abort();
      },
      promise,
    };
  }

  async function runInstallerDownload(release, asset, target, key, controller) {
    const startedAt = readSetting('update.downloadStartedAt', '') || nowIsoSafe();
    const progress = createProgressReporter({
      version: release.version,
      asset,
      key,
      startedAt,
      localPath: '',
    });
    try {
      await fs.promises.mkdir(target.dir, { recursive: true });
      if (await installerFileExists(target.filePath, asset)) {
        persistDownloadState({
          phase: DOWNLOAD_PHASE.DOWNLOADED,
          reason: 'existing_file',
          version: release.version,
          assetKey: key,
          localPath: target.filePath,
          progress: 100,
          totalBytes: asset.size || 0,
          bytesReceived: asset.size || 0,
          startedAt,
          completedAt: nowIsoSafe(),
        });
        emitStatus();
        return;
      }

      persistDownloadState({
        phase: DOWNLOAD_PHASE.DOWNLOADING,
        reason: 'downloading',
        version: release.version,
        assetKey: key,
        progress: 0,
        totalBytes: asset.size || 0,
        bytesReceived: 0,
        startedAt,
      });
      emitStatus();

      const result = (await downloadInstallerAsset(asset, target, controller, progress)) || {};
      if (controller.aborted) return;
      await replaceFile(target.tempPath, target.filePath);
      persistDownloadState({
        phase: DOWNLOAD_PHASE.DOWNLOADED,
        reason: 'completed',
        version: release.version,
        assetKey: key,
        localPath: target.filePath,
        progress: 100,
        totalBytes: result.totalBytes || asset.size || result.bytesReceived || 0,
        bytesReceived: result.bytesReceived || result.totalBytes || asset.size || 0,
        startedAt,
        completedAt: nowIsoSafe(),
      });
      emitStatus();
    } catch (error) {
      await removeFileQuietly(target.tempPath);
      if (controller.aborted) return;
      persistDownloadState({
        phase: DOWNLOAD_PHASE.FAILED,
        reason: 'download_failed',
        error: errorMessage(error),
        version: release.version,
        assetKey: key,
        progress: Number(readSetting('update.downloadProgress', '0')) || 0,
        bytesReceived: Number(readSetting('update.downloadBytesReceived', '0')) || 0,
        totalBytes: asset.size || Number(readSetting('update.downloadTotalBytes', '0')) || 0,
        startedAt,
      });
      emitStatus();
    }
  }

  function createProgressReporter(base) {
    let lastEmitAt = 0;
    let lastProgress = -1;
    return (next = {}) => {
      const bytesReceived = normalizeAssetSize(next.bytesReceived);
      const totalBytes = normalizeAssetSize(next.totalBytes || base.asset.size || 0);
      const progress = totalBytes > 0 ? Math.max(0, Math.min(99, Math.floor((bytesReceived / totalBytes) * 100))) : 0;
      const now = Date.now();
      if (progress === lastProgress && now - lastEmitAt < DOWNLOAD_PROGRESS_INTERVAL_MS) return;
      lastEmitAt = now;
      lastProgress = progress;
      persistDownloadState({
        phase: DOWNLOAD_PHASE.DOWNLOADING,
        reason: 'downloading',
        version: base.version,
        assetKey: base.key,
        localPath: base.localPath,
        progress,
        bytesReceived,
        totalBytes,
        startedAt: base.startedAt,
      });
      emitStatus();
    };
  }

  async function downloadInstallerAsset(asset, target, controller, onProgress) {
    if (typeof downloadFile === 'function') {
      return downloadFile({
        asset,
        filePath: target.filePath,
        tempPath: target.tempPath,
        onProgress,
        signal: controller,
      });
    }
    return downloadWithNet(asset, target.tempPath, controller, onProgress);
  }

  function downloadWithNet(asset, tempPath, controller, onProgress) {
    return new Promise((resolve, reject) => {
      if (!net || typeof net.request !== 'function') {
        reject(new Error('network unavailable'));
        return;
      }
      let request;
      let output;
      let settled = false;
      let bytesReceived = 0;
      let totalBytes = normalizeAssetSize(asset.size);

      const settle = (error, result) => {
        if (settled) return;
        settled = true;
        if (output) output.destroy();
        if (error) reject(error);
        else resolve(result);
      };

      try {
        request = net.request({ url: asset.downloadUrl, method: 'GET', redirect: 'follow' });
      } catch (error) {
        reject(error);
        return;
      }
      controller.abort = () => {
        controller.aborted = true;
        try {
          request?.abort();
        } catch {
          /* 中止下载为尽力而为 */
        }
      };
      try {
        request.setHeader('User-Agent', userAgent());
        request.setHeader('Accept', 'application/octet-stream');
      } catch {
        /* 头部设置失败交由请求本身返回错误 */
      }
      request.on('response', (response) => {
        const status = Number(response.statusCode || 0);
        if (status < 200 || status >= 300) {
          response.on('data', () => {});
          response.on('end', () => settle(new Error(`HTTP ${status || 'unknown'}`)));
          response.on('error', (error) => settle(error));
          return;
        }

        const contentLength = headerNumber(response.headers, 'content-length');
        if (contentLength > 0) totalBytes = contentLength;
        output = fs.createWriteStream(tempPath);
        output.on('error', (error) => settle(error));
        response.on('data', (chunk) => {
          const buffer = Buffer.from(chunk);
          bytesReceived += buffer.length;
          if (!output.write(buffer)) {
            try {
              response.pause();
              output.once('drain', () => response.resume());
            } catch {
              /* pause/resume 在 Electron net 流上为尽力而为 */
            }
          }
          onProgress({ bytesReceived, totalBytes });
        });
        response.on('end', () => {
          if (totalBytes > 0 && bytesReceived !== totalBytes) {
            settle(new Error(`incomplete download: ${bytesReceived}/${totalBytes}`));
            return;
          }
          output.end(() => settle(null, { bytesReceived, totalBytes }));
        });
        response.on('error', (error) => settle(error));
      });
      request.on('error', (error) => {
        if (controller.aborted) settle(new Error('download aborted'));
        else settle(error);
      });
      try {
        request.end();
      } catch (error) {
        settle(error);
      }
    });
  }

  function persistDownloadState(next = {}) {
    const phase = next.phase || DOWNLOAD_PHASE.IDLE;
    const progress = clampPercent(next.progress);
    const localPath = next.localPath || '';
    writeSettings([
      ['update.downloadPhase', phase],
      ['update.downloadProgress', String(progress)],
      ['update.downloadError', next.error || ''],
      ['update.downloadReason', next.reason || ''],
      ['update.downloadStartedAt', next.startedAt || ''],
      ['update.downloadCompletedAt', next.completedAt || ''],
      ['update.downloadBytesReceived', String(normalizeAssetSize(next.bytesReceived))],
      ['update.downloadTotalBytes', String(normalizeAssetSize(next.totalBytes))],
      ['update.downloadAssetKey', next.assetKey || ''],
      ['update.downloadVersion', next.version || ''],
      ['update.localInstallerPath', localPath],
    ]);
  }

  function abortActiveDownloadForVersion(version) {
    if (activeDownload && activeDownload.version === version) abortActiveDownload();
  }

  function abortActiveDownload() {
    if (!activeDownload) return;
    try {
      activeDownload.abort();
    } catch {
      /* 中止失败不影响后续状态写入 */
    }
    activeDownload = null;
  }

  async function fetchLatestRelease() {
    const url = `https://api.github.com/repos/${repoSlug}/releases/latest`;
    if (typeof fetch === 'function') return fetchWithInjected(fetch, url);
    return fetchWithNet(net, url);
  }

  // 注入式 fetch（单测用）：复用 fetch API 形态，带超时中止。
  async function fetchWithInjected(fetchFn, url) {
    const controller = typeof AbortController !== 'undefined' ? new AbortController() : null;
    const timeout = setTimeout(() => {
      try {
        controller?.abort();
      } catch {
        /* 超时中止为尽力而为 */
      }
    }, REQUEST_TIMEOUT_MS);
    try {
      const response = await fetchFn(url, {
        method: 'GET',
        headers: { 'User-Agent': userAgent(), Accept: GITHUB_ACCEPT },
        signal: controller?.signal,
      });
      if (!response || !response.ok) throw new Error(`HTTP ${response ? response.status : 'unknown'}`);
      const text = await response.text();
      return JSON.parse(text);
    } finally {
      clearTimeout(timeout);
    }
  }

  // 生产路径：Electron net.request，正确复用系统代理，带 User-Agent/Accept 头与超时中止。
  function fetchWithNet(netModule, url) {
    return new Promise((resolve, reject) => {
      if (!netModule || typeof netModule.request !== 'function') {
        reject(new Error('network unavailable'));
        return;
      }
      let request;
      let settled = false;
      const timeout = setTimeout(() => {
        if (settled) return;
        settled = true;
        try {
          request?.abort();
        } catch {
          /* 忽略中止异常 */
        }
        reject(new Error('request timeout'));
      }, REQUEST_TIMEOUT_MS);
      try {
        request = netModule.request({ url, method: 'GET', redirect: 'follow' });
      } catch (error) {
        clearTimeout(timeout);
        reject(error);
        return;
      }
      request.setHeader('User-Agent', userAgent());
      request.setHeader('Accept', GITHUB_ACCEPT);
      const chunks = [];
      request.on('response', (response) => {
        const status = response.statusCode;
        response.on('data', (chunk) => chunks.push(Buffer.from(chunk)));
        response.on('end', () => {
          if (settled) return;
          settled = true;
          clearTimeout(timeout);
          const body = Buffer.concat(chunks).toString('utf8');
          if (status < 200 || status >= 300) {
            reject(new Error(`HTTP ${status}`));
            return;
          }
          try {
            resolve(JSON.parse(body));
          } catch {
            reject(new Error('invalid JSON'));
          }
        });
        response.on('error', (error) => {
          if (settled) return;
          settled = true;
          clearTimeout(timeout);
          reject(error);
        });
      });
      request.on('error', (error) => {
        if (settled) return;
        settled = true;
        clearTimeout(timeout);
        reject(error);
      });
      try {
        request.end();
      } catch (error) {
        if (settled) return;
        settled = true;
        clearTimeout(timeout);
        reject(error);
      }
    });
  }

  /**
   * 拼装当前状态：当前版本、最新正式版、是否有更新（已扣除已忽略版本）、上次检查时间、autoCheck、html_url 等。
   * @returns {object}
   */
  function status() {
    const settings = db && typeof db.getSettings === 'function' ? db.getSettings('update.') : {};
    const currentVersion = localVersion();
    const latestVersion = settings['update.latestVersion'] || '';
    const latestName = settings['update.latestVersionName'] || '';
    const htmlUrl = settings['update.latestHtmlUrl'] || '';
    const publishedAt = settings['update.latestPublishedAt'] || '';
    const dismissedVersion = settings['update.dismissedVersion'] || '';
    const installerAsset = readStoredInstallerAsset(settings);
    const downloadState = readStoredDownloadState(settings);
    const latestIsStable = settings['update.latestIsStable'];
    const latestRelease = latestVersion
      ? {
          version: latestVersion,
          isPrerelease: settings['update.latestIsPrerelease'] === 'true',
          isDraft: settings['update.latestIsDraft'] === 'true',
          ...(latestIsStable === undefined ? {} : { isStable: latestIsStable === 'true' }),
        }
      : null;
    const stableUpdate = latestRelease ? hasStableUpdate(currentVersion, latestRelease) : false;
    const hasUpdate = stableUpdate && latestVersion !== dismissedVersion;
    return {
      currentVersion,
      latestVersion,
      latestName,
      htmlUrl,
      publishedAt,
      lastCheckedAt: settings['update.lastCheckedAt'] || '',
      dismissedVersion,
      hasUpdate,
      stableUpdate,
      installerAsset,
      installerAssetAvailable: Boolean(installerAsset),
      installerAssetStatus: settings['update.installerAssetStatus'] || '',
      installerAssetReason: settings['update.installerAssetReason'] || '',
      ...downloadState,
      autoCheck: settings['update.autoCheck'] !== 'false',
      intervalMinutes: Number(settings['update.intervalMinutes'] || DEFAULT_INTERVAL_MINUTES) || DEFAULT_INTERVAL_MINUTES,
    };
  }

  /** 忽略指定版本（写 update.dismissedVersion）；缺省取当前最新正式版。本轮不再弹横幅。 */
  function dismiss(input) {
    const version = resolveVersionInput(input) || readSetting('update.latestVersion', '') || '';
    if (version) {
      writeSetting('update.dismissedVersion', version);
      abortActiveDownloadForVersion(version);
      persistDownloadState({
        phase: DOWNLOAD_PHASE.SKIPPED,
        reason: 'dismissed',
        version,
      });
    }
    return status();
  }

  /** 切换自动检查并即时重排调度。 */
  function setAutoCheck(enabled) {
    const value = isFalsyEnabled(enabled) ? 'false' : 'true';
    writeSetting('update.autoCheck', value);
    reschedule();
    return status();
  }

  /** 启动周期调度（autoCheck 关闭时不启动定时器）。 */
  function start() {
    reschedule();
  }

  /** 停止调度，清理定时器。 */
  function stop() {
    clearTimer();
    abortActiveDownload();
  }

  function reschedule() {
    clearTimer();
    if (!autoCheckEnabled()) return;
    const delay = intervalMinutes() * 60 * 1000;
    timer.handle = setTimeout(() => {
      timer.handle = null;
      check().catch(() => {
        /* 检查失败已在结果中结构化，调度不中断 */
      });
      reschedule();
    }, delay);
    // Node 定时器不阻止退出；应用退出时由 stop() 显式清理。
    if (timer.handle && typeof timer.handle.unref === 'function') timer.handle.unref();
  }

  function clearTimer() {
    if (timer.handle) {
      clearTimeout(timer.handle);
      timer.handle = null;
    }
  }

  return { check, status, dismiss, setAutoCheck, start, stop };
}

/* ---------------------------------- 内部纯函数 ---------------------------------- */

function installerDownloadTarget(version, asset, baseDir) {
  const root = path.resolve(baseDir || defaultDownloadDir());
  const versionDir = safePathSegment(`v${stripLeadingV(version)}`) || 'version';
  const fileName = safeFileName(asset?.name || 'installer');
  const dir = path.resolve(root, versionDir);
  const filePath = path.resolve(dir, fileName);
  if (!isInsidePath(root, filePath)) throw new Error('download path escapes update directory');
  return {
    root,
    dir,
    filePath,
    tempPath: path.resolve(dir, `${fileName}.download-${process.pid}-${Date.now()}.tmp`),
  };
}

function defaultDownloadDir() {
  return path.join(process.cwd(), 'updates', 'downloads');
}

function installerDownloadKey(version, asset, filePath) {
  return JSON.stringify([stripLeadingV(version), asset?.downloadUrl || '', asset?.name || '', normalizeAssetSize(asset?.size), filePath]);
}

function safePathSegment(value) {
  return String(value || '')
    .trim()
    .replace(/[<>:"/\\|?*\x00-\x1F]/g, '-')
    .replace(/\.+$/g, '')
    .replace(/^-+$/g, '')
    .slice(0, 120);
}

function safeFileName(value) {
  const base = path.basename(String(value || '').trim());
  return safePathSegment(base) || 'installer';
}

function isInsidePath(root, candidate) {
  const relative = path.relative(path.resolve(root), path.resolve(candidate));
  return relative === '' || (!relative.startsWith('..') && !path.isAbsolute(relative));
}

async function installerFileExists(filePath, asset) {
  try {
    const stat = await fs.promises.stat(filePath);
    if (!stat.isFile()) return false;
    const expectedSize = normalizeAssetSize(asset?.size);
    return expectedSize <= 0 || stat.size === expectedSize;
  } catch {
    return false;
  }
}

function installerFileExistsSync(filePath, asset) {
  try {
    const stat = fs.statSync(filePath);
    if (!stat.isFile()) return false;
    const expectedSize = normalizeAssetSize(asset?.size);
    return expectedSize <= 0 || stat.size === expectedSize;
  } catch {
    return false;
  }
}

async function replaceFile(tempPath, filePath) {
  await removeFileQuietly(filePath);
  await fs.promises.rename(tempPath, filePath);
}

async function removeFileQuietly(filePath) {
  try {
    if (filePath) await fs.promises.unlink(filePath);
  } catch (error) {
    if (error?.code !== 'ENOENT') {
      /* 清理失败由后续写入/重命名错误体现 */
    }
  }
}

function headerNumber(headers, name) {
  if (!headers || typeof headers !== 'object') return 0;
  const value = headers[name] ?? headers[String(name).toLowerCase()] ?? headers[String(name).toUpperCase()];
  const raw = Array.isArray(value) ? value[0] : value;
  const number = Number(raw);
  return Number.isFinite(number) && number > 0 ? Math.trunc(number) : 0;
}

function clampPercent(value) {
  const number = Number(value);
  if (!Number.isFinite(number)) return 0;
  return Math.max(0, Math.min(100, Math.trunc(number)));
}

function resolveInstallerAsset(json, release, runtime) {
  if (!release.isStable) {
    const reason = release.prerelease ? 'prerelease' : release.draft ? 'draft' : 'invalid_version';
    return installerAssetResult(null, INSTALLER_ASSET_STATUS.NOT_APPLICABLE, reason);
  }
  const assets = Array.isArray(json.assets) ? json.assets : [];
  if (assets.length === 0) return installerAssetResult(null, INSTALLER_ASSET_STATUS.UNAVAILABLE, 'no_assets');
  const asset = selectInstallerAsset(assets, runtime);
  if (!asset) return installerAssetResult(null, INSTALLER_ASSET_STATUS.UNAVAILABLE, 'no_platform_match');
  return installerAssetResult(asset, INSTALLER_ASSET_STATUS.AVAILABLE, 'matched');
}

function installerAssetResult(asset, status, reason) {
  return {
    installerAsset: asset,
    installerAssetAvailable: Boolean(asset),
    installerAssetStatus: status,
    installerAssetReason: reason,
  };
}

function selectInstallerAsset(assets, runtime = {}) {
  const target = runtimeTarget(runtime);
  if (!target.platform) return null;
  const candidates = assets
    .map((asset, index) => installerAssetCandidate(asset, target, index))
    .filter(Boolean)
    .sort((a, b) => b.score - a.score || a.index - b.index);
  return candidates.length > 0 ? candidates[0].asset : null;
}

function installerAssetCandidate(asset, target, index) {
  if (!asset || typeof asset !== 'object') return null;
  const name = String(asset.name || '').trim();
  const downloadUrl = String(asset.browser_download_url || asset.download_url || '').trim();
  if (!name || !downloadUrl || isAuxiliaryAssetName(name)) return null;
  const format = matchInstallerFormat(name, target.platform);
  if (!format) return null;
  const detectedArch = detectAssetArch(name);
  if (detectedArch && detectedArch !== 'universal' && target.arch && detectedArch !== target.arch) return null;
  const archScore = detectedArch === target.arch ? 30 : detectedArch === 'universal' ? 20 : 5;
  const platformScore = hasPlatformToken(name, target.platform) ? 10 : 0;
  const normalized = {
    name,
    downloadUrl,
    size: normalizeAssetSize(asset.size),
    platform: target.platform,
    arch: detectedArch || target.arch || '',
    kind: format.kind,
  };
  const contentType = String(asset.content_type || '').trim();
  if (contentType) normalized.contentType = contentType;
  return {
    index,
    score: format.priority + archScore + platformScore,
    asset: normalized,
  };
}

function matchInstallerFormat(name, platform) {
  const lower = name.toLowerCase();
  if (platform === 'win32') {
    if (lower.endsWith('.exe') && hasBoundaryToken(name, 'setup')) return { kind: 'nsis', priority: 300 };
    return null;
  }
  if (platform === 'darwin') {
    if (lower.endsWith('.dmg')) return { kind: 'dmg', priority: 300 };
    if (lower.endsWith('.zip') && hasPlatformToken(name, platform)) return { kind: 'zip', priority: 250 };
    return null;
  }
  if (platform === 'linux') {
    if (lower.endsWith('.appimage')) return { kind: 'appimage', priority: 300 };
    if (lower.endsWith('.deb')) return { kind: 'deb', priority: 250 };
  }
  return null;
}

function isAuxiliaryAssetName(name) {
  return /\.(blockmap|ya?ml|json|txt|sha256|sha512|sig|asc)$/i.test(String(name || ''));
}

function runtimeTarget(runtime = {}) {
  return {
    platform: normalizePlatform(runtime.platform || process.platform),
    arch: normalizeArch(runtime.arch || process.arch),
  };
}

function normalizePlatform(platform) {
  const value = String(platform || '').toLowerCase();
  if (value === 'win32' || value === 'windows' || value === 'win') return 'win32';
  if (value === 'darwin' || value === 'mac' || value === 'macos' || value === 'osx') return 'darwin';
  if (value === 'linux') return 'linux';
  return '';
}

function normalizeArch(arch) {
  const value = String(arch || '').toLowerCase();
  if (value === 'x64' || value === 'x86_64' || value === 'amd64') return 'x64';
  if (value === 'arm64' || value === 'aarch64') return 'arm64';
  if (value === 'ia32' || value === 'x86' || value === 'i386') return 'ia32';
  return value;
}

function detectAssetArch(name) {
  const groups = [
    ['universal', ['universal', 'universal2']],
    ['arm64', ['arm64', 'aarch64']],
    ['x64', ['x64', 'x86_64', 'amd64']],
    ['ia32', ['ia32', 'x86', 'i386']],
  ];
  for (const [arch, aliases] of groups) {
    if (aliases.some((alias) => hasBoundaryToken(name, alias))) return arch;
  }
  return '';
}

function hasPlatformToken(name, platform) {
  const aliases = {
    win32: ['win', 'win32', 'windows'],
    darwin: ['mac', 'macos', 'darwin', 'osx'],
    linux: ['linux'],
  }[platform];
  return Array.isArray(aliases) && aliases.some((alias) => hasBoundaryToken(name, alias));
}

function hasBoundaryToken(name, token) {
  const escaped = escapeRegExp(String(token || '').toLowerCase());
  if (!escaped) return false;
  return new RegExp(`(^|[^a-z0-9])${escaped}([^a-z0-9]|$)`, 'i').test(String(name || ''));
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function normalizeAssetSize(size) {
  const value = Number(size);
  return Number.isFinite(value) && value >= 0 ? Math.trunc(value) : 0;
}

function readStoredInstallerAsset(settings) {
  const name = settings['update.installerAssetName'] || '';
  const downloadUrl = settings['update.installerAssetDownloadUrl'] || '';
  if (!name || !downloadUrl) return null;
  return {
    name,
    downloadUrl,
    size: normalizeAssetSize(settings['update.installerAssetSize']),
    platform: settings['update.installerAssetPlatform'] || '',
    arch: settings['update.installerAssetArch'] || '',
    kind: settings['update.installerAssetKind'] || '',
  };
}

function readStoredDownloadState(settings) {
  const localPath = settings['update.localInstallerPath'] || '';
  return {
    downloadPhase: settings['update.downloadPhase'] || DOWNLOAD_PHASE.IDLE,
    downloadProgress: clampPercent(settings['update.downloadProgress']),
    downloadError: settings['update.downloadError'] || '',
    downloadReason: settings['update.downloadReason'] || '',
    localInstallerPath: localPath,
    downloadedInstallerPath: localPath,
    downloadStartedAt: settings['update.downloadStartedAt'] || '',
    downloadCompletedAt: settings['update.downloadCompletedAt'] || '',
    downloadBytesReceived: normalizeAssetSize(settings['update.downloadBytesReceived']),
    downloadTotalBytes: normalizeAssetSize(settings['update.downloadTotalBytes']),
    downloadAssetKey: settings['update.downloadAssetKey'] || '',
    downloadVersion: settings['update.downloadVersion'] || '',
  };
}

function toParsed(input) {
  if (input === null || input === undefined) return null;
  if (typeof input === 'string') return parseVersion(input);
  if (typeof input === 'object') {
    if (typeof input.version === 'string') return parseVersion(input.version);
    if (Number.isInteger(input.major) && Number.isInteger(input.minor) && Number.isInteger(input.patch)) {
      return {
        major: input.major,
        minor: input.minor,
        patch: input.patch,
        prerelease: Array.isArray(input.prerelease) ? input.prerelease.map(String) : [],
      };
    }
  }
  return null;
}

function comparePrerelease(a, b) {
  const aHas = a.length > 0;
  const bHas = b.length > 0;
  if (!aHas && !bHas) return 0;
  // 无 prerelease（稳定版）优先级高于有 prerelease：`0.2.1-beta.6 < 0.2.1`。
  if (!aHas) return 1;
  if (!bHas) return -1;
  const max = Math.max(a.length, b.length);
  for (let i = 0; i < max; i += 1) {
    const ai = a[i];
    const bi = b[i];
    if (ai === undefined) return -1; // 前置相等时更短的 prerelease 列表优先级更低
    if (bi === undefined) return 1;
    const aNum = NUMERIC_ID_RE.test(ai);
    const bNum = NUMERIC_ID_RE.test(bi);
    if (aNum && !bNum) return -1; // 纯数字标识符优先级低于字母标识符
    if (!aNum && bNum) return 1;
    if (aNum && bNum) {
      const diff = Number(ai) - Number(bi);
      if (diff !== 0) return diff < 0 ? -1 : 1;
    } else if (ai < bi) return -1;
    else if (ai > bi) return 1;
  }
  return 0;
}

function releaseIsStable(release) {
  if (!release) return false;
  if (release.isStable === false) return false;
  if (release.isStable === true) return true;
  if (release.isPrerelease === true || release.isDraft === true) return false;
  return true;
}

function stripLeadingV(value) {
  return String(value || '').trim().replace(/^[vV=]+/, '');
}

function summarizeBody(body) {
  const text = String(body || '').replace(/\s+/g, ' ').trim();
  if (!text) return '';
  const MAX = 280;
  return text.length > MAX ? `${text.slice(0, MAX).trimEnd()}…` : text;
}

function resolveVersionInput(input) {
  if (!input) return '';
  if (typeof input === 'string') return input.trim();
  if (typeof input === 'object' && typeof input.version === 'string') return input.version.trim();
  return '';
}

function isFalsyEnabled(enabled) {
  return enabled === false || enabled === 'false' || enabled === 0 || enabled === '0' || enabled === 'off';
}

function errorMessage(error) {
  if (!error) return 'unknown error';
  return String(error.message || error);
}

function nowIsoSafe() {
  try {
    return new Date().toISOString();
  } catch {
    return '';
  }
}

module.exports = {
  parseVersion,
  compareVersions,
  parseLatestRelease,
  hasStableUpdate,
  createUpdateChecker,
};
