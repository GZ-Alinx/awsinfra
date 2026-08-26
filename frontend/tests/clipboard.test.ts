import assert from 'node:assert/strict';
import test from 'node:test';
import { copyToClipboard } from '../src/services/clipboard.ts';

function replaceGlobal(name: 'navigator' | 'document' | 'HTMLElement', value: unknown) {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, name);
  Object.defineProperty(globalThis, name, { configurable: true, writable: true, value });
  return () => {
    if (descriptor) Object.defineProperty(globalThis, name, descriptor);
    else delete (globalThis as Record<string, unknown>)[name];
  };
}

test('copyToClipboard uses the modern Clipboard API when available', async () => {
  let copied = '';
  const restoreNavigator = replaceGlobal('navigator', {
    clipboard: { writeText: async (value: string) => { copied = value; } },
  });
  const restoreDocument = replaceGlobal('document', undefined);
  try {
    await copyToClipboard('hello');
    assert.equal(copied, 'hello');
  } finally {
    restoreDocument();
    restoreNavigator();
  }
});

test('copyToClipboard falls back when Clipboard API permission is rejected', async () => {
  class MockHTMLElement { focus() {} }
  let appended = false;
  let removed = false;
  let selected = false;
  let copied = false;
  const textarea = new class extends MockHTMLElement {
    value = '';
    readOnly = false;
    style: Record<string, string> = {};
    setAttribute() {}
    select() { selected = true; }
    setSelectionRange() {}
    remove() { removed = true; }
  }();
  const restoreHTMLElement = replaceGlobal('HTMLElement', MockHTMLElement);
  const restoreNavigator = replaceGlobal('navigator', {
    clipboard: { writeText: async () => { throw new Error('denied'); } },
  });
  const restoreDocument = replaceGlobal('document', {
    activeElement: null,
    body: { appendChild: () => { appended = true; } },
    createElement: () => textarea,
    execCommand: (command: string) => { copied = command === 'copy'; return copied; },
  });
  try {
    await copyToClipboard('fallback');
    assert.equal(textarea.value, 'fallback');
    assert.equal(appended, true);
    assert.equal(selected, true);
    assert.equal(copied, true);
    assert.equal(removed, true);
  } finally {
    restoreDocument();
    restoreNavigator();
    restoreHTMLElement();
  }
});

test('copyToClipboard rejects empty content instead of showing false success', async () => {
  await assert.rejects(copyToClipboard(''), /没有可复制的内容/);
});
