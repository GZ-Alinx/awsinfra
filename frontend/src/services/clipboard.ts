/**
 * Copy text with a compatibility fallback for browsers where the modern
 * Clipboard API is unavailable or rejected by the current permissions policy.
 *
 * This function must be called directly from a user interaction such as a
 * click. Browsers intentionally block clipboard writes outside that context.
 */
export async function copyToClipboard(value: unknown): Promise<void> {
  const text = String(value ?? '');
  if (!text) throw new Error('没有可复制的内容');

  if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {
      // Continue with the DOM fallback below. This covers denied Clipboard API
      // permissions, embedded browsers and deployments without a secure context.
    }
  }

  if (typeof document === 'undefined' || !document.body) {
    throw new Error('当前环境不支持复制到剪贴板');
  }

  const activeElement = document.activeElement instanceof HTMLElement
    ? document.activeElement
    : null;
  const textarea = document.createElement('textarea');
  textarea.value = text;
  textarea.readOnly = true;
  textarea.setAttribute('aria-hidden', 'true');
  textarea.style.position = 'fixed';
  textarea.style.inset = '0 auto auto -9999px';
  textarea.style.opacity = '0';
  textarea.style.pointerEvents = 'none';

  document.body.appendChild(textarea);
  try {
    textarea.focus();
    textarea.select();
    textarea.setSelectionRange(0, textarea.value.length);
    if (!document.execCommand('copy')) {
      throw new Error('浏览器拒绝了复制操作');
    }
  } finally {
    textarea.remove();
    activeElement?.focus();
  }
}
