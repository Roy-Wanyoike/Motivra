import { describe, expect, it } from 'vitest';

import {
  guard,
  guardOrThrow,
  IllegalTransitionError,
  isKnownStatus,
  isTerminal,
  JOB_STATUSES,
  legalTargets,
  statusRank,
  TERMINAL_STATUSES,
  VALID_TRANSITIONS,
} from '../src/domain/job-state';
import type { JobStatus } from '../src/domain/job-state';

const ALL = [...JOB_STATUSES];

/** The legal-edge set, restated independently of VALID_TRANSITIONS from
 *  contracts/jobs/openapi.yaml + backend/jobs/README.md, so the test pins the
 *  contract rather than testing the map against itself. */
const LEGAL_EDGES = new Set<string>([
  'CREATED->TRIAGING',
  'CREATED->CANCELLED',
  'TRIAGING->DISPATCHING',
  'TRIAGING->CANCELLED',
  'DISPATCHING->ASSIGNED',
  'DISPATCHING->ESCALATED',
  'DISPATCHING->CANCELLED',
  'ASSIGNED->ACCEPTED',
  'ASSIGNED->DISPATCHING',
  'ASSIGNED->CANCELLED',
  'ACCEPTED->EN_ROUTE',
  'ACCEPTED->DISPATCHING',
  'ACCEPTED->CANCELLED',
  'EN_ROUTE->ARRIVED',
  'EN_ROUTE->CANCELLED',
  'ARRIVED->INSPECTION',
  'ARRIVED->FAILED',
  'INSPECTION->DIAGNOSIS',
  'DIAGNOSIS->ESTIMATE',
  'ESTIMATE->AWAITING_APPROVAL',
  'AWAITING_APPROVAL->APPROVED',
  'AWAITING_APPROVAL->ESCALATED',
  'AWAITING_APPROVAL->CANCELLED',
  'APPROVED->REPAIRING',
  'REPAIRING->VERIFICATION',
  'REPAIRING->FAILED',
  'VERIFICATION->COMPLETED',
  'VERIFICATION->REPAIRING',
]);

describe('job state guards', () => {
  it('exposes exactly the 18 contract statuses in lifecycle order', () => {
    expect(ALL).toHaveLength(18);
    expect(ALL[0]).toBe('CREATED');
  });

  it('exhaustive 18x18 matrix: guard agrees with the contract edge set', () => {
    for (const current of ALL) {
      for (const next of ALL) {
        const check = guard(next, current);
        const legal = LEGAL_EDGES.has(`${current}->${next}`);
        expect(check.ok, `${current}->${next} expected ${legal ? 'ok' : 'blocked'}`).toBe(legal);
      }
    }
  });

  it('map and contract edge set have identical size (no extra/missing edges)', () => {
    const mapEdges = new Set<string>();
    for (const [from, tos] of Object.entries(VALID_TRANSITIONS)) {
      for (const to of tos) mapEdges.add(`${from}->${to}`);
    }
    expect(mapEdges).toEqual(LEGAL_EDGES);
  });

  it('terminal statuses are immutable', () => {
    for (const t of TERMINAL_STATUSES) {
      expect(VALID_TRANSITIONS[t]).toHaveLength(0);
      for (const next of ALL) {
        expect(guard(next, t).ok).toBe(false);
      }
    }
  });

  it('ESCALATED is a dead end but not terminal', () => {
    expect(VALID_TRANSITIONS.ESCALATED).toHaveLength(0);
    expect(isTerminal('ESCALATED')).toBe(false);
  });

  it('self-transitions are never legal', () => {
    for (const s of ALL) expect(guard(s, s).ok).toBe(false);
  });

  it('guard rules: no repair before approval, no assignment outside dispatching, cancel only pre-physical work', () => {
    expect(guard('REPAIRING', 'DIAGNOSIS').ok).toBe(false);
    expect(guard('REPAIRING', 'ESTIMATE').ok).toBe(false);
    expect(guard('REPAIRING', 'APPROVED').ok).toBe(true);
    expect(guard('ASSIGNED', 'EN_ROUTE').ok).toBe(false);
    expect(guard('ACCEPTED', 'ARRIVED').ok).toBe(false);
    expect(guard('CANCELLED', 'REPAIRING').ok).toBe(false);
    expect(guard('CANCELLED', 'AWAITING_APPROVAL').ok).toBe(true);
  });

  it('legalTargets returns sorted, complete targets', () => {
    expect(legalTargets('DISPATCHING')).toEqual(['ASSIGNED', 'CANCELLED', 'ESCALATED']);
    expect(legalTargets('COMPLETED')).toEqual([]);
  });

  it('isKnownStatus rejects unknown statuses', () => {
    expect(isKnownStatus('REPAIRING')).toBe(true);
    expect(isKnownStatus('repairing')).toBe(false);
    expect(isKnownStatus('DONE')).toBe(false);
  });

  it('guardOrThrow throws IllegalTransitionError with actionable reason', () => {
    expect(() => guardOrThrow('COMPLETED', 'CREATED')).toThrowError(IllegalTransitionError);
    try {
      guardOrThrow('COMPLETED', 'CREATED');
    } catch (err) {
      expect((err as IllegalTransitionError).message).toContain('CREATED -> COMPLETED');
    }
  });

  it('every non-terminal status can reach COMPLETED or a documented dead end', () => {
    // Walk guarantee: from CREATED the mainline reaches COMPLETED.
    const path = ['TRIAGING', 'DISPATCHING', 'ASSIGNED', 'ACCEPTED', 'EN_ROUTE', 'ARRIVED', 'INSPECTION', 'DIAGNOSIS', 'ESTIMATE', 'AWAITING_APPROVAL', 'APPROVED', 'REPAIRING', 'VERIFICATION', 'COMPLETED'] as const;
    let cur: JobStatus = 'CREATED';
    for (const next of path) {
      expect(guard(next, cur).ok, `${cur}->${next}`).toBe(true);
      cur = next;
    }
    expect(isTerminal(cur)).toBe(true);
    expect(statusRank(cur)).toBeGreaterThan(statusRank('CREATED'));
  });
});
