import { createContext, type ReactNode, useContext } from 'react';
import {
  getHttpChatOperations,
  getTerminalConnectionOperations,
  type AutoplanClient,
  type HttpChatOperations,
  type TerminalConnectionOperations,
} from './client';
import { getAutoplanClient } from './transport';
import type { DesktopBridge } from '../desktop/bridge';
import { getDefaultDesktopBridge } from '../desktop/ipcBridge';

const defaultClient = getAutoplanClient();
const defaultDesktopBridge: DesktopBridge = getDefaultDesktopBridge();
const AutoplanClientContext = createContext<AutoplanClient | null>(null);
const DesktopBridgeContext = createContext<DesktopBridge | null>(null);

export interface AutoplanProviderProps {
  children: ReactNode;
  /** Explicit injection keeps the component tree transport-neutral. */
  client?: AutoplanClient;
  /** Explicit injection is reserved for desktop bridge contract tests. */
  desktopBridge?: DesktopBridge;
}

/** Injects stable business and native-desktop dependencies at the renderer root. */
export function AutoplanProvider({
  children,
  client = defaultClient,
  desktopBridge = defaultDesktopBridge,
}: AutoplanProviderProps) {
  return (
    <AutoplanClientContext.Provider value={client}>
      <DesktopBridgeContext.Provider value={desktopBridge}>
        {children}
      </DesktopBridgeContext.Provider>
    </AutoplanClientContext.Provider>
  );
}

export function useAutoplanClient(): AutoplanClient {
  const client = useContext(AutoplanClientContext);
  if (!client) throw new Error('useAutoplanClient must be used within AutoplanProvider');
  return client;
}

/** Returns P13A Chat HTTP operations only when its independent gate is open. */
export function useHttpChatOperations(): HttpChatOperations | null {
  return getHttpChatOperations(useAutoplanClient());
}

/** P14 data-plane access stays behind the injected business client. */
export function useTerminalConnectionOperations(): TerminalConnectionOperations | null {
  return getTerminalConnectionOperations(useAutoplanClient());
}

export function useDesktopBridge(): DesktopBridge {
  const desktopBridge = useContext(DesktopBridgeContext);
  if (!desktopBridge) throw new Error('useDesktopBridge must be used within AutoplanProvider');
  return desktopBridge;
}
