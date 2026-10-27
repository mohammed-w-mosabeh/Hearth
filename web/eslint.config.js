import js from '@eslint/js';
import ts from 'typescript-eslint';
import svelte from 'eslint-plugin-svelte';
import svelteConfig from './svelte.config.js';

export default ts.config(
  { ignores: ['dist/', 'node_modules/'] },
  js.configs.recommended,
  ...ts.configs.strict,
  ...svelte.configs.recommended,
  {
    files: ['**/*.svelte', '**/*.svelte.ts'],
    languageOptions: {
      parserOptions: { parser: ts.parser, extraFileExtensions: ['.svelte'], svelteConfig },
    },
  },
  {
    languageOptions: {
      globals: {
        window: 'readonly', document: 'readonly', location: 'readonly', history: 'readonly',
        localStorage: 'readonly', fetch: 'readonly', WebSocket: 'readonly', URL: 'readonly',
        setTimeout: 'readonly', clearTimeout: 'readonly', console: 'readonly',
        Storage: 'readonly', Location: 'readonly', History: 'readonly',
        URLSearchParams: 'readonly', ResizeObserver: 'readonly', matchMedia: 'readonly', getComputedStyle: 'readonly',
        HTMLDivElement: 'readonly', HTMLElement: 'readonly', HTMLDialogElement: 'readonly', HTMLInputElement: 'readonly', MouseEvent: 'readonly', KeyboardEvent: 'readonly',
        innerHeight: 'readonly', innerWidth: 'readonly', setInterval: 'readonly', clearInterval: 'readonly', navigator: 'readonly', TextEncoder: 'readonly', SubmitEvent: 'readonly', Event: 'readonly',
      },
    },
    rules: {
      // SECURITY S6: render all data as text.
      'svelte/no-at-html-tags': 'error',
      'no-restricted-properties': [
        'error',
        { property: 'innerHTML', message: 'Render data as text (SECURITY S6).' },
        { property: 'outerHTML', message: 'Render data as text (SECURITY S6).' },
        { property: 'insertAdjacentHTML', message: 'Render data as text (SECURITY S6).' },
      ],
      'no-restricted-globals': ['error', { name: 'eval', message: 'No eval (CSP).' }],
      'no-new-func': 'error',
    },
  },
);
