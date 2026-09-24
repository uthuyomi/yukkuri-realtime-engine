import {YukkuriClient} from '../../../sdk/typescript/dist/index.js';
import {BrowserAudioPlayer, BrowserMicrophone, sileroVoiceFactory} from '../../../sdk/typescript/dist/browser.js';
const $ = id => document.getElementById(id);
let session, player, microphone;
const status = message => { $('status').textContent = message; };
async function close() {
  await microphone?.close(); await player?.close(); await session?.close();
  microphone = player = session = undefined;
  $('connect').disabled = false; $('mic').disabled = $('stop').disabled = $('close').disabled = true;
}
$('connect').onclick = async () => {
  $('connect').disabled = true;
  // Resume AudioContext during the actual click, before network awaits.
  const context = new AudioContext(); await context.resume();
  try {
    session = await new YukkuriClient({baseUrl: $('url').value}).realtime.connect();
    player = await BrowserAudioPlayer.create(session, {context});
    session.on('closed', () => { void context.close(); status('Closed'); });
    session.on('error', e => status(`${e.code}: ${e.message}`));
    session.on('transcript', e => { $('transcript').textContent = e.text; });
    session.on('generationStarted', () => { $('response').textContent = ''; });
    session.on('textDelta', e => { $('response').textContent += e.delta; });
    session.on('interruption', e => status(e.type));
    session.on('backchannel', () => status('Backchannel: playback recovered'));
    microphone = new BrowserMicrophone(session, {player, onError: e => status(e.message),
      voiceFactory: sileroVoiceFactory(options => window.vad.MicVAD.new(options), {
        baseAssetPath: 'https://cdn.jsdelivr.net/npm/@ricky0123/vad-web@0.0.31/dist/',
        onnxWASMBasePath: 'https://cdn.jsdelivr.net/npm/onnxruntime-web@1.22.0/dist/'})});
    $('mic').disabled = $('stop').disabled = $('close').disabled = false; status('Connected');
  } catch(e) { status(`${e.code ?? 'browser_error'}: ${e.message}`); await close(); await context.close(); }
};
$('mic').onclick = async () => { try { await microphone.start(); status('Listening'); } catch(e) { status(e.message); } };
$('stop').onclick = async () => { await microphone.stop(); status('Microphone stopped'); };
$('close').onclick = async () => { await close(); status('Closed'); };
$('text').onsubmit = async event => {
  event.preventDefault();
  try { if (!session) throw Error('Connect first'); await microphone.stop(); await session.sendText($('prompt').value, {output: $('output').value}); }
  catch(e) { status(e.message); }
};
