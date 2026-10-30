import { mount } from 'svelte';
import './styles/tokens.css';
import './styles/base.css';
import App from './App.svelte';
import { applyDensity, loadDensity } from './lib/density';

applyDensity(loadDensity()); // before the first paint

const target = document.getElementById('app');
if (!target) throw new Error('missing #app');

export default mount(App, { target });
