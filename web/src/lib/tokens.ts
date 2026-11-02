// Tokens page: types of GET /tokens, which never carries a token itself.

export interface TokenInfo {
  name: string;
  file: string;
  created: number;
}

export interface TokensView {
  tokens: TokenInfo[];
  metrics: boolean;
  port: number;
}

/** The server's rule for a token's name (auth.ValidTokenName). */
export function validName(name: string): boolean {
  return /^[a-z0-9][a-z0-9-]{0,31}$/.test(name);
}

/** A Prometheus scrape job for a token, which it reads from its file. */
export function scrapeConfig(port: number, file: string): string {
  return [
    'scrape_configs:',
    '  - job_name: hearth',
    '    authorization:',
    `      credentials_file: ${file}`,
    '    static_configs:',
    `      - targets: ['127.0.0.1:${port}']`,
  ].join('\n');
}
