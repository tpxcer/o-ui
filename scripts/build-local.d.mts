export function buildTarget(args?: string[], platform?: string, architecture?: string): { os: string; arch: string };
export function stageLocalFiles(source: string, destination: string): Promise<void>;
export function writeLocalManifest(destination: string, target: Record<string, string>, release?: { version: string }): Promise<void>;
export function stageReleaseFiles(source: string, destination: string, version: string): Promise<void>;
export function assembleBundle(output: string, os: string, arch: string, release?: { version: string }): Promise<void>;
export function stageThirdPartyNotices(source: string, destination: string, buildInfo: { GoVersion: string; Deps: { Path: string; Version: string; Replace?: unknown }[] }, goRoot: string, modules: Record<string, { Dir: string; Version: string; Replace?: unknown }>): Promise<void>;
