// OUI风格设置页的公共结构：顶部保存/重启栏、可折叠分组、左侧标题说明+右侧控件的设置行、表格。
import { escapeHtml as esc } from './workspace.ts';
import './settings-ui.css';

export function settingsToolbar(buttons: string, note: string): string {
  return `<div class="card settings-toolbar"><div class="settings-toolbar-buttons">${buttons}</div><p class="settings-banner" role="note">${esc(note)}</p></div>`;
}

export function settingSection(title: string, body: string, open = true): string {
  return `<details class="setting-section"${open ? ' open' : ''}><summary>${esc(title)}</summary><div class="setting-list">${body}</div></details>`;
}

export function settingRow(title: string, description: string, control: string): string {
  return `<div class="setting-row"><div class="setting-copy"><strong>${esc(title)}</strong>${description ? `<small>${esc(description)}</small>` : ''}</div><div class="setting-control">${control}</div></div>`;
}

export function toggle(name: string, checked: boolean, label: string, attrs = ''): string {
  return `<label class="switch"><input type="checkbox" name="${esc(name)}" ${checked ? 'checked' : ''} aria-label="${esc(label)}" ${attrs}><span aria-hidden="true"></span></label>`;
}

export function selectControl(name: string, value: string, options: [string, string][], label: string, attrs = ''): string {
  return `<select name="${esc(name)}" aria-label="${esc(label)}" ${attrs}>${options.map(([key, text]) => `<option value="${esc(key)}"${key === value ? ' selected' : ''}>${esc(text)}</option>`).join('')}</select>`;
}

export function textControl(name: string, value: string, label: string, attrs = ''): string {
  return `<input name="${esc(name)}" value="${esc(value)}" aria-label="${esc(label)}" autocomplete="off" ${attrs}>`;
}
