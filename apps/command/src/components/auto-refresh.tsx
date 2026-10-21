"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";

/*
 * Panel auto-refresh: re-runs the server render on an interval so the job
 * board, escalations and health panels track live state. All fetching stays
 * server-side — this component only triggers a refresh.
 */
export function AutoRefresh({ seconds }: { seconds: number }) {
  const router = useRouter();
  const [paused, setPaused] = useState(false);
  const pausedRef = useRef(false);
  pausedRef.current = paused;

  useEffect(() => {
    const timer = setInterval(() => {
      if (!pausedRef.current) {
        router.refresh();
      }
    }, Math.max(5, seconds) * 1000);
    return () => clearInterval(timer);
  }, [router, seconds]);

  return (
    <p className="text-xs text-slate-500" aria-live="polite">
      {paused ? (
        <>
          Auto-refresh paused ·{" "}
          <button
            type="button"
            onClick={() => setPaused(false)}
            className="font-semibold text-amber-800 underline underline-offset-4"
          >
            resume
          </button>
        </>
      ) : (
        <>
          Auto-refresh every {seconds}s ·{" "}
          <button
            type="button"
            onClick={() => setPaused(true)}
            className="font-semibold text-amber-800 underline underline-offset-4"
          >
            pause
          </button>
        </>
      )}
    </p>
  );
}
