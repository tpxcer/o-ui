export const pageSegments = { home: 'panel', inbounds: 'inbounds', clients: 'clients', xray: 'xray', settings: 'settings' } as const;
export type PanelPage = keyof typeof pageSegments;

export function inferPanelBase(pathname: string): string {
  const directory = pathname.replace(/[^/]*$/, '') || '/';
  return directory.replace(/(?:panel|inbounds|clients|xray|settings)\/$/, '') || '/';
}

export function panelBase(): string {
  // 正式服务注入启动时的访问路径；开发服务器没有注入时从已知页面推导。
  return globalThis.document?.querySelector?.('base[data-panel-base]')?.getAttribute('href')
    || inferPanelBase(globalThis.location?.pathname ?? '/');
}

export function panelPage(pathname: string, hash: string, base: string): PanelPage {
  const legacy = hash.slice(1);
  if (Object.hasOwn(pageSegments, legacy)) return legacy as PanelPage;
  return (Object.entries(pageSegments).find(([, segment]) => pathname === `${base}${segment}/`)?.[0] ?? 'home') as PanelPage;
}

export function pageURL(page: PanelPage, base: string, offline = false): string {
  return offline ? `#${page}` : `${base}${pageSegments[page]}/`;
}
