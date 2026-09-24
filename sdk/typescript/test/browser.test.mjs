import {test} from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {readFileSync} from 'node:fs';
import {BrowserAudioPlayer, BrowserMicrophone, sileroVoiceFactory} from '../dist/browser.js';
class Session {
  state='active'; generation=undefined; listeners={}; sent=[]; acks=[]; interruptionTimeoutMs=1500;
  on(n,f){(this.listeners[n]??=[]).push(f);return()=>this.listeners[n]=this.listeners[n].filter(x=>x!==f);}
  emit(n,v){for(const f of this.listeners[n]??[])f(v);}
  sendEvent(e){this.sent.push(e);}ackPlayed(n,g){this.acks.push([n,g]);}cancelGeneration(){}
  startAudioInput(){this.sent.push({type:'start'});}stopAudioInput(){this.sent.push({type:'stop'});}
  async sendAudio(pcm){this.sent.push({type:'pcm',pcm});}
}
function context(rate=48000){let Processor;
  vm.runInNewContext(readFileSync(new URL('../src/audio-worklet.js',import.meta.url),'utf8'),{sampleRate:rate,Float32Array,Float64Array,Int32Array,
    AudioWorkletProcessor:class{constructor(){this.port={postMessage:m=>this.bridge?.(m)};}},registerProcessor:(_,p)=>Processor=p});
  globalThis.AudioWorkletNode=class{constructor(){this.processor=new Processor();this.port={postMessage:m=>this.processor.port.onmessage({data:m}),close(){}};this.processor.bridge=m=>this.port.onmessage?.({data:m});}connect(){}disconnect(){}};
  return {audioWorklet:{async addModule(){}},destination:{},async resume(){},async close(){}};
}
test('browser canonical ring/resampler reports only rendered source frames; pause and matching recovery',async()=>{
  const s=new Session(), player=await BrowserAudioPlayer.create(s,{context:context()});
  s.generation={id:'g'};s.emit('generationStarted',s.generation);
  s.emit('event',{type:'response.audio.chunk.started',generation_id:'g',data:{sample_rate:16000}});
  s.emit('audio',{generationId:'g',pcm:new Uint8Array(3200),metadata:{sample_rate:16000,source_frames:1600,source_start_frame:0,speech_sequence:0,audio_sequence:0}});
  assert.ok(s.acks.every(([n])=>n===0));
  const p=player.node.processor; p.process([],[[new Float32Array(480)]]); assert.equal(p.sourceProgress(),160);
  const token=player.pause('pause1');const before=p.sourceProgress();p.process([],[[new Float32Array(480)]]);assert.equal(p.sourceProgress(),before);
  assert.ok(s.sent.some(e=>e.type==='playback.paused'&&e.data.played_source_frames===before));
  s.emit('event',{type:'interruption.recovered',generation_id:'old',data:{interruption_id:'pause1'}});assert.equal(p.paused,true);
  s.emit('event',{type:'interruption.recovered',generation_id:'g',data:{interruption_id:'pause1'}});assert.equal(p.paused,false);
  s.emit('event',{type:'generation.done',generation_id:'g',data:{source_frames:1600}});
  for(let i=0;i<20;i++)p.process([],[[new Float32Array(480)]]);
  assert.equal(s.acks.at(-1)[0],1600);await player.close();
});
test('microphone constraints, continuous PCM, VAD, stop releases tracks',async()=>{
  const s=new Session();let callbacks,stopped=0,destroyed=0,constraints;
  const mic=new BrowserMicrophone(s,{mediaDevices:{async getUserMedia(c){constraints=c;return {getTracks:()=>[{stop(){stopped++;}}]};}},
    voiceFactory:async(_,c)=>{callbacks=c;return{start(){},destroy(){destroyed++;}};}});
  await mic.start();assert.equal(constraints.audio.echoCancellation,true);assert.equal(constraints.audio.noiseSuppression,true);assert.equal(constraints.audio.autoGainControl,true);
  callbacks.frame(new Float32Array([1,-1,0]));callbacks.speechStart();callbacks.speechEnd();callbacks.misfire();
  await new Promise(r=>setTimeout(r,0));const pcm=s.sent.find(e=>e.type==='pcm').pcm;assert.deepEqual([...pcm],[255,127,0,128,0,0]);
  assert.ok(s.sent.some(e=>e.type==='input_audio.speech_start'));await mic.stop();assert.equal(stopped,1);assert.equal(destroyed,1);await mic.close();
  assert.ok(s.sent.findIndex(e=>e.type==='pcm')<s.sent.findIndex(e=>e.type==='input_audio.speech_end'));
});
test('stop while permission pending releases late stream, no input starts',async()=>{
  const s=new Session();let resolve,stops=0;
  const mic=new BrowserMicrophone(s,{mediaDevices:{getUserMedia:()=>new Promise(r=>resolve=r)},voiceFactory:async()=>{throw Error('not expected');}});
  const starting=mic.start();await mic.stop();resolve({getTracks:()=>[{stop(){stops++;}}]});await starting;
  assert.equal(stops,1);assert.equal(s.sent.length,0);await mic.close();
  let finishStart,destroys=0;
  const second=new BrowserMicrophone(new Session(),{mediaDevices:{async getUserMedia(){return{getTracks:()=>[{stop(){}}]};}},voiceFactory:async()=>({start:()=>new Promise(r=>finishStart=r),destroy(){destroys++;}})});
  const pending=second.start();while(!finishStart)await Promise.resolve();await second.stop();finishStart();await pending;
  assert.equal(destroys,2);await second.close();
});
test('Silero adapter is optional and uses explicitly supplied assets and stream',async()=>{
  let options;const stream={};const factory=sileroVoiceFactory(async o=>{options=o;return{};},{baseAssetPath:'/vad/',onnxWASMBasePath:'/onnx/'});
  await factory(stream,{frame(){},speechStart(){},speechEnd(){},misfire(){}});assert.equal(await options.getStream(),stream);assert.equal(options.startOnLoad,false);assert.equal(options.baseAssetPath,'/vad/');
});
test('microphone queue overflow and disconnect release media',async()=>{
  const s=new Session();let callbacks,stopped=0,err;
  const mic=new BrowserMicrophone(s,{onError:e=>err=e,mediaDevices:{async getUserMedia(){return{getTracks:()=>[{stop(){stopped++;}}]};}},voiceFactory:async(_,c)=>{callbacks=c;return{start(){},destroy(){}};}});
  await mic.start();for(let n=0;n<300;n++)callbacks.frame(new Float32Array(1024));await new Promise(r=>setTimeout(r,0));
  assert.equal(err.code,'resource_limit');assert.equal(stopped,1);await mic.start();s.state='closed';s.emit('closed',{});await new Promise(r=>setTimeout(r,0));assert.equal(stopped,2);await mic.close();
});
