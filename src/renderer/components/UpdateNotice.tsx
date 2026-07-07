import { AUTOPLAN_RELEASES_URL, type UpdateStatus } from '../types';
import { useUpdateStatus } from '../hooks/useUpdateStatus';
import { Icon } from './icons';

export function UpdateNotice() {
  const { status, openInstaller, openingInstaller, installerOpenError } = useUpdateStatus();
  if (!status.hasUpdate) return null;

  const versionLabel = status.latestVersion ? `v${status.latestVersion}` : '新版本';
  const releaseUrl = status.htmlUrl || AUTOPLAN_RELEASES_URL;
  const phase = status.downloadPhase || 'idle';
  const progress = clampProgress(status.downloadProgress);
  const canOpenInstaller = phase === 'downloaded' && Boolean(status.localInstallerPath || status.downloadedInstallerPath);

  return (
    <div className={`inline-banner info update-notice update-notice--${phase}`} role="status">
      <div className="update-notice__main">
        <div className="update-notice__title">
          检测到正式版本 <b>{versionLabel}</b>
          {status.latestName ? <span> · {status.latestName}</span> : null}
        </div>
        <div className="update-notice__meta">
          <span>{updateDownloadText(status)}</span>
          {status.installerAsset?.name ? <span className="mono">{status.installerAsset.name}</span> : null}
          {installerOpenError ? <span className="danger-text">{installerOpenError}</span> : null}
        </div>
        {phase === 'downloading' || phase === 'pending' ? (
          <div className="update-notice__progress" aria-label={`下载进度 ${progress}%`}>
            <span style={{ width: `${progress}%` }} />
          </div>
        ) : null}
      </div>
      <div className="update-notice__actions">
        {canOpenInstaller ? (
          <button
            type="button"
            className="btn btn-sm btn-primary"
            disabled={openingInstaller}
            onClick={() => {
              void openInstaller();
            }}
          >
            <Icon name="open" size={14} aria-hidden="true" />
            {openingInstaller ? '打开中' : '打开安装包'}
          </button>
        ) : null}
        <button
          type="button"
          className={`btn btn-sm${canOpenInstaller ? '' : ' btn-primary'}`}
          onClick={() => {
            void window.autoplan.openExternal(releaseUrl);
          }}
        >
          <Icon name="open" size={14} aria-hidden="true" />
          打开 Releases
        </button>
        <button
          type="button"
          className="btn btn-sm"
          onClick={() => {
            void window.autoplan.dismissUpdate();
          }}
        >
          <Icon name="close" size={14} aria-hidden="true" />
          稍后提醒
        </button>
      </div>
    </div>
  );
}

function updateDownloadText(status: UpdateStatus) {
  const phase = status.downloadPhase || 'idle';
  const progress = clampProgress(status.downloadProgress);
  if (phase === 'pending') return '已找到当前平台安装包，等待自动下载。';
  if (phase === 'downloading') return `正在自动下载安装包：${progress}%`;
  if (phase === 'downloaded') return '安装包已下载，可直接打开。';
  if (phase === 'failed') return status.downloadError ? `安装包下载失败：${status.downloadError}` : '安装包下载失败。';
  if (phase === 'unavailable') return '未找到适用于当前平台的安装包，可前往 Releases 手动下载。';
  return status.installerAssetAvailable ? '已找到当前平台安装包。' : '未找到当前平台安装包，可前往 Releases 手动下载。';
}

function clampProgress(value: unknown) {
  const number = Number(value);
  if (!Number.isFinite(number)) return 0;
  return Math.max(0, Math.min(100, Math.trunc(number)));
}
