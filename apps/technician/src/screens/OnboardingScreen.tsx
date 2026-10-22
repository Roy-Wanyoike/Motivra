import { useState } from 'react';
import { Pressable, ScrollView, StyleSheet, Text, TextInput, View } from 'react-native';

import { TRADES, TRADE_LABELS, type Trade } from '../api/technician';
import { useNavigation } from '../navigation/NavigationContext';
import { usePipeline } from '../pipeline/TechnicianPipelineContext';

/**
 * Onboarding wizard (issue #126 Phase 1) — the "no garage required" track:
 *
 *   Step 1  Sign-up context  — what joining as a technician means and what
 *                             the identity service does (honest: account
 *                             sign-in is not wired in this foundation build).
 *   Step 2  Skills profile   — trade, specializations, tools owned, coarse
 *                             service area. Validated against the contract
 *                             mirror; nothing leaves the device until valid.
 *   Step 3  Review & submit  — enqueue a `technician.profile.submit` op into
 *                             the pipeline outbox (offline draft persistence)
 *                             and show the queued state + the journey ahead.
 *
 * No demo data anywhere: submission is a real queued op, and the degraded
 * "backend pending" states are stated plainly.
 */

const STEPS = ['Sign-up', 'Skills profile', 'Review & submit'] as const;

export function OnboardingScreen() {
  const { back, canGoBack, navigate } = useNavigation();
  const { draft, setDraft, draftIssues, submitDraft, queuedOp, backendDegraded } = usePipeline();
  const [step, setStep] = useState<0 | 1 | 2>(0);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [stepIssues, setStepIssues] = useState<string[] | null>(null);

  const toggleSpecialization = (trade: Trade) => {
    const has = draft.specializations.includes(trade);
    setDraft({
      specializations: has
        ? draft.specializations.filter((t) => t !== trade)
        : [...draft.specializations, trade],
    });
  };

  const goToReview = () => {
    if (draftIssues.length > 0) {
      setStepIssues(draftIssues.map((i) => `${i.field}: ${i.issue}`));
      return;
    }
    setStepIssues(null);
    setStep(2);
  };

  const onSubmit = () => {
    setSubmitError(null);
    void (async () => {
      const result = await submitDraft();
      if (!result.ok) {
        setSubmitError(result.issues?.map((i) => `${i.field}: ${i.issue}`).join('; ') ?? 'submission failed');
      }
    })();
  };

  const queued = queuedOp;

  return (
    <ScrollView style={styles.container} testID="onboarding-screen">
      {canGoBack ? (
        <Pressable onPress={back}>
          <Text style={styles.link}>← Back</Text>
        </Pressable>
      ) : null}

      <Text style={styles.title}>Join as a technician</Text>
      <Text style={styles.stepCounter}>
        Step {step + 1} of {STEPS.length} · {STEPS[step]}
      </Text>

      {step === 0 ? (
        <View style={styles.section}>
          <Text style={styles.heading}>You do not need a garage to work here.</Text>
          <Text style={styles.body}>
            Motivra sends the work to you. This wizard sets up your technician
            profile: what you can fix, the tools you already own, and the area
            you serve. A senior verifier later assesses your skills; once
            verified, dispatch can assign you jobs.
          </Text>
          <Text style={styles.heading}>What sign-up does</Text>
          <Text style={styles.body}>
            Your account is created in the Motivra identity service (email or
            phone + password, TECHNICIAN role). Passwords are never stored in
            readable form, and verification of your contact channels follows
            the identity service's normal flow.
          </Text>
          <Text style={styles.honestNote}>
            Honest build note: account sign-in is not wired into this
            foundation build yet (identity integration is a tracked follow-up).
            Your skills profile is collected on this device and queued through
            the offline outbox — it will sync when the technician backend
            (Phase 2, issue #166) is connected.
          </Text>
          <Pressable testID="onboarding-continue" style={styles.primaryButton} onPress={() => setStep(1)}>
            <Text style={styles.primaryButtonText}>Continue — skills profile</Text>
          </Pressable>
        </View>
      ) : null}

      {step === 1 ? (
        <View style={styles.section}>
          <Text style={styles.heading}>Primary trade</Text>
          <View style={styles.chipWrap}>
            {TRADES.map((trade) => (
              <Pressable
                key={trade}
                testID={`trade-${trade}`}
                style={[styles.chip, draft.trade === trade && styles.chipSelected]}
                onPress={() => setDraft({ trade })}
              >
                <Text style={[styles.chipText, draft.trade === trade && styles.chipTextSelected]}>
                  {TRADE_LABELS[trade]}
                </Text>
              </Pressable>
            ))}
          </View>

          <Text style={styles.heading}>Specializations (pick at least one)</Text>
          <View style={styles.chipWrap}>
            {TRADES.map((trade) => (
              <Pressable
                key={trade}
                testID={`spec-${trade}`}
                style={[styles.chip, draft.specializations.includes(trade) && styles.chipSelected]}
                onPress={() => toggleSpecialization(trade)}
              >
                <Text style={[styles.chipText, draft.specializations.includes(trade) && styles.chipTextSelected]}>
                  {TRADE_LABELS[trade]}
                </Text>
              </Pressable>
            ))}
          </View>

          <Text style={styles.heading}>Tools you own (advisory — not a gate)</Text>
          <TextInput
            style={styles.input}
            testID="tools-input"
            value={draft.toolsText}
            onChangeText={(text) => setDraft({ toolsText: text })}
            placeholder="e.g. OBD-II scanner, torque wrench set, jack stands"
            placeholderTextColor="#94a3b8"
            multiline
          />

          <Text style={styles.heading}>Service area (coarse — area and county only)</Text>
          <TextInput
            style={styles.input}
            testID="area-name-input"
            value={draft.areaName}
            onChangeText={(text) => setDraft({ areaName: text })}
            placeholder="e.g. Westlands"
            placeholderTextColor="#94a3b8"
          />
          <TextInput
            style={styles.input}
            testID="county-input"
            value={draft.county}
            onChangeText={(text) => setDraft({ county: text })}
            placeholder="County, e.g. Nairobi"
            placeholderTextColor="#94a3b8"
          />
          <Text style={styles.meta}>
            We never store precise location history — dispatch matches on this
            coarse area only (docs/PII-HANDLING.md).
          </Text>

          {stepIssues ? (
            <View style={styles.errorBox} testID="wizard-issues">
              {stepIssues.map((issue) => (
                <Text key={issue} style={styles.errorText}>
                  {issue}
                </Text>
              ))}
            </View>
          ) : null}

          <View style={styles.buttonRow}>
            <Pressable style={styles.secondaryButton} onPress={() => setStep(0)}>
              <Text style={styles.secondaryButtonText}>← Back</Text>
            </Pressable>
            <Pressable testID="onboarding-review" style={styles.primaryButton} onPress={goToReview}>
              <Text style={styles.primaryButtonText}>Review</Text>
            </Pressable>
          </View>
        </View>
      ) : null}

      {step === 2 ? (
        <View style={styles.section}>
          <Text style={styles.heading}>Review your profile</Text>
          {queued === null ? (
            <>
              <Text style={styles.body}>
                Trade: {draft.trade ? TRADE_LABELS[draft.trade] : '—'}
              </Text>
              <Text style={styles.body}>
                Specializations:{' '}
                {draft.specializations.map((t) => TRADE_LABELS[t]).join(', ') || '—'}
              </Text>
              <Text style={styles.body}>
                Tools: {draft.toolsText.trim() || 'none listed (fine — kits grow with the work)'}
              </Text>
              <Text style={styles.body}>
                Service area: {draft.areaName.trim() || '—'}, {draft.county.trim() || '—'}
              </Text>
              <Pressable testID="onboarding-submit" style={styles.primaryButton} onPress={onSubmit}>
                <Text style={styles.primaryButtonText}>Save profile (queued on device)</Text>
              </Pressable>
              {submitError ? (
                <View style={styles.errorBox} testID="wizard-submit-error">
                  <Text style={styles.errorText}>{submitError}</Text>
                </View>
              ) : null}
            </>
          ) : (
            <View style={styles.queuedBox} testID="profile-queued">
              <Text style={styles.queuedTitle}>
                {queued.state === 'DONE'
                  ? 'Profile synced ✓'
                  : queued.state === 'FAILED'
                    ? 'Profile not delivered yet'
                    : 'Profile saved on device — queued for sync'}
              </Text>
              <Text style={styles.body}>
                Op {queued.op_id.slice(0, 8)}… · key {queued.idempotency_key.slice(0, 8)}… ·
                {' '}
                {queued.state === 'DONE'
                  ? 'the technician backend accepted it.'
                  : queued.state === 'FAILED'
                    ? `server verdict: ${queued.last_error ?? 'rejected'} — edit and resubmit; nothing was silently dropped.`
                    : `${queued.attempts} attempt${queued.attempts === 1 ? '' : 's'} — it syncs automatically when the technician backend (Phase 2, issue #166) is reachable.`}
              </Text>
              {backendDegraded ? (
                <Text style={styles.honestNote}>
                  Degraded mode: no technician backend is configured in this
                  build, so the op stays queued on device. Your draft is
                  safe here and will deliver when Phase 2 lands.
                </Text>
              ) : null}
              <Pressable testID="go-profile" style={styles.secondaryButton} onPress={() => navigate({ name: 'Profile' })}>
                <Text style={styles.secondaryButtonText}>View profile & verification status →</Text>
              </Pressable>
            </View>
          )}

          <Text style={styles.heading}>What happens next</Text>
          <View style={styles.journey}>
            {[
              '1. Skills profile saved (this step — queued offline)',
              '2. Skills assessment by a senior verifier (Inspect template)',
              '3. Verified Technician credential (audit-logged, Phase 2)',
              '4. Dispatch eligibility — verified technicians only (Phase 3)',
              '5. Earnings per job, gross → fee → net (Phase 4)',
              '6. M-Pesa payout (Phase 4)',
            ].map((stage) => (
              <Text key={stage} style={styles.journeyRow}>
                {stage}
              </Text>
            ))}
          </View>

          <View style={styles.buttonRow}>
            <Pressable style={styles.secondaryButton} onPress={() => setStep(1)}>
              <Text style={styles.secondaryButtonText}>← Edit profile</Text>
            </Pressable>
          </View>
        </View>
      ) : null}

      <Text style={styles.meta}>
        Draft persistence honesty: drafts live in the pipeline outbox
        (in-memory storage adapter in this build — the SQLite adapter is a
        tracked follow-up), so they survive within a session but not across
        app restarts. Every money amount in this app is integer KES minor
        units — never a float.
      </Text>
    </ScrollView>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, padding: 12 },
  link: { color: '#1f6feb', marginBottom: 8, fontSize: 14 },
  title: { fontSize: 22, fontWeight: '700' },
  stepCounter: { color: '#666', fontSize: 12, marginTop: 2, marginBottom: 10 },
  section: { marginTop: 4 },
  heading: { fontWeight: '700', marginTop: 12, marginBottom: 4, fontSize: 15 },
  body: { fontSize: 14, color: '#334155', lineHeight: 20, marginBottom: 4 },
  meta: { color: '#666', fontSize: 12, marginTop: 14, lineHeight: 17 },
  honestNote: {
    color: '#8a5a00',
    fontSize: 12,
    backgroundColor: '#fdf6e3',
    borderRadius: 8,
    padding: 10,
    marginTop: 8,
    lineHeight: 17,
  },
  chipWrap: { flexDirection: 'row', flexWrap: 'wrap', gap: 6, marginBottom: 8 },
  chip: {
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: '#94a3b8',
    borderRadius: 999,
    paddingHorizontal: 10,
    paddingVertical: 6,
    backgroundColor: '#f8fafc',
  },
  chipSelected: { backgroundColor: '#0a3069', borderColor: '#0a3069' },
  chipText: { fontSize: 12, color: '#0f172a' },
  chipTextSelected: { color: '#fff', fontWeight: '700' },
  input: {
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: '#cbd5e1',
    borderRadius: 8,
    padding: 10,
    marginBottom: 8,
    fontSize: 14,
    backgroundColor: '#fff',
  },
  buttonRow: { flexDirection: 'row', gap: 8, marginTop: 12 },
  primaryButton: { backgroundColor: '#1a7f37', borderRadius: 8, paddingHorizontal: 14, paddingVertical: 10, flex: 1 },
  primaryButtonText: { color: '#fff', fontWeight: '700', fontSize: 13, textAlign: 'center' },
  secondaryButton: {
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: '#0a3069',
    borderRadius: 8,
    paddingHorizontal: 14,
    paddingVertical: 10,
    flex: 1,
  },
  secondaryButtonText: { color: '#0a3069', fontWeight: '600', fontSize: 13, textAlign: 'center' },
  errorBox: { backgroundColor: '#fdecea', borderRadius: 8, padding: 10, marginTop: 10 },
  errorText: { color: '#c62828', fontSize: 13 },
  queuedBox: {
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: '#cbd5e1',
    borderRadius: 8,
    padding: 10,
    backgroundColor: '#f8fafc',
  },
  queuedTitle: { fontWeight: '700', fontSize: 15, marginBottom: 4 },
  journey: { borderWidth: StyleSheet.hairlineWidth, borderColor: '#ddd', borderRadius: 8, padding: 8 },
  journeyRow: { fontSize: 13, paddingVertical: 2, color: '#334155' },
});
