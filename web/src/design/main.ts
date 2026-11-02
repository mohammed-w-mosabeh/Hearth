// Development-only entry for the Phase 9 and 10 screens (web/design.html).
// Nothing outside src/design may import this folder (check-forbidden.sh).
import { mount } from 'svelte';
import '../styles/tokens.css';
import '../styles/base.css';
import '../styles/ember.css';
import './design.css';
import Screens from './Screens.svelte';
import { applyDensity, loadDensity } from '../lib/density';
import { applyLang, loadLang } from '../lib/i18n';

applyDensity(loadDensity());
document.documentElement.dataset['ui'] = 'ember';

const target = document.getElementById('app');
if (!target) throw new Error('missing #app');
const start = () => void mount(Screens, { target });
applyLang(loadLang()).then(start, start);
