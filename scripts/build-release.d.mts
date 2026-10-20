export const releaseArchitectures: string[];
export function releaseVersion(args?: string[]): string;
export function packBundle(parent: string, output: string): void;
export function writeReleaseSums(directory: string, names: string[]): Promise<void>;
