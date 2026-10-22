import { afterEach, describe, expect, it, vi } from 'vitest';

import { TechnicianApiClient } from '../src/api/technician';
import { TECHNICIAN_PROFILE_OP, TechnicianOutboxDispatcher } from '../src/pipeline/dispatcher';
import { profilePayloadFromInput } from '../src/pipeline/profile';
import type { ProfileInput } from '../src/api/technician';
import type { OutboxOperation } from '../src/outbox/types';

const PROFILE: ProfileInput = {
  trade: 'engine',
  specializations: ['engine', 'diagnostics'],
  tools: ['OBD-II scanner'],
  service_area: { area_name: 'Westlands', county: 'Nairobi' },
};

function makeOp(overrides: Partial<OutboxOperation> = {}): OutboxOperation {
  return {
    id: 'op-1',
    type: TECHNICIAN_PROFILE_OP,
    payload: profilePayloadFromInput(PROFILE),
    idempotency_key: 'key-1',
    created_at: '2026-01-01T00:00:00.000Z',
    attempts: 0,
    next_attempt_at: '2026-01-01T00:00:00.000Z',
    state: 'IN_FLIGHT',
    last_error: null,
    seq: 1,
    ...overrides,
  };
}

const json = (status: number, body: unknown, contentType = 'application/json'): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'content-type': contentType } });

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('TechnicianOutboxDispatcher (degraded mode — the Phase 1 default)', () => {
  it('reports backend-pending as a RETRYABLE failure — the op stays queued, never faked', async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);

    const client = new TechnicianApiClient({ baseUrl: null, accessToken: null });
    const result = await new TechnicianOutboxDispatcher(client).dispatch(makeOp());

    expect(result).toEqual({
      kind: 'retryable-failure',
      reason: 'technician backend pending (Phase 2, issue #166) — op stays queued on device',
    });
    expect(fetchMock).not.toHaveBeenCalled(); // degraded mode makes no network calls
  });
});

describe('TechnicianOutboxDispatcher (backend online — Phase 2 behaviour, contract-shaped)', () => {
  it('sends POST /v1/technician/profile with the contract payload and Idempotency-Key, classifies 201 as accepted', async () => {
    const fetchMock = vi.fn(async () =>
      json(201, {
        technician_id: '11111111-1111-4111-8111-111111111111',
        ...PROFILE,
        updated_at: '2026-01-01T00:00:00.000Z',
      }),
    );
    vi.stubGlobal('fetch', fetchMock);

    const client = new TechnicianApiClient({ baseUrl: 'https://api.test', accessToken: 'token-1' });
    const result = await new TechnicianOutboxDispatcher(client).dispatch(makeOp());

    expect(result).toEqual({ kind: 'accepted' });
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('https://api.test/v1/technician/profile');
    expect(init.method).toBe('POST');
    const headers = init.headers as Record<string, string>;
    expect(headers['Idempotency-Key']).toBe('key-1');
    expect(headers.Authorization).toBe('Bearer token-1');
    expect(JSON.parse(init.body as string)).toEqual(PROFILE); // exact contract body
  });

  it('classifies a 4xx problem+json as a permanent rejection carrying the server verdict', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        json(
          422,
          {
            code: 'validation_failed',
            title: 'Validation failed',
            errors: [{ field: 'specializations', issue: 'at least one' }],
          },
          'application/problem+json',
        ),
      ),
    );

    const client = new TechnicianApiClient({ baseUrl: 'https://api.test', accessToken: null });
    const result = await new TechnicianOutboxDispatcher(client).dispatch(makeOp());

    expect(result).toEqual({
      kind: 'rejected',
      reason: 'server rejected the profile (422 validation_failed): Validation failed',
    });
  });

  it('classifies network failure and non-problem 5xx as retryable', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => {
      throw new TypeError('network down');
    }));
    const client = new TechnicianApiClient({ baseUrl: 'https://api.test', accessToken: null });
    const down = await new TechnicianOutboxDispatcher(client).dispatch(makeOp());
    expect(down.kind).toBe('retryable-failure');

    vi.stubGlobal('fetch', vi.fn(async () => json(503, 'nope', 'text/plain')));
    const serverError = await new TechnicianOutboxDispatcher(client).dispatch(makeOp());
    expect(serverError.kind).toBe('retryable-failure');
  });

  it('permanently rejects a queued payload that no longer matches the contract — without a network call', async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);

    const client = new TechnicianApiClient({ baseUrl: 'https://api.test', accessToken: null });
    const result = await new TechnicianOutboxDispatcher(client).dispatch(
      makeOp({ payload: { trade: 'not-a-trade' }, type: TECHNICIAN_PROFILE_OP }),
    );

    expect(result.kind).toBe('rejected');
    expect(String((result as { reason: string }).reason)).toContain('no longer matches the contract');
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('rejects unknown op types (the pipeline outbox only carries technician ops)', async () => {
    const client = new TechnicianApiClient({ baseUrl: null, accessToken: null });
    const result = await new TechnicianOutboxDispatcher(client).dispatch(makeOp({ type: 'job.transition' }));
    expect(result).toEqual({ kind: 'rejected', reason: 'unknown technician pipeline op type: job.transition' });
  });
});
