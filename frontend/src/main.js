import './style.css';
import {Connect, Disconnect, EnableService, GetStatus, SetMode} from '../wailsjs/go/main/App';

const power = document.getElementById('power');
const stateWord = document.getElementById('state-word');
const statIp = document.getElementById('stat-ip');
const statHs = document.getElementById('stat-hs');
const hsDot = document.getElementById('hs-dot');
const statTx = document.getElementById('stat-tx');
const usBadge = document.getElementById('us-badge');
const usEndpoint = document.getElementById('us-endpoint');
const modeFull = document.getElementById('mode-full');
const modeSplit = document.getElementById('mode-split');
const toast = document.getElementById('toast');
const svcBtn = document.getElementById('svc-btn');

let busy = false;
let connected = false;
let toastTimer = null;

function showError(msg) {
  if (!msg) return;
  toast.textContent = msg;
  toast.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => toast.classList.remove('show'), 5000);
}

function fmtBytes(n) {
  if (!n) return '0';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return n.toFixed(n >= 100 || i === 0 ? 0 : 1) + ' ' + units[i];
}

function fmtAge(sec) {
  if (sec < 0) return 'no handshake';
  if (sec < 5) return 'just now';
  if (sec < 120) return sec + 's ago';
  if (sec < 7200) return Math.floor(sec / 60) + 'm ago';
  return Math.floor(sec / 3600) + 'h ago';
}

function render(s) {
  connected = s.connected;
  power.classList.toggle('on', s.connected);
  power.setAttribute('aria-label', s.connected ? 'Disconnect' : 'Connect');
  if (!busy) {
    stateWord.textContent = s.connected ? 'Protected' : 'Tap to connect';
    stateWord.classList.toggle('on', s.connected);
  }
  statIp.textContent = s.publicIP || '—';
  if (s.connected) {
    statHs.textContent = fmtAge(s.handshakeAge);
    hsDot.className = 'dot ' + (s.handshakeAge >= 0 && s.handshakeAge < 180 ? 'fresh' : 'stale');
    statTx.textContent = fmtBytes(s.rxBytes) + ' ↓ ' + fmtBytes(s.txBytes) + ' ↑';
  } else {
    statHs.textContent = 'off';
    hsDot.className = 'dot';
    statTx.textContent = '—';
  }
  usBadge.className = 'server-badge' + (s.connected ? ' on' : '');
  if (s.endpoint) usEndpoint.textContent = s.endpoint.replace(/:.*/, '');
  modeFull.classList.toggle('sel', s.mode === 'full');
  modeSplit.classList.toggle('sel', s.mode === 'split');
  svcBtn.classList.toggle('hidden', s.serviceInstalled);
  if (!s.hasConfig && s.serviceInstalled) showError('No tunnel config found.');
}

async function refresh() {
  try { render(await GetStatus()); } catch (e) { /* backend not ready yet */ }
}

function setBusy(word) {
  busy = true;
  power.classList.add('busy');
  stateWord.textContent = word;
}

function clearBusy() {
  busy = false;
  power.classList.remove('busy');
}

power.addEventListener('click', async () => {
  if (busy) return;
  setBusy(connected ? 'Disconnecting' : 'Connecting');
  try {
    const err = connected ? await Disconnect() : await Connect();
    showError(err);
  } finally {
    clearBusy();
    refresh();
  }
});

async function switchMode(mode) {
  if (busy) return;
  setBusy('Applying');
  try {
    showError(await SetMode(mode));
  } finally {
    clearBusy();
    refresh();
  }
}
modeFull.addEventListener('click', () => switchMode('full'));
modeSplit.addEventListener('click', () => switchMode('split'));

svcBtn.addEventListener('click', async () => {
  svcBtn.disabled = true;
  svcBtn.textContent = 'Waiting for approval…';
  showError(await EnableService());
  // Poll until the service pipe appears (UAC consent + service start).
  let tries = 0;
  const wait = setInterval(async () => {
    const s = await GetStatus();
    if (s.serviceInstalled || ++tries > 30) {
      clearInterval(wait);
      svcBtn.disabled = false;
      svcBtn.textContent = 'Enable background service';
      render(s);
    }
  }, 1000);
});

refresh();
setInterval(refresh, 2000);
