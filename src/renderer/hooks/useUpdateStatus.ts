import { useCallback, useEffect, useState } from 'react';
import type { UpdateCheckResult, UpdateInstallerOpenResult, UpdateStatus } from '../types';

const EMPTY_STATUS: UpdateStatus = {
  currentVersion: '',
  latestVersion: '',
  latestName: '',
  htmlUrl: '',
  publishedAt: '',
  lastCheckedAt: '',
  dismissedVersion: '',
  hasUpdate: false,
  stableUpdate: false,
  installerAsset: null,
  installerAssetAvailable: false,
  installerAssetStatus: '',
  installerAssetReason: '',
  downloadPhase: 'idle',
  downloadProgress: 0,
  downloadError: '',
  downloadReason: '',
  localInstallerPath: '',
  downloadedInstallerPath: '',
  downloadStartedAt: '',
  downloadCompletedAt: '',
  downloadBytesReceived: 0,
  downloadTotalBytes: 0,
  downloadAssetKey: '',
  downloadVersion: '',
  autoCheck: true,
  intervalMinutes: 360,
};

/**
 * 订阅主进程正式版本更新检查状态（需求 #24）。
 * 挂载时调用 updateStatus() 取初值并订阅 onUpdateStatus 推送，卸载时取消订阅。
 * 返回更新状态、手动检查状态，以及打开已下载安装包的受控入口。
 * 取初值/推送失败时保持默认空状态，绝不抛出到 UI 打扰用户。
 */
export function useUpdateStatus() {
  const [status, setStatus] = useState<UpdateStatus>(EMPTY_STATUS);
  const [checking, setChecking] = useState(false);
  const [openingInstaller, setOpeningInstaller] = useState(false);
  const [installerOpenError, setInstallerOpenError] = useState<string | null>(null);

  useEffect(() => {
    let active = true;
    window.autoplan
      .updateStatus()
      .then((next) => {
        if (active && next) setStatus(next);
      })
      .catch(() => {
        /* 取初值失败保持默认，不打扰用户 */
      });
    const unsubscribe = window.autoplan.onUpdateStatus((next) => {
      if (active && next) setStatus(next);
    });
    return () => {
      active = false;
      unsubscribe();
    };
  }, []);

  const check = useCallback(async () => {
    setChecking(true);
    try {
      const result: UpdateCheckResult = await window.autoplan.checkForUpdates();
      // check() 结果在 UpdateStatus 字段之外附带 ok/error/release，可直接作为最新状态。
      if (result) setStatus(result);
      return result;
    } catch {
      /* 手动检查失败不抛出到 UI，仅恢复 checking 态 */
      return null;
    } finally {
      setChecking(false);
    }
  }, []);

  const openInstaller = useCallback(async (): Promise<UpdateInstallerOpenResult | null> => {
    setOpeningInstaller(true);
    setInstallerOpenError(null);
    try {
      const result = await window.autoplan.openUpdateInstaller();
      if (!result?.ok) setInstallerOpenError(result?.error || '打开安装包失败');
      return result || null;
    } catch {
      setInstallerOpenError('打开安装包失败');
      return null;
    } finally {
      setOpeningInstaller(false);
    }
  }, []);

  return { status, check, checking, openInstaller, openingInstaller, installerOpenError };
}
