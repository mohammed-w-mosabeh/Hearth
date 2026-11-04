import { mount } from 'svelte';
import './styles/tokens.css';
import './styles/base.css';
import App from './App.svelte';
import { applyDensity, loadDensity } from './lib/density';
import { applyTheme, followSystemTheme, loadTheme } from './lib/theme';
import { applyLang, loadLang } from './lib/i18n';

applyDensity(loadDensity()); // before the first paint
applyTheme(loadTheme());
followSystemTheme();

const target = document.getElementById('app');
if (!target) throw new Error('missing #app');

const start = () => void mount(App, { target });
// If the language cannot load, the page still comes up, in English.
applyLang(loadLang()).then(start, start);
