import {test} from 'node:test';
import assert from 'node:assert/strict';
import {YukkuriClient, wavToPCM} from '../dist/index.js';
import {readFileSync} from 'node:fs';
import {execFileSync} from 'node:child_process';
const caps={protocol_version:'1',features:{audio_flow_control:{version:'credit-v1',available:true},conversation:{version:'1',available:true},tts:{version:'1',available:true},transcription:{version:'1',available:true}},limits:{}};
class Socket {
  readyState=1; bufferedAmount=0; sent=[]; n=0;
  constructor(version='1', features=caps.features) {queueMicrotask(()=>this.event('session.created',{protocol_version:version,capabilities:{...caps,features}}));}
  event(type,data={},generation_id,related_event_id) {this.onmessage?.({data:JSON.stringify({type,data,generation_id,related_event_id,session_id:'s',event_id:`e${++this.n}`,timestamp:new Date().toISOString()})});}
  send(raw) {if(typeof raw!=='string')return;const e=JSON.parse(raw);this.sent.push(e);
    if(e.type==='session.configure')queueMicrotask(()=>this.event('session.configured',{protocol_version:'1'},undefined,e.event_id));
    if(e.type==='session.close')queueMicrotask(()=>this.event('session.closed',{cleanup_complete:true},undefined,e.event_id));
  }
  close(){this.readyState=3;queueMicrotask(()=>this.onclose?.({code:1000}));}
}
async function setup(version='1', features=caps.features) {let ws;
  const c=new YukkuriClient({fetch:async()=>new Response(JSON.stringify({...caps,features})),webSocketFactory:()=>ws=new Socket(version,features),connectTimeoutMs:100,closeTimeoutMs:100,operationTimeoutMs:100});
  return {c,s:await c.realtime.connect(),ws};
}
test('schema-generated client types are synchronized',()=>{execFileSync(process.execPath,['scripts/wire-types.mjs','--check']);const schema=JSON.parse(readFileSync('../../docs/protocol/server-event.schema.json'));assert.ok(schema.required.includes('event_id'));});
test('version and capability validation',async()=>{
  await assert.rejects(setup('2'),e=>e.code==='unsupported_protocol');
  const {s}=await setup('1',{});try{await assert.rejects(s.sendText('x'),e=>e.code==='unsupported_capability');}finally{await s.close();}
});
test('unknown future event is delivered, not fatal',async()=>{const {s,ws}=await setup();let e;s.on('event',v=>e=v);ws.event('future.optional',{new_field:true});assert.equal(e.type,'future.optional');assert.equal(s.state,'active');await assert.rejects(s.connect(),e=>e.code==='invalid_state');await s.close();await assert.rejects(s.connect(),e=>e.code==='invalid_state');});
test('unexpected binary is a protocol error and explicit disconnect',async()=>{const {s,ws}=await setup();let error,closed;s.on('error',e=>error=e);s.on('closed',e=>closed=e);ws.onmessage({data:new ArrayBuffer(2)});assert.equal(error.code,'protocol_error');assert.equal(s.state,'closed');assert.ok(closed);});
test('missing binary, wrong byte count, missing metadata, and disconnect pairing',async()=>{
  for(const action of ['json','size','disconnect','timeout']){
    const {s,ws}=await setup();let error;s.on('error',e=>error=e);
    ws.event('response.audio.delta',{bytes:2,source_frames:1,channels:1,bits_per_sample:16},'old');
    if(action==='json')ws.event('future.event');if(action==='size')ws.onmessage({data:new ArrayBuffer(4)});
    if(action==='disconnect')ws.close();if(action==='timeout')await new Promise(r=>setTimeout(r,130));else await Promise.resolve();
    assert.equal(error.code,'protocol_error',action);assert.equal(s.state,'closed');
  }
});
test('AbortSignal terminates connecting and unavailable server remains typed',async()=>{
  const controller=new AbortController();controller.abort();let created=false;
  const c=new YukkuriClient({fetch:async()=>new Response(JSON.stringify(caps)),webSocketFactory:()=>{created=true;return new Socket();}});
  await assert.rejects(c.realtime.connect({signal:controller.signal}),e=>e.code==='cancelled');assert.equal(created,false);
});
test('WAV rejects unsupported and truncated input',()=>{assert.throws(()=>wavToPCM(new Uint8Array(20)),e=>e.code==='unsupported_format');});
