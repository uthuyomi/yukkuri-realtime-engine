import {YukkuriClient} from '../../../sdk/typescript/dist/index.js';
import {BrowserAudioPlayer, BrowserMicrophone, sileroVoiceFactory} from '../../../sdk/typescript/dist/browser.js';
import {TurnHistory, formatLatency as fmt} from './history.mjs';
const $ = id => document.getElementById(id);
const history = new TurnHistory();
const views = new Map();
let session, player, microphone, context, closing = false, sending = false, scheduled = false;
const labels = {pending: '処理中', completed: '完了', interrupted: '割り込み', cancelled: '中止', failed: '失敗'};
const metrics = {eotToSTT: 'EOT → STT final', sttToLLM: 'STT final → LLM first delta', llmToTTS: 'LLM first delta → TTS開始', ttsToAudio: 'TTS開始 → 音声準備', serverTTFA: 'Server TTFA（通知間隔）', clientTTFA: 'Client TTFA（PCM受信）', audibleTTFA: 'Audible TTFA（可聴開始）', interruptionLatency: '割り込み確定', total: 'total（生成終了まで）'};
function element(tag, text, className) { const node = document.createElement(tag); if (text != null) node.textContent = text; if (className) node.className = className; return node; }
function controls(active) {
  $('connect').disabled = active || closing; $('url').disabled = active || closing;
  for (const id of ['mic', 'stop', 'close']) $(id).disabled = !active || closing;
  $('send').disabled = !active || sending || closing;
}
const status = message => { $('status').textContent = message; };
const notice = error => { $('notice').textContent = error ? `${error.code ? `${error.code}: ` : ''}${error.message ?? error}` : ''; };
function render() {
  scheduled = false;
  const container = $('conversation'), atBottom = container.scrollHeight - container.scrollTop - container.clientHeight < 60;
  if (history.turns.length) container.querySelector('.empty')?.remove();
  for (const t of history.turns) {
    let v = views.get(t);
    if (!v) {
      const article = element('article', null, 'turn'), heading = element('div', null, 'turn-heading');
      const badge = element('span', null, 'badge'); heading.append(element('span', `Turn ${t.number}`), badge);
      const user = element('p', null, 'message'), assistant = element('p', null, 'message'), compact = element('p', null, 'compact');
      const details = element('details'), list = element('dl'); details.append(element('summary', '計測の詳細'), list);
      article.append(heading, element('p', 'あなた', 'role'), user, element('p', '応答', 'role response-label'), assistant, compact, details);
      container.append(article); v = {badge, user, assistant, compact, list}; views.set(t, v);
    }
    v.badge.textContent = labels[t.status]; v.badge.dataset.state = t.status;
    v.user.textContent = t.userText || '入力情報を待っています'; v.assistant.textContent = t.assistantText || (t.status === 'pending' ? '応答を待っています…' : '応答テキストなし');
    const l = t.latency;
    v.compact.textContent = `Server TTFA ${fmt(l.serverTTFA)} ｜ STT ${fmt(l.eotToSTT)} ｜ LLM ${fmt(l.sttToLLM)} ｜ TTS ${fmt(l.ttsToAudio)}`;
    v.list.replaceChildren();
    for (const [label, value] of [['turn_id', t.turnId ?? '未取得'], ['generation_id', t.generationId], ...Object.entries(metrics).map(([field, label]) => [label, fmt(l[field])])]) v.list.append(element('dt', label), element('dd', value));
  }
  if (atBottom) container.scrollTop = container.scrollHeight;
  $('turn-count').textContent = `${history.turns.length} turns`;
  $('aggregate').replaceChildren();
  for (const [label, value] of Object.entries(history.aggregate())) { const group = element('div'); group.append(element('dt', label), element('dd', label === 'samples' ? String(value) : fmt(value))); $('aggregate').append(group); }
  $('history').replaceChildren();
  for (const t of [...history.turns].reverse()) {
    const row = element('tr');
    for (const value of [`#${t.number}`, fmt(t.latency.serverTTFA), fmt(t.latency.eotToSTT), fmt(t.latency.sttToLLM), fmt(t.latency.ttsToAudio)]) row.append(element('td', value));
    const cell = element('td'), badge = element('span', labels[t.status], 'badge'); badge.dataset.state = t.status; cell.append(badge); row.append(cell); $('history').append(row);
  }
  if (!history.turns.length) { const row = element('tr'), cell = element('td', '計測履歴はまだありません'); cell.colSpan = 6; row.append(cell); $('history').append(row); }
}
function scheduleRender() { if (!scheduled) { scheduled = true; requestAnimationFrame(render); } }
async function close() {
  if (closing) return;
  closing = true; controls(false);
  const oldSession = session, oldMic = microphone, oldPlayer = player, oldContext = context;
  session = microphone = player = context = undefined;
  try {
    // Release every resource even if one cleanup fails.
    for (const release of [() => oldMic?.close(), () => oldPlayer?.close(), () => oldSession?.close(), () => oldContext?.state !== 'closed' && oldContext?.close()]) {
      try { await release(); } catch (error) { notice(error); }
    }
    if (oldSession) history.close(oldSession.id);
  } finally { closing = false; controls(false); status('未接続'); scheduleRender(); }
}
$('connect').onclick = async () => {
  $('connect').disabled = true; notice(''); status('接続中');
  try {
    context = new AudioContext(); await context.resume();
    const current = session = await new YukkuriClient({baseUrl: $('url').value}).realtime.connect();
    current.on('event', e => { history.accept(e); scheduleRender(); });
    current.on('audio', e => { history.audio(current.id, e.generationId); scheduleRender(); });
    current.on('error', e => { notice(e); });
    current.on('closed', e => {
      history.close(current.id, e.code !== 1000); scheduleRender();
      if (session === current) void close();
    });
    player = await BrowserAudioPlayer.create(current, {context});
    microphone = new BrowserMicrophone(current, {player, onError: notice,
      voiceFactory: sileroVoiceFactory(options => window.vad.MicVAD.new(options), {
        baseAssetPath: 'https://cdn.jsdelivr.net/npm/@ricky0123/vad-web@0.0.31/dist/',
        onnxWASMBasePath: 'https://cdn.jsdelivr.net/npm/onnxruntime-web@1.22.0/dist/'})});
    controls(true); status('接続済み');
  } catch (error) { notice(error); await close(); }
};
$('mic').onclick = async () => { try { await microphone.start(); status('音声入力中'); } catch (error) { notice(error); } };
$('stop').onclick = async () => { try { await microphone.stop(); status('接続済み'); } catch (error) { notice(error); } };
$('close').onclick = () => void close();
$('text').onsubmit = async event => {
  event.preventDefault();
  if (!session || sending) return;
  const current = session, text = $('prompt').value.trim(); if (!text) return;
  sending = true; controls(true); notice('');
  try {
    await microphone.stop(); status('接続済み');
    const generation = await current.sendText(text, {output: $('output').value});
    history.bindText(current.id, generation.id, text); scheduleRender();
  } catch (error) { notice(error); }
  finally { sending = false; controls(session?.state === 'active'); }
};
render();
