import { describe, expect, it } from 'vitest';

import { TechnicianApiClient } from '../src/api/technician';
import { TECHNICIAN_PROFILE_OP, TechnicianOutboxDispatcher } from '../src/pipeline/dispatcher';
import { profilePayloadFromInput } from '../src/pipeline/profile';
import { Outbox } from '../src/outbox/outbox';
import { InMemoryOutboxStorage } from '../src/outbox/storage';
import type { ProfileInput } from '../src/api/technician';

/**
 * End-to-end outbox round-trip for the technician skills profile:
 * a draft submitted offline must survive as a queued op, dedupe per
 * idempotency key, retry on the backoff schedule while the backend is
 * pending (Phase 1) and complete once a contract-conformant backend
 * answers (Phase 2). Proves the onboarding wizard's offline-draft
 * persistence claim against the REAL outbox machinery.
 */

const PROFILE: ProfileInput = {
  trade: 'general_repair',
  specializations: ['general_repair'],
  tools: ['jack stands'],
  service_area: { area_name: 'Kilimani', county: 'Nairobi' },
};

function harness(client: TechnicianApiClient, nowMs = 1_000_000) {
  const clock = () => nowMs;
  const rng = () => 0.5; // jitterRatio 0 in the test policy → rng irrelevant
  const storage = new InMemoryOutboxStorage();
  const outbox = new Outbox({
    storage,
    dispatcher: new TechnicianOutboxDispatcher(client),
    clock,
    rng,
    backoff: { baseDelayMs: 1_000, factor: 2, maxDelayMs: 30_000, jitterRatio: 0, maxAttempts: 4 },
  });
  return { storage, outbox, clock };
}

const json = (status: number, body: unknown, contentType = 'application/json'): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'content-type': contentType } });

describe('profile submission through the pipeline outbox', () => {
  it('keeps the op queued (PENDING + backoff) while the backend is pending — never DONE, never faked', async () => {
    const { outbox, storage } = harness(new TechnicianApiClient({ baseUrl: null, accessToken: null }));

    const { deduped } = await outbox.enqueue({
      type: TECHNICIAN_PROFILE_OP,
      payload: profilePayloadFromInput(PROFILE),
      idempotencyKey: 'key-profile-1',
    });
    expect(deduped).toBe(false);

    const report = await outbox.drain();
    expect(report.processed).toBe(1);
    expect(report.failedRetryable).toBe(1); // backend-pending → retryable
    expect(report.succeeded).toBe(0);

    const op = await storage.getByIdempotencyKey('key-profile-1');
    expect(op?.state).toBe('PENDING'); // stays queued — the durable on-device draft
    expect(op?.attempts).toBe(1);
    expect(op?.last_error).toContain('Phase 2');
  });

  it('dedupes a second submission of the same logical op (double-tap safe)', async () => {
    const { outbox, storage } = harness(new TechnicianApiClient({ baseUrl: null, accessToken: null }));

    const first = await outbox.enqueue({
      type: TECHNICIAN_PROFILE_OP,
      payload: profilePayloadFromInput(PROFILE),
      idempotencyKey: 'key-profile-2',
    });
    const second = await outbox.enqueue({
      type: TECHNICIAN_PROFILE_OP,
      payload: profilePayloadFromInput(PROFILE),
      idempotencyKey: 'key-profile-2',
    });

    expect(second.deduped).toBe(true);
    expect(second.operation.id).toBe(first.operation.id);

    const counts = await storage.countByState();
    expect(counts.PENDING).toBe(1);
  });

  it('completes (DONE) once a contract-conformant backend accepts the profile', async () => {
    const fetchMock = async () =>
      json(201, {
        technician_id: '22222222-2222-4222-8222-222222222222',
        ...PROFILE,
        updated_at: '2026-01-01T00:00:00.000Z',
      });
    const { outbox, storage } = harness(
      new TechnicianApiClient({ baseUrl: 'https://api.test', accessToken: 'token-1' }),
    );
    const originalFetch = globalThis.fetch;
    globalThis.fetch = fetchMock as typeof fetch;
    try {
      await outbox.enqueue({
        type: TECHNICIAN_PROFILE_OP,
        payload: profilePayloadFromInput(PROFILE),
        idempotencyKey: 'key-profile-3',
      });
      const report = await outbox.drain();
      expect(report.succeeded).toBe(1);

      const op = await storage.getByIdempotencyKey('key-profile-3');
      expect(op?.state).toBe('DONE');
      expect(op?.last_error).toBeNull();
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it('parks a server 422 as FAILED and allows a corrected resubmission under a fresh key', async () => {
    const fetchMock = async () =>
      json(422, { code: 'validation_failed', title: 'Validation failed' }, 'application/problem+json');
    const originalFetch = globalThis.fetch;
    globalThis.fetch = fetchMock as typeof fetch;
    const { outbox, storage } = harness(
      new TechnicianApiClient({ baseUrl: 'https://api.test', accessToken: null }),
    );
    try {
      await outbox.enqueue({
        type: TECHNICIAN_PROFILE_OP,
        payload: profilePayloadFromInput(PROFILE),
        idempotencyKey: 'key-profile-4',
      });
      await outbox.drain();
      const failed = await storage.getByIdempotencyKey('key-profile-4');
      expect(failed?.state).toBe('FAILED'); // permanent; surfaced in the UI

      // Corrected profile → fresh key → a NEW op (never a silent overwrite).
      const corrected = await outbox.enqueue({
        type: TECHNICIAN_PROFILE_OP,
        payload: profilePayloadFromInput({ ...PROFILE, tools: ['jack stands', 'OBD-II scanner'] }),
        idempotencyKey: 'key-profile-4',
      });
      expect(corrected.deduped).toBe(false);
      expect(corrected.operation.id).not.toBe(failed?.id);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });
});
