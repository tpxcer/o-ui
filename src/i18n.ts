// 界面只支持简体中文与英文：优先用户保存的选择，其次安装时选择的默认语言，再次浏览器语言；非浏览器环境（测试）默认中文。
export type Lang = 'zh' | 'en';
const storageKey = 'o-ui-lang';

function detect(): Lang {
  try {
    const saved = localStorage.getItem(storageKey);
    if (saved === 'zh' || saved === 'en') return saved;
    const preset = document.querySelector('meta[name="o-ui-default-lang"]')?.getAttribute('content');
    if (preset === 'zh' || preset === 'en') return preset;
    return /^zh\b/i.test(navigator.language) ? 'zh' : 'en';
  } catch { return 'zh'; }
}

export const lang: Lang = detect();
export const locale = lang === 'en' ? 'en-US' : 'zh-CN';

export function t(zh: string, en: string): string { return lang === 'en' ? en : zh; }

// 按钮显示目标语言，侧栏与登录页共用；点击由 main.ts 统一处理。
export function languageButton(): string {
  const [next, label, text] = lang === 'zh' ? ['en', 'Switch to English', 'EN'] : ['zh', '切换到简体中文', '中'];
  return `<button type="button" class="icon-button lang-toggle" data-lang="${next}" lang="${next}" aria-label="${label}" title="${label}">${text}</button>`;
}

// 切换后整页重新加载，避免部分区域残留旧语言。
export function setLang(next: Lang): void {
  try { localStorage.setItem(storageKey, next); } catch { /* 禁用存储时仅本次无法记住。 */ }
  location.reload();
}
