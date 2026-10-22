import {
  TRADES,
  type ProfileInput,
  type Trade,
} from '../api/technician';
import type { JsonObject, JsonValue } from '../lib/json';

/**
 * Skills-profile draft + validation — the pure core of the onboarding
 * wizard. Field constraints mirror `contracts/technician/openapi.yaml`
 * (ProfileInput) exactly; the contract is the source of truth and this
 * mirror is pinned by tests (tests/pipeline.profile-validation.test.ts).
 */

export interface ProfileDraft {
  trade: Trade | null;
  specializations: Trade[];
  /** Raw comma-separated text; parsed into tools[] on submit. */
  toolsText: string;
  areaName: string;
  county: string;
}

export const EMPTY_PROFILE_DRAFT: ProfileDraft = {
  trade: null,
  specializations: [],
  toolsText: '',
  areaName: '',
  county: '',
};

export type ValidationIssue = { field: string; issue: string };

function isTrade(value: string): value is Trade {
  return (TRADES as readonly string[]).includes(value);
}

/** Parse the comma-separated tools text (trimmed, empties dropped, deduped). */
export function parseToolsText(toolsText: string): string[] {
  const seen = new Set<string>();
  for (const raw of toolsText.split(',')) {
    const tool = raw.trim();
    if (tool.length === 0) continue;
    seen.add(tool);
  }
  return [...seen];
}

/**
 * Validation mirror of the contract's ProfileInput constraints:
 *  - trade: required, one of the Trade enum values;
 *  - specializations: 1..6, Trade enum values, unique;
 *  - tools: ≤40 entries, each 1..80 chars (advisory, never a gate);
 *  - service_area.area_name / county: required, ≤120 / ≤80 chars.
 */
export function validateProfileDraft(draft: ProfileDraft): ValidationIssue[] {
  const issues: ValidationIssue[] = [];

  if (draft.trade === null) {
    issues.push({ field: 'trade', issue: 'required' });
  }

  if (draft.specializations.length === 0) {
    issues.push({ field: 'specializations', issue: 'required (at least one)' });
  } else if (draft.specializations.length > 6) {
    issues.push({ field: 'specializations', issue: 'at most 6' });
  } else {
    const unique = new Set(draft.specializations);
    if (unique.size !== draft.specializations.length) {
      issues.push({ field: 'specializations', issue: 'must be unique' });
    }
  }

  const tools = parseToolsText(draft.toolsText);
  if (tools.length > 40) {
    issues.push({ field: 'tools', issue: 'at most 40 entries' });
  }
  for (const tool of tools) {
    if (tool.length > 80) {
      issues.push({ field: 'tools', issue: 'each entry at most 80 characters' });
      break;
    }
  }

  const areaName = draft.areaName.trim();
  if (areaName.length === 0) {
    issues.push({ field: 'service_area.area_name', issue: 'required' });
  } else if (areaName.length > 120) {
    issues.push({ field: 'service_area.area_name', issue: 'at most 120 characters' });
  }

  const county = draft.county.trim();
  if (county.length === 0) {
    issues.push({ field: 'service_area.county', issue: 'required' });
  } else if (county.length > 80) {
    issues.push({ field: 'service_area.county', issue: 'at most 80 characters' });
  }

  return issues;
}

/** Build the contract-shaped ProfileInput from a valid draft. */
export function profileInputFromDraft(draft: ProfileDraft): ProfileInput {
  if (draft.trade === null) throw new Error('profile draft has no trade');
  return {
    trade: draft.trade,
    specializations: [...draft.specializations],
    tools: parseToolsText(draft.toolsText),
    service_area: {
      area_name: draft.areaName.trim(),
      county: draft.county.trim(),
    },
  };
}

/**
 * The outbox payload for a `technician.profile.submit` op — the exact body
 * POST /v1/technician/profile will receive, as JSON values (the outbox
 * stores JsonValue payloads so a SQLite mirror can persist them verbatim).
 */
export function profilePayloadFromInput(input: ProfileInput): JsonObject {
  return {
    trade: input.trade,
    specializations: [...input.specializations],
    tools: [...input.tools],
    service_area: { area_name: input.service_area.area_name, county: input.service_area.county },
  };
}

/**
 * Defensive parse of a queued payload back into ProfileInput (the dispatcher
 * runs before the network; a payload that no longer matches the contract is
 * a permanent rejection, never a retry).
 */
export function parseProfilePayload(payload: JsonValue): { ok: true; input: ProfileInput } | { ok: false; reason: string } {
  if (typeof payload !== 'object' || payload === null || Array.isArray(payload)) {
    return { ok: false, reason: 'payload is not an object' };
  }
  const trade = payload['trade'];
  if (typeof trade !== 'string' || !isTrade(trade)) {
    return { ok: false, reason: 'trade is missing or not a contract Trade value' };
  }
  const specializationsRaw = payload['specializations'];
  if (!Array.isArray(specializationsRaw) || specializationsRaw.length < 1 || specializationsRaw.length > 6) {
    return { ok: false, reason: 'specializations must be an array of 1..6 trades' };
  }
  const specializations: Trade[] = [];
  for (const entry of specializationsRaw) {
    if (typeof entry !== 'string' || !isTrade(entry)) {
      return { ok: false, reason: 'specializations contain a non-contract trade value' };
    }
    specializations.push(entry);
  }
  const toolsRaw = payload['tools'];
  if (!Array.isArray(toolsRaw) || toolsRaw.some((t) => typeof t !== 'string')) {
    return { ok: false, reason: 'tools must be an array of strings' };
  }
  const area = payload['service_area'];
  if (typeof area !== 'object' || area === null || Array.isArray(area)) {
    return { ok: false, reason: 'service_area is missing' };
  }
  const areaName = area['area_name'];
  const county = area['county'];
  if (typeof areaName !== 'string' || areaName.length === 0 || typeof county !== 'string' || county.length === 0) {
    return { ok: false, reason: 'service_area requires area_name and county' };
  }
  return {
    ok: true,
    input: {
      trade,
      specializations,
      tools: toolsRaw as string[],
      service_area: { area_name: areaName, county },
    },
  };
}

/** Integer minor units (KES cents) → display string. Money never floats (repo principle 4). */
export function formatKes(minor: number): string {
  const sign = minor < 0 ? '-' : '';
  const abs = Math.abs(Math.trunc(minor));
  const shillings = Math.floor(abs / 100);
  const cents = String(abs % 100).padStart(2, '0');
  const grouped = shillings.toString().replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  return `${sign}KES ${grouped}.${cents}`;
}
