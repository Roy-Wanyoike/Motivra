/**
 * Exponential backoff with bounded delay and jittered schedule.
 *
 * delay(attempts) = min(maxDelayMs, baseDelayMs * factor^(attempts-1)) — i.e.
 * the FIRST retry waits `baseDelayMs`, then each further failure doubles the
 * wait, capped at maxDelayMs. Then symmetric jitter:
 * d' = d * (1 + (rng()*2 - 1) * jitterRatio).
 *
 * The rng is injectable so tests are deterministic; production uses Math.random.
 * Jitter prevents thundering-herd reconnects when a whole crew of technicians
 * regains connectivity at the same site (the classic Nairobi-workshop case).
 */

export interface BackoffPolicy {
  /** Delay before the first retry (attempt 1), ms. */
  baseDelayMs: number;
  /** Multiplier per attempt (2 = exponential doubling). */
  factor: number;
  /** Upper bound for the pre-jitter delay. */
  maxDelayMs: number;
  /** 0..1 — fraction of the delay used as symmetric uniform jitter. */
  jitterRatio: number;
  /** Attempts (after the first) before an op is parked FAILED (dead-letter). */
  maxAttempts: number;
}

export const DEFAULT_BACKOFF_POLICY: BackoffPolicy = {
  baseDelayMs: 1_000,
  factor: 2,
  maxDelayMs: 5 * 60_000,
  jitterRatio: 0.25,
  maxAttempts: 8,
};

export function computeBackoffDelayMs(
  attempts: number,
  policy: BackoffPolicy = DEFAULT_BACKOFF_POLICY,
  rng: () => number = Math.random,
): number {
  // attempts counts completed failures; the first retry (attempts=1) waits base.
  const exponent = Math.max(0, attempts - 1);
  const raw = policy.baseDelayMs * Math.pow(policy.factor, exponent);
  const capped = Math.min(policy.maxDelayMs, raw);
  const jitter = (rng() * 2 - 1) * policy.jitterRatio * capped;
  return Math.max(0, Math.round(capped + jitter));
}
