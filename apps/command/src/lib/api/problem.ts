import { NextResponse } from "next/server";

/** RFC 7807-style problem+json response, mirroring the platform convention. */
export function problemResponse(
  status: number,
  code: string,
  title: string,
  detail?: string,
): NextResponse {
  const body: Record<string, unknown> = { code, title };
  if (detail !== undefined) {
    body.detail = detail;
  }
  return NextResponse.json(body, {
    status,
    headers: { "Content-Type": "application/problem+json" },
  });
}
