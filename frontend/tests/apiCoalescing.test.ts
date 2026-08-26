import assert from 'node:assert/strict';
import test from 'node:test';

import { api, setCSRFToken } from '../src/services/api.ts';

test('identical concurrent GET requests share one network operation without persistent caching', async () => {
  const originalFetch = globalThis.fetch;
  const originalWindow = (globalThis as any).window;
  let calls = 0;
  (globalThis as any).window = {
    setTimeout,
    clearTimeout,
    dispatchEvent() {},
  };
  globalThis.fetch = (async () => {
    calls += 1;
    await new Promise((resolve) => setTimeout(resolve, 15));
    return new Response(JSON.stringify({ items: [{ key: 'alpha' }] }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    });
  }) as typeof fetch;

  try {
    const [first, second] = await Promise.all([
      api<{ items: Array<{ key: string }> }>('/api/performance-test'),
      api<{ items: Array<{ key: string }> }>('/api/performance-test'),
    ]);
    assert.equal(calls, 1);
    assert.deepEqual(first, second);
    assert.notEqual(first, second);

    await api('/api/performance-test');
    assert.equal(calls, 2, 'completed GET responses must never be cached');
  } finally {
    globalThis.fetch = originalFetch;
    (globalThis as any).window = originalWindow;
  }
});

test('GET requests with independent abort signals are not coalesced', async () => {
  const originalFetch = globalThis.fetch;
  const originalWindow = (globalThis as any).window;
  let calls = 0;
  (globalThis as any).window = {
    setTimeout,
    clearTimeout,
    dispatchEvent() {},
  };
  globalThis.fetch = (async () => {
    calls += 1;
    return new Response(JSON.stringify({ ok: true }), { status: 200 });
  }) as typeof fetch;

  try {
    await Promise.all([
      api('/api/signal-test', { signal: new AbortController().signal }),
      api('/api/signal-test', { signal: new AbortController().signal }),
    ]);
    assert.equal(calls, 2);
  } finally {
    globalThis.fetch = originalFetch;
    (globalThis as any).window = originalWindow;
  }
});

test('binary upload preserves the caller content type instead of forcing JSON', async () => {
  const originalFetch = globalThis.fetch;
  const originalWindow = (globalThis as any).window;
  let capturedType = '';
  let capturedBody: BodyInit | null | undefined;
  (globalThis as any).window = {
    setTimeout,
    clearTimeout,
    dispatchEvent() {},
  };
  globalThis.fetch = (async (_input, init) => {
    capturedType = new Headers(init?.headers).get('Content-Type') || '';
    capturedBody = init?.body;
    return new Response(null, { status: 204 });
  }) as typeof fetch;

  try {
    const payload = new Blob(['png-data'], { type: 'image/png' });
    await api<void>('/api/upload-test', {
      method: 'PUT',
      body: payload,
      headers: { 'Content-Type': payload.type },
    });
    assert.equal(capturedType, 'image/png');
    assert.equal(capturedBody, payload);
  } finally {
    globalThis.fetch = originalFetch;
    (globalThis as any).window = originalWindow;
  }
});

test('a stale cross-tab CSRF token refreshes the session and retries one mutation', async () => {
  const originalFetch = globalThis.fetch;
  const originalWindow = (globalThis as any).window;
  const calls: Array<{ path: string; csrf: string }> = [];
  (globalThis as any).window = {
    setTimeout,
    clearTimeout,
    dispatchEvent() {},
  };
  setCSRFToken('stale-token');
  globalThis.fetch = (async (input, init) => {
    const path = String(input);
    const csrf = new Headers(init?.headers).get('X-CSRF-Token') || '';
    calls.push({ path, csrf });
    if (path === '/api/auth/session') {
      return new Response(JSON.stringify({ csrf_token: 'fresh-token' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    }
    if (calls.filter((item) => item.path === '/api/mutation').length === 1) {
      return new Response(JSON.stringify({ error: 'invalid CSRF token' }), {
        status: 403,
        headers: { 'Content-Type': 'application/json' },
      });
    }
    return new Response(JSON.stringify({ ok: true }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    });
  }) as typeof fetch;

  try {
    assert.deepEqual(await api('/api/mutation', { method: 'POST', body: '{}' }), { ok: true });
    assert.deepEqual(calls, [
      { path: '/api/mutation', csrf: 'stale-token' },
      { path: '/api/auth/session', csrf: '' },
      { path: '/api/mutation', csrf: 'fresh-token' },
    ]);
  } finally {
    setCSRFToken('');
    globalThis.fetch = originalFetch;
    (globalThis as any).window = originalWindow;
  }
});
