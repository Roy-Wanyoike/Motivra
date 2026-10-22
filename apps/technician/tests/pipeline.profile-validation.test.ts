import { describe, expect, it } from 'vitest';

import { TRADES } from '../src/api/technician';
import {
  EMPTY_PROFILE_DRAFT,
  formatKes,
  parseProfilePayload,
  parseToolsText,
  profileInputFromDraft,
  profilePayloadFromInput,
  validateProfileDraft,
  type ProfileDraft,
} from '../src/pipeline/profile';

function validDraft(overrides: Partial<ProfileDraft> = {}): ProfileDraft {
  return {
    ...EMPTY_PROFILE_DRAFT,
    trade: 'engine',
    specializations: ['engine', 'diagnostics'],
    toolsText: 'OBD-II scanner, torque wrench set',
    areaName: 'Westlands',
    county: 'Nairobi',
    ...overrides,
  };
}

describe('profile draft validation (mirrors contracts/technician ProfileInput)', () => {
  it('accepts a complete draft with no issues', () => {
    expect(validateProfileDraft(validDraft())).toEqual([]);
  });

  it('requires a trade, at least one specialization and a service area', () => {
    const issues = validateProfileDraft(EMPTY_PROFILE_DRAFT);
    const fields = issues.map((i) => i.field);
    expect(fields).toContain('trade');
    expect(fields).toContain('specializations');
    expect(fields).toContain('service_area.area_name');
    expect(fields).toContain('service_area.county');
  });

  it('caps specializations at 6 and rejects duplicates', () => {
    const six = validateProfileDraft(validDraft({ specializations: ['engine', 'electrical', 'tyres', 'diagnostics', 'brake_systems', 'body_work'] }));
    expect(six).toEqual([]);

    const seven = validateProfileDraft(
      validDraft({ specializations: ['engine', 'electrical', 'tyres', 'diagnostics', 'brake_systems', 'body_work', 'transmission'] }),
    );
    expect(seven.map((i) => i.field)).toEqual(['specializations']);

    const dupes = validateProfileDraft(validDraft({ specializations: ['engine', 'engine'] }));
    expect(dupes.map((i) => i.issue)).toEqual(['must be unique']);
  });

  it('caps tools at 40 entries of at most 80 characters', () => {
    const forty = validateProfileDraft(validDraft({ toolsText: Array.from({ length: 40 }, (_, i) => `tool ${i}`).join(', ') }));
    expect(forty).toEqual([]);

    const fortyOne = validateProfileDraft(validDraft({ toolsText: Array.from({ length: 41 }, (_, i) => `tool ${i}`).join(', ') }));
    expect(fortyOne.map((i) => i.field)).toEqual(['tools']);

    const tooLong = validateProfileDraft(validDraft({ toolsText: 'x'.repeat(81) }));
    expect(tooLong.map((i) => i.issue)).toEqual(['each entry at most 80 characters']);
  });

  it('enforces the coarse service-area length limits', () => {
    const longArea = validateProfileDraft(validDraft({ areaName: 'a'.repeat(121) }));
    expect(longArea.map((i) => i.field)).toEqual(['service_area.area_name']);

    const longCounty = validateProfileDraft(validDraft({ county: 'c'.repeat(81) }));
    expect(longCounty.map((i) => i.field)).toEqual(['service_area.county']);
  });

  it('parses tools text trimmed, deduped, empty-free', () => {
    expect(parseToolsText('  OBD-II scanner , , torque wrench set ,OBD-II scanner  ')).toEqual([
      'OBD-II scanner',
      'torque wrench set',
    ]);
  });

  it('builds a contract-shaped ProfileInput with trimmed fields', () => {
    const input = profileInputFromDraft(validDraft({ areaName: '  Westlands ', county: ' Nairobi ' }));
    expect(input).toEqual({
      trade: 'engine',
      specializations: ['engine', 'diagnostics'],
      tools: ['OBD-II scanner', 'torque wrench set'],
      service_area: { area_name: 'Westlands', county: 'Nairobi' },
    });
    expect(TRADES).toContain(input.trade);
  });
});

describe('outbox payload round-trip (JsonValue safe)', () => {
  it('payload round-trips through parseProfilePayload unchanged', () => {
    const input = profileInputFromDraft(validDraft());
    const payload = profilePayloadFromInput(input);
    const parsed = parseProfilePayload(payload);
    expect(parsed).toEqual({ ok: true, input });
  });

  it('rejects payloads that do not match the contract', () => {
    expect(parseProfilePayload(null).ok).toBe(false);
    expect(parseProfilePayload({ trade: 'welding', specializations: ['engine'] }).ok).toBe(false);
    expect(parseProfilePayload({ trade: 'engine', specializations: [] }).ok).toBe(false);
    expect(
      parseProfilePayload({ trade: 'engine', specializations: ['engine'], tools: [], service_area: { area_name: '', county: 'Nairobi' } }).ok,
    ).toBe(false);
    expect(parseProfilePayload({ trade: 'engine', specializations: ['engine'], tools: 'OBD-II scanner' }).ok).toBe(false);
  });
});

describe('money formatting (integer minor units, repo principle 4)', () => {
  it('formats KES cents without floats', () => {
    expect(formatKes(0)).toBe('KES 0.00');
    expect(formatKes(123456)).toBe('KES 1,234.56');
    expect(formatKes(5)).toBe('KES 0.05');
    expect(formatKes(123456789)).toBe('KES 1,234,567.89');
    expect(formatKes(-250)).toBe('-KES 2.50');
  });
});
