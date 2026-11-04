// Error text for the UI: every error says what failed and what to do next
// (ROADMAP 8.9). The server's message says what failed; the next step comes
// from the HTTP status, or from the request getting no answer at all.

import { ApiError } from './api';
import { t, type MessageKey } from './i18n';

/** The next step for an HTTP status; 0 means Hearth did not answer. */
export function nextStep(status: number): MessageKey {
  switch (status) {
    case 0:
      return 'next.offline';
    case 400:
    case 413:
    case 422:
      return 'next.input';
    case 403:
      return 'next.forbidden';
    case 404:
      return 'next.gone';
    case 409:
      return 'next.conflict';
    case 429:
      return 'next.wait';
    case 502:
    case 503:
    case 504:
      return 'next.service';
    default:
      return 'next.log';
  }
}

const sentence = (s: string): string => (/[.!?…]$/.test(s.trim()) ? s.trim() : `${s.trim()}.`);

/** What failed, then what to do next. `what` is used when the server gave
 *  no message: fetch rejects with a TypeError when Hearth does not answer. */
export function failure(err: unknown, what: MessageKey): string {
  if (err instanceof ApiError) return `${sentence(err.message)} ${t(nextStep(err.status))}`;
  return `${t(what)} ${t(err instanceof TypeError ? 'next.offline' : 'next.log')}`;
}
