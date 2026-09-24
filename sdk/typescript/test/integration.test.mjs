import {before, after, test} from 'node:test';
import assert from 'node:assert/strict';
import {spawn, execFileSync} from 'node:child_process';
import {mkdtempSync, rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {fileURLToPath} from 'node:url';
import net from 'node:net';
import {YukkuriClient, YukkuriError, wavToPCM} from '../dist/index.js';
const root = fileURLToPath(new URL('../../../', import.meta.url));
const temp = mkdtempSync(join(tmpdir(), 'yukkuri-sdk-'));
let process, url;
before(async () => {
  const server = net.createServer(); await new Promise(r => server.listen(0, '127.0.0.1', r));
  const port = server.address().port; await new Promise(r => server.close(r));
  const binary = join(temp, 'fixture.exe'); execFileSync('go', ['build', '-o', binary, './internal/sdktest'], {cwd: root});
  process = spawn(binary, [`127.0.0.1:${port}`], {stdio: 'ignore', windowsHide: true}); url = `http://127.0.0.1:${port}`;
  for (let n=0; n<100; n++) { try { await fetch(url+'/health'); return; } catch { await new Promise(r=>setTimeout(r,25)); } }
  throw Error('fixture did not start');
});
after(async () => { if (process) { const exit = new Promise(r=>process.once('exit',r)); process.kill(); await exit; } rmSync(temp,{recursive:true,force:true}); });
const client = () => new YukkuriClient({baseUrl:url, operationTimeoutMs:3000});
test('health, capabilities refresh, TTS format and complete WAV', async () => {
  const c=client(); assert.equal((await c.health()).status,'ok');
  const first=await c.capabilities(); assert.equal(first.protocol_version,'1'); assert.equal(await c.capabilities(),first);
  assert.notEqual(await c.capabilities({refresh:true}),first);
  const result=await c.speak('hello'); assert.equal(result.format.sample_rate,16000); assert.ok(result.requestId);
  assert.equal(wavToPCM(result.audio).length,3200);
});
test('HTTP error is typed and carries request ID; cancelled TTS', async () => {
  const c=client(); await assert.rejects(c.speak('fail'),e=>e instanceof YukkuriError && e.code==='generation_failed' && !!e.requestId && !e.message.includes('private'));
  await assert.rejects(c.speak('slow',{timeoutMs:30}),e=>e.code==='timeout');
  const controller=new AbortController(); setTimeout(()=>controller.abort(),30);
  await assert.rejects(c.speak('slow',{signal:controller.signal}),e=>e.code==='cancelled');
  const s=await c.realtime.connect();
  try { const pending=new Promise(r=>s.on('error',r)); const related=s.sendEvent({type:'input_text.commit',data:{text:''}});
    const error=await pending;assert.ok(error instanceof YukkuriError);assert.equal(error.relatedEventId,related);assert.ok(error.eventId);assert.equal(error.requestId,s.requestId);
  } finally {await s.close();}
});
test('transcription start, PCM segmentation, commit, final, close', async () => {
  const s=await client().transcription.connect();
  try { await s.start(); await s.sendAudio(new Uint8Array(70000)); const t=await s.commit(); assert.equal(t.text,'fixture transcript'); }
  finally { await s.close(); }
  assert.equal(s.state,'closed'); await s.close();
});
test('one-shot PCM and transcription cancellation', async () => {
  assert.equal((await client().transcribe(new Uint8Array(320))).text,'fixture transcript');
  const s=await client().transcription.connect();
  try { await s.start(); await s.sendAudio(new Uint8Array([127,0])); const pending=s.commit();
    const rejected=assert.rejects(pending,e=>e.code==='cancelled'); await s.cancel(); await rejected;
  } finally {await s.close();}
});
test('realtime text event order, generation tracking, stale cancel, close', async () => {
  const s=await client().realtime.connect(); const order=[], text=[];
  s.on('event',e=>order.push(e.type)); s.on('textDelta',e=>text.push(e.delta));
  try { const g=await s.sendText('hello'); assert.equal(await g.done,'done'); assert.equal(text.join(''),'Hello. SDK fixture.');
    assert.ok(order.indexOf('generation.created')<order.indexOf('response.text.delta')); s.cancelGeneration('stale');
    const next=await s.sendText('again'); assert.notEqual(next.id,g.id); await next.done;
  } finally {await s.close();}
});
test('audio pairing, exact source frames, received is not played, explicit ACK', async () => {
  const s=await client().realtime.connect(); const audio=[]; s.on('audio',a=>audio.push(a));
  try { const g=await s.sendText('hello',{output:'audio'}); await g.done;
    assert.ok(audio.length); assert.equal(audio[0].pcm.length,audio[0].metadata.bytes);
    const snap=s.playbackSnapshot(); assert.ok(snap.received_source_frames>0); assert.equal(snap.played_source_frames,0);
    assert.equal(snap.buffered_source_frames,snap.received_source_frames);
    s.ackPlayed(snap.received_source_frames,g.id); assert.equal(s.playbackSnapshot().buffered_source_frames,0);
    assert.throws(()=>s.ackPlayed(snap.received_source_frames+1,g.id),e=>e.code==='invalid_request');
  } finally {await s.close();}
});
test('manual realtime audio input and cancellation APIs', async () => {
  const s=await client().realtime.connect(); const done=new Promise(r=>s.on('generationDone',r));
  s.on('audio',a=>s.ackPlayed(a.metadata.source_start_frame+a.metadata.source_frames,a.generationId)); // Simulated external renderer in this test only.
  try { s.startAudioInput({mode:'manual'}); await s.sendAudio(new Uint8Array(320)); s.commitInput(); await done;
    s.startAudioInput({mode:'manual'}); s.cancelInput(); s.cancelGeneration();
  } finally {await s.close();}
});
test('AbortSignal after generation acceptance scopes active generation cancellation', async () => {
  const s=await client().realtime.connect(), controller=new AbortController();
  try {const g=await s.sendText('slow-generation',{signal:controller.signal});controller.abort();assert.equal(await g.done,'cancelled');assert.equal(s.state,'active');}
  finally {await s.close();}
});
