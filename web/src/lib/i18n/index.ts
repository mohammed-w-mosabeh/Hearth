import en from './en.json';

// English only for now; another locale is another JSON file with the same keys.
export type MessageKey = keyof typeof en;

const messages: Record<MessageKey, string> = en;

/** Returns the message for key with {name} placeholders filled in. */
export function t(key: MessageKey, params: Record<string, string | number> = {}): string {
  const msg = messages[key] ?? key;
  return msg.replace(/\{(\w+)\}/g, (whole, name: string) =>
    name in params ? String(params[name]) : whole,
  );
}
