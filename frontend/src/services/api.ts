let csrfToken = '';
let sessionCheck: Promise<'valid' | 'invalid' | 'unknown'> | null = null;
let expirationNotified = false;
let requestSequence = 0;
const inFlightGETRequests = new Map<string, Promise<unknown>>();

export interface APIRequestInit extends RequestInit {
  /** Override the platform default timeout. Set to 0 only for a deliberate long-running request. */
  timeoutMs?: number;
  /** Mutations are shown in the global operation indicator unless explicitly disabled. */
  activity?: boolean;
}

export function setCSRFToken(value: string) {
  csrfToken = value;
  if (value) expirationNotified = false;
}

export class APIError extends Error {
  public status: number;

  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

async function confirmSession(): Promise<'valid' | 'invalid' | 'unknown'> {
  if (sessionCheck) return sessionCheck;
  sessionCheck = (async () => {
    const controller = new AbortController();
    const timer = window.setTimeout(() => controller.abort(), 10_000);
    try {
      const response = await fetch('/api/auth/session', {
        method: 'GET',
        headers: { Accept: 'application/json' },
        credentials: 'same-origin',
        cache: 'no-store',
        signal: controller.signal,
      });
      if (response.status === 401) return 'invalid';
      if (!response.ok) return 'unknown';
      const session = await response.json() as { csrf_token?: string };
      if (session.csrf_token) csrfToken = session.csrf_token;
      return 'valid';
    } catch {
      // A network interruption is not proof that the login session expired.
      return 'unknown';
    } finally {
      window.clearTimeout(timer);
    }
  })();
  try {
    return await sessionCheck;
  } finally {
    sessionCheck = null;
  }
}

async function responseErrorMessage(response: Response) {
  let message = `请求失败（HTTP ${response.status}）`;
  try { message = (await response.json()).error || message; } catch { /* empty or non-JSON body */ }
  return message;
}

function canReplayBody(body: BodyInit | null | undefined) {
  return typeof ReadableStream === 'undefined' || !(body instanceof ReadableStream);
}

function emitRequestActivity(type: 'start' | 'end', detail: Record<string, unknown>) {
  if (typeof window !== 'undefined') window.dispatchEvent(new CustomEvent(`ops:request-${type}`, { detail }));
}

function defaultTimeout(path: string, method: string) {
  if (path === '/api/auth/session') return 12_000;
  return method === 'GET' ? 30_000 : 90_000;
}

function cloneAPIResult<T>(value: T): T {
  if (value === undefined || value === null || typeof value !== 'object') return value;
  if (typeof structuredClone === 'function') return structuredClone(value);
  return JSON.parse(JSON.stringify(value)) as T;
}

export function api<T>(path: string, options: APIRequestInit = {}): Promise<T> {
  const method = (options.method || 'GET').toUpperCase();
  // Several panels can mount together and request the same read model. Share
  // only the in-flight network operation (never cache a completed response),
  // so freshness and mutation semantics stay unchanged.
  const coalescible = method === 'GET' && !options.signal && !options.body && !options.headers;
  if (!coalescible) return executeAPI<T>(path, options);

  const key = `${path}\x00${options.timeoutMs ?? defaultTimeout(path, method)}`;
  const existing = inFlightGETRequests.get(key) as Promise<T> | undefined;
  if (existing) return existing.then(cloneAPIResult);

  const request = executeAPI<T>(path, options);
  inFlightGETRequests.set(key, request);
  const clear = () => {
    if (inFlightGETRequests.get(key) === request) inFlightGETRequests.delete(key);
  };
  void request.then(clear, clear);
  return request;
}

async function executeAPI<T>(path: string, options: APIRequestInit = {}): Promise<T> {
  const { timeoutMs: timeoutOverride, activity = true, signal: externalSignal, ...requestOptions } = options;
  const headers = new Headers(requestOptions.headers);
  headers.set('Accept', 'application/json');
  if (requestOptions.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json');
  const method = (requestOptions.method || 'GET').toUpperCase();
  if (['POST', 'PUT', 'PATCH', 'DELETE'].includes(method) && csrfToken && path !== '/api/auth/login') {
    headers.set('X-CSRF-Token', csrfToken);
  }

  const controller = new AbortController();
  const timeoutMs = timeoutOverride ?? defaultTimeout(path, method);
  let timedOut = false;
  const abortFromCaller = () => controller.abort();
  if (externalSignal?.aborted) controller.abort();
  else externalSignal?.addEventListener('abort', abortFromCaller, { once: true });
  const timer = timeoutMs > 0 ? window.setTimeout(() => { timedOut = true; controller.abort(); }, timeoutMs) : 0;
  const tracked = activity && ['POST', 'PUT', 'PATCH', 'DELETE'].includes(method);
  const requestID = ++requestSequence;
  if (tracked) emitRequestActivity('start', { id: requestID, method, path });

  try {
    const send = () => fetch(path, { ...requestOptions, headers, credentials: 'same-origin', signal: controller.signal });
    let response = await send();
    let message = '';
    if (!response.ok) message = await responseErrorMessage(response);

    // A login in another tab replaces the HttpOnly session cookie while this
    // tab still holds the previous in-memory CSRF token. Refresh the current
    // session and retry the same mutation exactly once; never weaken server-side
    // CSRF validation and never replay a streaming request body.
    if (response.status === 403 && message === 'invalid CSRF token' &&
      ['POST', 'PUT', 'PATCH', 'DELETE'].includes(method) &&
      path !== '/api/auth/login' && path !== '/api/auth/session' && canReplayBody(requestOptions.body)) {
      const state = await confirmSession();
      if (state === 'valid' && csrfToken) {
        headers.set('X-CSRF-Token', csrfToken);
        response = await send();
        message = response.ok ? '' : await responseErrorMessage(response);
      }
    }
    if (!response.ok) {
      if (response.status === 401 && path !== '/api/auth/login' && path !== '/api/auth/session') {
        // Some protected operations legitimately use 401 for a failed password
        // recheck. Only redirect to login after the session endpoint confirms the
        // browser session itself is no longer valid.
        const state = await confirmSession();
        if (state === 'invalid' && !expirationNotified) {
          expirationNotified = true;
          window.dispatchEvent(new Event('ops:session-expired'));
        }
      }
      throw new APIError(message, response.status);
    }
    if (response.status === 204) return undefined as T;
    return response.json() as Promise<T>;
  } catch (error) {
    if (timedOut) throw new APIError(`请求处理超时（${Math.round(timeoutMs / 1000)} 秒），请检查网络或上游服务状态后重试`, 408);
    throw error;
  } finally {
    if (timer) window.clearTimeout(timer);
    externalSignal?.removeEventListener('abort', abortFromCaller);
    if (tracked) emitRequestActivity('end', { id: requestID, method, path });
  }
}
