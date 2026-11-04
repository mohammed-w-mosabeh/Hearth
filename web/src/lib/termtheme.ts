// ANSI colours for the terminal on the light theme (ROADMAP 8.10). Programs
// choose their colours for a dark screen; xterm's defaults (bright green for
// the prompt, yellow, cyan) fall far under AA on Ember's light background.
// These are darkened to 4.5:1 or more on it (termtheme.test.ts). xterm's
// minimumContrastRatio would do this per cell, but writes style attributes,
// which the CSP refuses (SECURITY S4). The dark theme keeps xterm's defaults.

export const lightAnsi = {
  black: '#1f1b16',
  red: '#b3261e',
  green: '#1b7038',
  yellow: '#7a5000',
  blue: '#1f5fa8',
  magenta: '#8a3a86',
  cyan: '#0b6169',
  white: '#635a4e',
  brightBlack: '#4f473d',
  brightRed: '#9c1f18',
  brightGreen: '#17652f',
  brightYellow: '#6e4800',
  brightBlue: '#1a5296',
  brightMagenta: '#7a3077',
  brightCyan: '#09565d',
  brightWhite: '#3d362e',
} as const;
