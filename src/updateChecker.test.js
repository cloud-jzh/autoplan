const { describe, it } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {
  parseVersion,
  compareVersions,
  parseLatestRelease,
  hasStableUpdate,
  createUpdateChecker,
} = require('./updateChecker');

/**
 * 最小 settings 存储替身：与 AppDatabase 同口径（setSetting 一律 String(value) 化），
 * 隔离真实 SQLite，便于覆盖 createUpdateChecker 的落库与状态拼装。
 */
function createSettingsDb(initial = {}) {
  const store = new Map();
  for (const [key, value] of Object.entries(initial)) store.set(key, String(value));
  return {
    getSetting(key, fallback = null) {
      return store.has(key) ? store.get(key) : fallback;
    },
    setSetting(key, value) {
      store.set(key, String(value));
    },
    runBatch(statements) {
      for (const statement of statements || []) {
        const [key, value] = Array.isArray(statement?.params) ? statement.params : [];
        if (key) store.set(key, String(value));
      }
    },
    getSettings(prefix = '') {
      const entries = [...store.entries()]
        .filter(([key]) => (prefix ? key.startsWith(prefix) : true))
        .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
      return Object.fromEntries(entries);
    },
  };
}

function releaseJson(tag, extra = {}) {
  return {
    tag_name: tag,
    name: `Release ${tag}`,
    html_url: `https://github.com/lyming99/autoplan/releases/tag/${tag}`,
    published_at: '2026-07-01T00:00:00Z',
    body: 'release notes',
    prerelease: false,
    draft: false,
    ...extra,
  };
}

function releaseAsset(name, extra = {}) {
  return {
    name,
    browser_download_url: `https://github.com/lyming99/autoplan/releases/download/v0.3.0/${encodeURIComponent(name)}`,
    size: 1024,
    ...extra,
  };
}

function fetchStub(payload) {
  const fn = async () => {
    fn.calls += 1;
    const body = typeof payload === 'function' ? payload() : payload;
    return { ok: true, status: 200, text: async () => JSON.stringify(body) };
  };
  fn.calls = 0;
  return fn;
}

function makeTempDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-update-download-'));
}

function cleanupTempDir(dir) {
  fs.rmSync(dir, { recursive: true, force: true });
}

function expectedInstallerPath(downloadDir, version, fileName) {
  return path.join(downloadDir, `v${version}`, fileName);
}

async function waitForStatus(checker, predicate, message) {
  const deadline = Date.now() + 1500;
  let last = checker.status();
  while (Date.now() < deadline) {
    last = checker.status();
    if (predicate(last)) return last;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  assert.fail(`${message}: ${JSON.stringify(last)}`);
}

describe('parseVersion', () => {
  it('兼容 v 前缀、无前缀、大写 V 与等号前缀', () => {
    assert.deepEqual(parseVersion('v0.2.2'), { major: 0, minor: 2, patch: 2, prerelease: [] });
    assert.deepEqual(parseVersion('0.2.2'), { major: 0, minor: 2, patch: 2, prerelease: [] });
    assert.deepEqual(parseVersion('V1.2.3'), { major: 1, minor: 2, patch: 3, prerelease: [] });
    assert.deepEqual(parseVersion('=1.2.3'), { major: 1, minor: 2, patch: 3, prerelease: [] });
  });

  it('解析 prerelease 标识符数组', () => {
    assert.deepEqual(parseVersion('0.2.1-beta.6'), {
      major: 0,
      minor: 2,
      patch: 1,
      prerelease: ['beta', '6'],
    });
    assert.deepEqual(parseVersion('1.0.0-beta'), { major: 1, minor: 0, patch: 0, prerelease: ['beta'] });
  });

  it('忽略 build 元数据', () => {
    assert.deepEqual(parseVersion('1.0.0-alpha+001'), {
      major: 1,
      minor: 0,
      patch: 0,
      prerelease: ['alpha'],
    });
    assert.deepEqual(parseVersion('1.2.3+build.77'), {
      major: 1,
      minor: 2,
      patch: 3,
      prerelease: [],
    });
  });

  it('脏数据与非版本输入返回 null 而非抛错', () => {
    for (const bad of ['', 'abc', '1.2', '1.2.3.4', 'v', '1.2.3-beta.', 'version-1', null, undefined]) {
      assert.equal(parseVersion(bad), null, `应判定为非法：${JSON.stringify(bad)}`);
    }
  });
});

describe('compareVersions', () => {
  it('升序比较与相等', () => {
    assert.equal(compareVersions('0.2.1', '0.2.2'), -1);
    assert.equal(compareVersions('0.2.2', '0.2.1'), 1);
    assert.equal(compareVersions('0.2.2', '0.2.2'), 0);
  });

  it('主版本/次版本按数值比较而非字典序', () => {
    assert.equal(compareVersions('1.0.0', '0.9.9'), 1);
    assert.equal(compareVersions('0.10.0', '0.9.0'), 1);
    assert.equal(compareVersions('0.9.0', '0.10.0'), -1);
  });

  it('prerelease 低于同号稳定版', () => {
    assert.equal(compareVersions('0.2.1-beta.6', '0.2.1'), -1);
    assert.equal(compareVersions('0.2.1', '0.2.1-beta.6'), 1);
  });

  it('prerelease 段按 semver 语义比较', () => {
    assert.equal(compareVersions('0.2.1-beta.2', '0.2.1-beta.11'), -1, '数字标识符按数值比较');
    assert.equal(compareVersions('1.0.0-1', '1.0.0-alpha'), -1, '数字标识符低于字母标识符');
    assert.equal(compareVersions('1.0.0-alpha', '1.0.0-alpha.1'), -1, '更短的 prerelease 列表优先级更低');
  });

  it('接受已解析对象作为入参', () => {
    assert.equal(compareVersions(parseVersion('1.0.0'), '0.9.0'), 1);
    assert.equal(compareVersions({ version: '1.2.3' }, { major: 1, minor: 2, patch: 2, prerelease: [] }), 1);
  });

  it('任一不可解析时保守返回 0', () => {
    assert.equal(compareVersions('not-a-version', '1.0.0'), 0);
    assert.equal(compareVersions('1.0.0', null), 0);
  });
});

describe('parseLatestRelease', () => {
  it('映射字段并去除 tag 前导 v，稳定版标记为 isStable', () => {
    const r = parseLatestRelease(releaseJson('v0.3.0'));
    assert.equal(r.version, '0.3.0');
    assert.equal(r.name, 'Release v0.3.0');
    assert.equal(r.htmlUrl, 'https://github.com/lyming99/autoplan/releases/tag/v0.3.0');
    assert.equal(r.publishedAt, '2026-07-01T00:00:00Z');
    assert.equal(r.body, 'release notes');
    assert.equal(r.isStable, true);
    assert.equal(r.isPrerelease, false);
    assert.equal(r.isDraft, false);
    assert.equal(r.installerAssetAvailable, false);
    assert.equal(r.installerAssetStatus, 'unavailable');
    assert.equal(r.installerAssetReason, 'no_assets');
  });

  it('从 assets 选择 Windows 当前架构 NSIS Setup .exe 并提取元数据', () => {
    const winX64 = releaseAsset('AutoPlan-0.3.0-win-x64-Setup.exe', {
      size: 123456,
      content_type: 'application/octet-stream',
    });
    const r = parseLatestRelease(
      releaseJson('v0.3.0', {
        assets: [
          releaseAsset('latest.yml'),
          releaseAsset('AutoPlan-0.3.0-win-x64.exe'),
          releaseAsset('AutoPlan-0.3.0-win-arm64-Setup.exe'),
          releaseAsset('AutoPlan-0.3.0-mac-x64.dmg'),
          winX64,
        ],
      }),
      { platform: 'win32', arch: 'x64' }
    );
    assert.equal(r.installerAssetAvailable, true);
    assert.equal(r.installerAssetStatus, 'available');
    assert.equal(r.installerAssetReason, 'matched');
    assert.equal(r.installerAsset.name, 'AutoPlan-0.3.0-win-x64-Setup.exe');
    assert.equal(r.installerAsset.downloadUrl, winX64.browser_download_url);
    assert.equal(r.installerAsset.size, 123456);
    assert.equal(r.installerAsset.platform, 'win32');
    assert.equal(r.installerAsset.arch, 'x64');
    assert.equal(r.installerAsset.kind, 'nsis');
    assert.equal(r.installerAsset.contentType, 'application/octet-stream');
  });

  it('macOS 优先选择 dmg，缺少 dmg 时选择 zip', () => {
    const dmg = releaseAsset('AutoPlan-0.3.0-mac-arm64.dmg');
    const zip = releaseAsset('AutoPlan-0.3.0-mac-arm64.zip');
    const withDmg = parseLatestRelease(releaseJson('v0.3.0', { assets: [zip, dmg] }), {
      platform: 'darwin',
      arch: 'arm64',
    });
    assert.equal(withDmg.installerAsset.name, 'AutoPlan-0.3.0-mac-arm64.dmg');
    assert.equal(withDmg.installerAsset.kind, 'dmg');

    const zipOnly = parseLatestRelease(releaseJson('v0.3.0', { assets: [zip] }), {
      platform: 'darwin',
      arch: 'arm64',
    });
    assert.equal(zipOnly.installerAsset.name, 'AutoPlan-0.3.0-mac-arm64.zip');
    assert.equal(zipOnly.installerAsset.kind, 'zip');
  });

  it('Linux 优先选择 AppImage，缺少 AppImage 时选择 deb', () => {
    const appImage = releaseAsset('AutoPlan-0.3.0-linux-x64.AppImage');
    const deb = releaseAsset('AutoPlan-0.3.0-linux-x64.deb');
    const withAppImage = parseLatestRelease(releaseJson('v0.3.0', { assets: [deb, appImage] }), {
      platform: 'linux',
      arch: 'x64',
    });
    assert.equal(withAppImage.installerAsset.name, 'AutoPlan-0.3.0-linux-x64.AppImage');
    assert.equal(withAppImage.installerAsset.kind, 'appimage');

    const debOnly = parseLatestRelease(releaseJson('v0.3.0', { assets: [deb] }), { platform: 'linux', arch: 'x64' });
    assert.equal(debOnly.installerAsset.name, 'AutoPlan-0.3.0-linux-x64.deb');
    assert.equal(debOnly.installerAsset.kind, 'deb');
  });

  it('没有当前平台匹配资产时明确标记无可用安装包', () => {
    const r = parseLatestRelease(
      releaseJson('v0.3.0', {
        assets: [releaseAsset('AutoPlan-0.3.0-win-x64-Setup.exe')],
      }),
      { platform: 'linux', arch: 'x64' }
    );
    assert.equal(r.installerAsset, null);
    assert.equal(r.installerAssetAvailable, false);
    assert.equal(r.installerAssetStatus, 'unavailable');
    assert.equal(r.installerAssetReason, 'no_platform_match');
  });

  it('prerelease=true 标记为非正式版', () => {
    const r = parseLatestRelease(
      releaseJson('v0.4.0-beta.1', {
        prerelease: true,
        assets: [releaseAsset('AutoPlan-0.4.0-beta.1-win-x64-Setup.exe')],
      }),
      { platform: 'win32', arch: 'x64' }
    );
    assert.equal(r.isPrerelease, true);
    assert.equal(r.isStable, false);
    assert.equal(r.installerAssetAvailable, false);
    assert.equal(r.installerAssetStatus, 'not_applicable');
    assert.equal(r.installerAssetReason, 'prerelease');
  });

  it('draft=true 标记为非正式版', () => {
    const r = parseLatestRelease(
      releaseJson('v0.4.0', { draft: true, assets: [releaseAsset('AutoPlan-0.4.0-win-x64-Setup.exe')] }),
      { platform: 'win32', arch: 'x64' }
    );
    assert.equal(r.isDraft, true);
    assert.equal(r.isStable, false);
    assert.equal(r.installerAssetAvailable, false);
    assert.equal(r.installerAssetStatus, 'not_applicable');
    assert.equal(r.installerAssetReason, 'draft');
  });

  it('非法版本不触发可下载安装包状态', () => {
    const r = parseLatestRelease(
      releaseJson('release-candidate', { assets: [releaseAsset('AutoPlan-release-candidate-win-x64-Setup.exe')] }),
      { platform: 'win32', arch: 'x64' }
    );
    assert.equal(r.isVersionValid, false);
    assert.equal(r.isStable, false);
    assert.equal(r.installerAsset, null);
    assert.equal(r.installerAssetStatus, 'not_applicable');
    assert.equal(r.installerAssetReason, 'invalid_version');
  });

  it('name 缺失时回退到 tag', () => {
    const r = parseLatestRelease(releaseJson('v0.3.0', { name: '' }));
    assert.equal(r.name, 'v0.3.0');
  });

  it('长 body 生成截断摘要', () => {
    const long = 'x'.repeat(500);
    const r = parseLatestRelease(releaseJson('v0.3.0', { body: long }));
    assert.equal(r.body.length, 500);
    assert.ok(r.summary.length <= 281, '摘要应被截断');
    assert.ok(r.summary.endsWith('…'), '摘要末尾应以省略号标识截断');
  });

  it('非对象或缺失 tag_name 返回 null', () => {
    assert.equal(parseLatestRelease(null), null);
    assert.equal(parseLatestRelease(undefined), null);
    assert.equal(parseLatestRelease({}), null);
    assert.equal(parseLatestRelease({ name: 'no tag' }), null);
  });
});

describe('hasStableUpdate', () => {
  it('稳定版严格大于当前版本时返回 true', () => {
    assert.equal(hasStableUpdate('0.2.1', { version: '0.2.2' }), true);
    assert.equal(hasStableUpdate('0.2.2', { version: '0.3.0' }), true);
  });

  it('稳定版等于当前版本时返回 false', () => {
    assert.equal(hasStableUpdate('0.2.2', { version: '0.2.2' }), false);
  });

  it('稳定版低于当前版本时返回 false', () => {
    assert.equal(hasStableUpdate('0.2.2', { version: '0.2.1' }), false);
  });

  it('beta 本地 → 同号稳定版视为有更新', () => {
    assert.equal(hasStableUpdate('0.2.1-beta.6', { version: '0.2.1' }), true);
  });

  it('返回的 Release 为 prerelease/draft 时忽略（不视为更新）', () => {
    assert.equal(hasStableUpdate('0.2.1', { version: '0.9.0', isPrerelease: true }), false);
    assert.equal(hasStableUpdate('0.2.1', { version: '0.9.0', isDraft: true }), false);
    assert.equal(hasStableUpdate('0.2.1', { version: '0.9.0', isStable: false }), false);
  });

  it('当前版本或 Release 版本不可解析时返回 false', () => {
    assert.equal(hasStableUpdate('not-a-version', { version: '0.3.0' }), false);
    assert.equal(hasStableUpdate('0.2.2', { version: 'bad' }), false);
  });
});

describe('createUpdateChecker.check', () => {
  it('成功抓取正式版并落库缓存与 lastCheckedAt', async () => {
    const db = createSettingsDb({ 'update.autoCheck': 'true', 'update.intervalMinutes': '360' });
    const fetch = fetchStub(releaseJson('v0.3.0'));
    const checker = createUpdateChecker({ app: { getVersion: () => '0.2.2' }, db, fetch });
    const result = await checker.check();
    assert.equal(result.ok, true);
    assert.equal(result.release.version, '0.3.0');
    assert.equal(result.hasUpdate, true);
    assert.equal(db.getSetting('update.latestVersion'), '0.3.0');
    assert.equal(db.getSetting('update.latestVersionName'), 'Release v0.3.0');
    assert.equal(db.getSetting('update.latestIsPrerelease'), 'false');
    assert.ok(db.getSetting('update.lastCheckedAt'), '应写入上次检查时间');
  });

  it('成功抓取后把当前平台安装包资产写入状态', async () => {
    const db = createSettingsDb();
    const installer = releaseAsset('AutoPlan-0.3.0-win-x64-Setup.exe', { size: 777 });
    const checker = createUpdateChecker({
      app: { getVersion: () => '0.2.2' },
      db,
      fetch: fetchStub(releaseJson('v0.3.0', { assets: [installer] })),
      runtime: { platform: 'win32', arch: 'x64' },
    });
    const result = await checker.check();
    assert.equal(result.installerAssetAvailable, true);
    assert.equal(result.installerAssetStatus, 'available');
    assert.equal(result.installerAsset.name, 'AutoPlan-0.3.0-win-x64-Setup.exe');
    assert.equal(result.installerAsset.downloadUrl, installer.browser_download_url);
    assert.equal(result.installerAsset.size, 777);
    assert.equal(result.installerAsset.platform, 'win32');
    assert.equal(result.installerAsset.arch, 'x64');
    assert.equal(result.installerAsset.kind, 'nsis');
    assert.equal(db.getSetting('update.installerAssetName'), 'AutoPlan-0.3.0-win-x64-Setup.exe');
    assert.equal(db.getSetting('update.installerAssetDownloadUrl'), installer.browser_download_url);
  });

  it('无匹配安装包时状态明确标记不可用并清空旧资产', async () => {
    const db = createSettingsDb({
      'update.installerAssetName': 'old.exe',
      'update.installerAssetDownloadUrl': 'https://example.test/old.exe',
      'update.installerAssetStatus': 'available',
    });
    const checker = createUpdateChecker({
      app: { getVersion: () => '0.2.2' },
      db,
      fetch: fetchStub(releaseJson('v0.3.0', { assets: [releaseAsset('AutoPlan-0.3.0-mac-x64.dmg')] })),
      runtime: { platform: 'win32', arch: 'x64' },
    });
    const result = await checker.check();
    assert.equal(result.installerAsset, null);
    assert.equal(result.installerAssetAvailable, false);
    assert.equal(result.installerAssetStatus, 'unavailable');
    assert.equal(result.installerAssetReason, 'no_platform_match');
    assert.equal(db.getSetting('update.installerAssetName'), '');
    assert.equal(db.getSetting('update.installerAssetDownloadUrl'), '');
  });

  it('非 2xx 响应返回结构化失败且不抛崩', async () => {
    const db = createSettingsDb();
    const fetch = async () => ({ ok: false, status: 404, text: async () => '' });
    const checker = createUpdateChecker({ app: { getVersion: () => '0.2.2' }, db, fetch });
    const result = await checker.check();
    assert.equal(result.ok, false);
    assert.match(result.error, /404/);
    assert.equal(result.release, null);
  });

  it('JSON 解析失败返回结构化失败', async () => {
    const db = createSettingsDb();
    const fetch = async () => ({ ok: true, status: 200, text: async () => 'not-json' });
    const checker = createUpdateChecker({ app: { getVersion: () => '0.2.2' }, db, fetch });
    const result = await checker.check();
    assert.equal(result.ok, false);
    assert.ok(result.error);
  });

  it('fetch 抛错时返回结构化失败', async () => {
    const db = createSettingsDb();
    const fetch = async () => {
      throw new Error('network down');
    };
    const checker = createUpdateChecker({ app: { getVersion: () => '0.2.2' }, db, fetch });
    const result = await checker.check();
    assert.equal(result.ok, false);
    assert.equal(result.error, 'network down');
  });

  it('并发 check 复用同一 Promise，仅抓取一次', async () => {
    const db = createSettingsDb();
    const inner = fetchStub(releaseJson('v0.3.0'));
    const slow = async (url) => {
      await new Promise((r) => setImmediate(r));
      return inner(url);
    };
    const checker = createUpdateChecker({ app: { getVersion: () => '0.2.2' }, db, fetch: slow });
    const [a, b] = await Promise.all([checker.check(), checker.check()]);
    assert.equal(inner.calls, 1, '并发应仅抓取一次');
    assert.equal(a, b, '并发 check 应返回同一结果对象');
  });
});

describe('createUpdateChecker 安装包自动下载状态机', () => {
  it('检测到可用更新与当前平台安装包后自动下载并落库本地路径', async () => {
    const downloadDir = makeTempDir();
    try {
      const db = createSettingsDb();
      const installer = releaseAsset('AutoPlan-0.3.0-win-x64-Setup.exe', { size: 1024 });
      const progressEvents = [];
      let downloadCalls = 0;
      const downloadFile = async ({ asset, tempPath, onProgress }) => {
        downloadCalls += 1;
        assert.equal(asset.name, installer.name);
        await fs.promises.mkdir(path.dirname(tempPath), { recursive: true });
        onProgress({ bytesReceived: 512, totalBytes: 1024 });
        progressEvents.push(checker.status().downloadProgress);
        await fs.promises.writeFile(tempPath, Buffer.alloc(1024));
        return { bytesReceived: 1024, totalBytes: 1024 };
      };
      const checker = createUpdateChecker({
        app: { getVersion: () => '0.2.2' },
        db,
        fetch: fetchStub(releaseJson('v0.3.0', { assets: [installer] })),
        runtime: { platform: 'win32', arch: 'x64' },
        downloadDir,
        downloadFile,
      });

      const result = await checker.check();
      assert.equal(result.hasUpdate, true);

      const status = await waitForStatus(checker, (next) => next.downloadPhase === 'downloaded', '安装包应下载完成');
      const expectedPath = expectedInstallerPath(downloadDir, '0.3.0', installer.name);
      assert.equal(downloadCalls, 1);
      assert.deepEqual(progressEvents, [50]);
      assert.equal(status.downloadReason, 'completed');
      assert.equal(status.downloadProgress, 100);
      assert.equal(status.downloadBytesReceived, 1024);
      assert.equal(status.downloadTotalBytes, 1024);
      assert.equal(status.downloadVersion, '0.3.0');
      assert.equal(status.localInstallerPath, expectedPath);
      assert.equal(status.downloadedInstallerPath, expectedPath);
      assert.ok(status.downloadAssetKey.includes(installer.browser_download_url));
      assert.equal(db.getSetting('update.localInstallerPath'), expectedPath);
      assert.equal(fs.statSync(expectedPath).size, 1024);
    } finally {
      cleanupTempDir(downloadDir);
    }
  });

  it('已存在同尺寸本地安装包时复用文件且不重复下载', async () => {
    const downloadDir = makeTempDir();
    try {
      const db = createSettingsDb();
      const installer = releaseAsset('AutoPlan-0.3.0-win-x64-Setup.exe', { size: 1024 });
      const expectedPath = expectedInstallerPath(downloadDir, '0.3.0', installer.name);
      fs.mkdirSync(path.dirname(expectedPath), { recursive: true });
      fs.writeFileSync(expectedPath, Buffer.alloc(1024));
      let downloadCalls = 0;
      const checker = createUpdateChecker({
        app: { getVersion: () => '0.2.2' },
        db,
        fetch: fetchStub(releaseJson('v0.3.0', { assets: [installer] })),
        runtime: { platform: 'win32', arch: 'x64' },
        downloadDir,
        downloadFile: async () => {
          downloadCalls += 1;
          throw new Error('should not download existing installer');
        },
      });

      const result = await checker.check();
      assert.equal(result.downloadPhase, 'downloaded');
      assert.equal(result.downloadReason, 'existing_file');
      assert.equal(result.localInstallerPath, expectedPath);
      assert.equal(result.downloadProgress, 100);
      assert.equal(result.downloadBytesReceived, 1024);
      assert.equal(result.downloadTotalBytes, 1024);
      assert.equal(downloadCalls, 0);
    } finally {
      cleanupTempDir(downloadDir);
    }
  });

  it('下载失败时记录 failed 阶段、错误信息与已知字节数', async () => {
    const downloadDir = makeTempDir();
    try {
      const db = createSettingsDb();
      const installer = releaseAsset('AutoPlan-0.3.0-win-x64-Setup.exe', { size: 512 });
      let downloadCalls = 0;
      const checker = createUpdateChecker({
        app: { getVersion: () => '0.2.2' },
        db,
        fetch: fetchStub(releaseJson('v0.3.0', { assets: [installer] })),
        runtime: { platform: 'win32', arch: 'x64' },
        downloadDir,
        downloadFile: async ({ onProgress }) => {
          downloadCalls += 1;
          onProgress({ bytesReceived: 128, totalBytes: 512 });
          throw new Error('disk full');
        },
      });

      await checker.check();
      const status = await waitForStatus(checker, (next) => next.downloadPhase === 'failed', '下载失败应进入 failed');
      assert.equal(downloadCalls, 1);
      assert.equal(status.downloadReason, 'download_failed');
      assert.equal(status.downloadError, 'disk full');
      assert.equal(status.downloadProgress, 25);
      assert.equal(status.downloadBytesReceived, 128);
      assert.equal(status.downloadTotalBytes, 512);
      assert.equal(status.downloadVersion, '0.3.0');
      assert.equal(status.localInstallerPath, '');
    } finally {
      cleanupTempDir(downloadDir);
    }
  });

  it('相同版本与资产的活跃下载只启动一次', async () => {
    const downloadDir = makeTempDir();
    let releaseDownload;
    try {
      const db = createSettingsDb();
      const installer = releaseAsset('AutoPlan-0.3.0-win-x64-Setup.exe', { size: 1024 });
      let downloadCalls = 0;
      const downloadGate = new Promise((resolve) => {
        releaseDownload = resolve;
      });
      const checker = createUpdateChecker({
        app: { getVersion: () => '0.2.2' },
        db,
        fetch: fetchStub(releaseJson('v0.3.0', { assets: [installer] })),
        runtime: { platform: 'win32', arch: 'x64' },
        downloadDir,
        downloadFile: async ({ tempPath }) => {
          downloadCalls += 1;
          await downloadGate;
          await fs.promises.mkdir(path.dirname(tempPath), { recursive: true });
          await fs.promises.writeFile(tempPath, Buffer.alloc(1024));
          return { bytesReceived: 1024, totalBytes: 1024 };
        },
      });

      await checker.check();
      await waitForStatus(checker, (next) => next.downloadPhase === 'downloading', '首个下载应进入 downloading');
      assert.equal(downloadCalls, 1);

      await checker.check();
      assert.equal(downloadCalls, 1, '活跃下载中的相同资产不应重复启动');

      releaseDownload();
      const status = await waitForStatus(checker, (next) => next.downloadPhase === 'downloaded', '去重下载应最终完成');
      assert.equal(downloadCalls, 1);
      assert.equal(status.downloadReason, 'completed');
    } finally {
      if (releaseDownload) releaseDownload();
      cleanupTempDir(downloadDir);
    }
  });

  it('忽略版本、非正式 Release 与无匹配资产时不启动下载', async () => {
    const cases = [
      {
        name: 'dismissed',
        initial: { 'update.dismissedVersion': '0.3.0' },
        release: releaseJson('v0.3.0', { assets: [releaseAsset('AutoPlan-0.3.0-win-x64-Setup.exe')] }),
        expectedPhase: 'skipped',
        expectedReason: 'dismissed',
      },
      {
        name: 'prerelease',
        release: releaseJson('v0.4.0-beta.1', {
          prerelease: true,
          assets: [releaseAsset('AutoPlan-0.4.0-beta.1-win-x64-Setup.exe')],
        }),
        expectedPhase: 'idle',
        expectedReason: 'prerelease',
      },
      {
        name: 'draft',
        release: releaseJson('v0.4.0', {
          draft: true,
          assets: [releaseAsset('AutoPlan-0.4.0-win-x64-Setup.exe')],
        }),
        expectedPhase: 'idle',
        expectedReason: 'draft',
      },
      {
        name: 'no matching asset',
        release: releaseJson('v0.3.0', { assets: [releaseAsset('AutoPlan-0.3.0-mac-x64.dmg')] }),
        expectedPhase: 'unavailable',
        expectedReason: 'no_platform_match',
      },
    ];

    for (const item of cases) {
      const db = createSettingsDb(item.initial);
      let downloadCalls = 0;
      const checker = createUpdateChecker({
        app: { getVersion: () => '0.2.2' },
        db,
        fetch: fetchStub(item.release),
        runtime: { platform: 'win32', arch: 'x64' },
        downloadFile: async () => {
          downloadCalls += 1;
          throw new Error(`should not download for ${item.name}`);
        },
      });

      const result = await checker.check();
      assert.equal(result.downloadPhase, item.expectedPhase, item.name);
      assert.equal(result.downloadReason, item.expectedReason, item.name);
      assert.equal(result.localInstallerPath, '', item.name);
      assert.equal(downloadCalls, 0, item.name);
    }
  });
});

describe('createUpdateChecker.status/dismiss/setAutoCheck', () => {
  it('status 从缓存与设置拼装状态，hasUpdate 扣除已忽略版本', async () => {
    const db = createSettingsDb({ 'update.autoCheck': 'true', 'update.intervalMinutes': '360' });
    const checker = createUpdateChecker({ app: { getVersion: () => '0.2.2' }, db, fetch: fetchStub(releaseJson('v0.3.0')) });
    await checker.check();
    const before = checker.status();
    assert.equal(before.currentVersion, '0.2.2');
    assert.equal(before.latestVersion, '0.3.0');
    assert.equal(before.hasUpdate, true);
    assert.equal(before.stableUpdate, true);

    checker.dismiss('0.3.0');
    const after = checker.status();
    assert.equal(after.stableUpdate, true, '仍存在稳定版更新');
    assert.equal(after.hasUpdate, false, '已忽略版本本轮不再提示');
    assert.equal(after.dismissedVersion, '0.3.0');
  });

  it('dismiss 缺省时忽略当前最新正式版', async () => {
    const db = createSettingsDb();
    const checker = createUpdateChecker({ app: { getVersion: () => '0.2.2' }, db, fetch: fetchStub(releaseJson('v0.3.0')) });
    await checker.check();
    checker.dismiss();
    assert.equal(db.getSetting('update.dismissedVersion'), '0.3.0');
  });

  it('setAutoCheck 切换并落库 autoCheck', () => {
    const db = createSettingsDb({ 'update.autoCheck': 'true' });
    const checker = createUpdateChecker({ app: { getVersion: () => '0.2.2' }, db, fetch: fetchStub(releaseJson('v9.9.9')) });
    checker.setAutoCheck(false);
    assert.equal(db.getSetting('update.autoCheck'), 'false');
    assert.equal(checker.status().autoCheck, false);
    checker.setAutoCheck(true);
    assert.equal(db.getSetting('update.autoCheck'), 'true');
    assert.equal(checker.status().autoCheck, true);
    checker.stop();
  });
});

describe('createUpdateChecker 调度', () => {
  it('autoCheck 开启时按间隔周期触发检查', async () => {
    const db = createSettingsDb({ 'update.autoCheck': 'true', 'update.intervalMinutes': '0.001' });
    const fetch = fetchStub(releaseJson('v9.9.9'));
    const checker = createUpdateChecker({ app: { getVersion: () => '0.0.0' }, db, fetch });
    checker.start();
    await new Promise((r) => setTimeout(r, 220));
    checker.stop();
    assert.ok(fetch.calls >= 1, '周期调度应至少触发一次检查');
  });

  it('autoCheck 关闭时 start 不触发任何检查', async () => {
    const db = createSettingsDb({ 'update.autoCheck': 'false', 'update.intervalMinutes': '0.001' });
    const fetch = fetchStub(releaseJson('v9.9.9'));
    const checker = createUpdateChecker({ app: { getVersion: () => '0.0.0' }, db, fetch });
    checker.start();
    await new Promise((r) => setTimeout(r, 150));
    checker.stop();
    assert.equal(fetch.calls, 0, 'autoCheck 关闭不应挂载定时器');
  });
});
