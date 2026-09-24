const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

function browser() {
    const sent = [];
	const timers = new Map();
	let nextTimer = 0;
    const sandbox = {
        console, Float32Array, ArrayBuffer, DataView, Int16Array, Uint8Array, window: {},
		setTimeout: callback => { const id = ++nextTimer; timers.set(id, callback); return id; },
		clearTimeout: id => timers.delete(id),
        document: {getElementById: () => ({addEventListener() {}, textContent: ''})},
        WebSocket: class {
            static OPEN = 1;
            constructor() { this.readyState = 1; this.bufferedAmount = 0; }
            send(message) { sent.push(typeof message === 'string' ? JSON.parse(message) : message); }
        },
    };
    vm.createContext(sandbox);
    const html = fs.readFileSync(__dirname + '/realtime-test.html', 'utf8');
    vm.runInContext(html.match(/<script>([\s\S]*?)<\/script>/)[1], sandbox);
    return {sent, sandbox, timers, run: code => vm.runInContext(code, sandbox)};
}

test('continuous PCM precedes VAD end, with immediate pause without generation cancellation', () => {
    const {sent, run} = browser();
    run('microphoneStarted = true; sendInputFrame(new Float32Array([0.5, -0.5]));');
    assert.ok(sent[0] instanceof ArrayBuffer);
    assert.equal(new DataView(sent[0]).getInt16(2, true), -16384);
    run(`var paused = false; playerNode = {port: {postMessage(m) { paused = m.type === "pause"; }}};
        currentGenerationID = 'gen_old'; handleUserSpeechStart();`);
    assert.equal(run('paused'), true);
    assert.deepEqual(sent.slice(1).map(e => e.type), ['input_audio.speech_start']);
    run('handleUserSpeechEnd(new Float32Array([1, 1]));');
    assert.equal(sent.at(-1).type, 'input_audio.speech_end');
    assert.equal(sent.filter(e => e.type === 'input_audio.commit').length, 0);
});

test('only matching recovery resumes and generation.done retains paused playback', () => {
    const {run} = browser();
    run(`var controls = []; playerNode = {port: {postMessage(m) {controls.push(m);}}};
        currentGenerationID = 'g1'; pausePlayback('g1');
        handleEvent({type:'generation.done',generation_id:'g1'});
        recoverPlayback({generation_id:'old',data:{interruption_id:'pause_1'}});
        recoverPlayback({generation_id:'g1',data:{interruption_id:'old'}});`);
    assert.equal(run('controls.length'), 2);
    assert.equal(run('controls.at(-1).type'), 'done');
    assert.equal(run('pendingPause.interruptionId'), 'pause_1');
    run(`recoverPlayback({generation_id:'g1',data:{interruption_id:'pause_1'}});`);
    assert.equal(run('controls.at(-1).type'), 'resume');
    assert.equal(run('pendingPause'), null);
});

test('new generation and stale cancellation cannot revive or clear the wrong queue', () => {
    const {run} = browser();
    run(`var controls = []; playerNode = {port: {postMessage(m) {controls.push(m);}}};
        currentGenerationID = 'g1'; pausePlayback('g1');
        handleEvent({type:'generation.created',generation_id:'g2'});
        handleEvent({type:'interruption.recovered',generation_id:'g1',data:{interruption_id:'pause_1'}});
        handleEvent({type:'generation.cancelled',generation_id:'g1'});`);
    assert.equal(run('currentGenerationID'), 'g2');
    assert.equal(run('controls.at(-1).type'), 'generation');
});

test('watchdog requests authoritative cancellation instead of pausing forever', () => {
    const {run, sent, timers} = browser();
    run(`playerNode = {port: {postMessage() {}}}; currentGenerationID='g1'; pausePlayback('g1');`);
    [...timers.values()][0]();
    assert.equal(sent.at(-1).type, 'interruption.failed');
    assert.equal(sent.at(-1).generation_id, 'g1');
    assert.equal(run('pendingPause'), null);
    assert.equal(run('currentGenerationID'), null);
});

test('main-thread MessagePort backlog has the same bounded audio budget', () => {
    const {run, sent} = browser();
    run(`playerNode={port:{postMessage(){}}}; audioContext={sampleRate:16000}; currentGenerationID='g1';
        postedSourceFrames=30*16000;
        pendingAudio={generationId:'g1',sample_rate:16000,channels:1,bits_per_sample:16,bytes:2};
        handleAudioBinary(new ArrayBuffer(2));`);
    assert.equal(sent.at(-1).type, 'playback.overflow');
    assert.equal(run('currentGenerationID'), null);
});

test('startup orders input.start before first microphone frame and stop cleans up', async () => {
    const {sent, sandbox, run} = browser();
    let destroyed = false;
    sandbox.vad = {MicVAD: {new: async options => {
        assert.equal(options.startOnLoad, false);
        return {
            start: async () => options.onFrameProcessed({}, new Float32Array([0, 0])),
            destroy: async () => {destroyed = true;},
        };
    }}};
    run('startAudio = async () => {};');
    await run('startMicrophone()');
    assert.equal(sent[0].type, 'input_audio.start');
    assert.equal(sent[0].data.mode, 'realtime');
    assert.ok(sent[1] instanceof ArrayBuffer);
    await run('stopMicrophone()');
    assert.equal(destroyed, true);
    assert.equal(sent.at(-1).type, 'input_audio.stop');
});

test('disconnect releases microphone and playback', async () => {
    const {run} = browser();
    run(`var destroyed = false; microphoneStarted = true;
        microphoneVAD = {destroy: async () => {destroyed = true;}};
        ws.readyState = 3; ws.onclose({code: 1000});`);
    assert.equal(run('microphoneStarted'), false);
    assert.equal(run('destroyed'), true);
});


test('resampled audio retains native frame counts for history acknowledgements', () => {
    const {run} = browser();
    run(`var posted=[]; playerNode={port:{postMessage(m){posted.push(m);}}};
        audioContext={sampleRate:44100}; currentGenerationID='g1';
        pendingAudio={generationId:'g1',sample_rate:8000,channels:1,bits_per_sample:16,bytes:6};
        handleAudioBinary(new ArrayBuffer(6));`);
    assert.equal(run('posted.at(-1).sourceFrames'), 3);
    assert.equal(run('posted.at(-1).samples.length'), 3);
    assert.equal(run('posted.at(-1).sourceRate'), 8000);
});


test('negotiated runtime grants initial credit from format and exact source credit after drain', () => {
    const {sent, run, sandbox} = browser();
    let Player;
    sandbox.sampleRate = 44100;
    sandbox.AudioWorkletProcessor = class {constructor() {this.port = {postMessage: m => sandbox.handlePlaybackMessage(m)};}};
    sandbox.registerProcessor = (_name, type) => {Player = type;};
    vm.runInContext(fs.readFileSync(__dirname + '/audio-worklet.js', 'utf8'), sandbox);
    const p = new Player();
    sandbox.testPort = {postMessage: m => p.port.onmessage({data:m})};
    run(`playerNode={port:testPort}; audioContext={sampleRate:44100};
        handleEvent({type:'session.created',data:{audio_flow_control:'credit-v1'}});
        handleEvent({type:'generation.created',generation_id:'g'});
        handleEvent({type:'response.audio.chunk.started',generation_id:'g',data:{sequence:0,sample_rate:8000,channels:1,bits_per_sample:16}});`);
    assert.equal(sent[0].type, 'playback.configure');
    assert.equal(sent.at(-1).type, 'playback.credit');
    assert.equal(sent.at(-1).data.received_source_frames, 0);
    assert.equal(sent.at(-1).data.capacity_source_frames, 16000);
    run(`handleEvent({type:'response.audio.delta',generation_id:'g',data:{speech_sequence:0,audio_sequence:0,source_start_frame:0,source_frames:3,sample_rate:8000,channels:1,bits_per_sample:16,bytes:6}});
        handleAudioBinary(new ArrayBuffer(6));
        handleEvent({type:'generation.done',generation_id:'g',data:{source_frames:3}});`);
    p.process([], [[new Float32Array(128)]]);
    assert.equal(p.completed, true);
    assert.equal(p.playedFrames, 17);
    const credit = sent.filter(e => e.type === 'playback.credit').at(-1);
    assert.equal(credit.data.played_source_frames, 3);
    assert.equal(credit.data.buffered_source_frames, 0);
    assert.equal(sent.filter(e => e.type === 'playback.progress').at(-1).data.played_source_frames, 3);
    const count = sent.length;
    run(`handlePlaybackMessage({type:'playback.credit',generationId:'old',sampleRate:44100,playedFrames:1,sourceRate:8000,capacitySourceFrames:16000});`);
    assert.equal(sent.length, count);
});

test('legacy server receives progress without unknown credit extensions', () => {
    const {sent, run} = browser();
    run(`handleEvent({type:'session.created',data:{}}); currentGenerationID='g';
        handlePlaybackMessage({type:'playback.progress',generationId:'g',sampleRate:48000,playedFrames:48000,
            sourceRate:8000,playedSourceFrames:8000,receivedSourceFrames:8000,bufferedSourceFrames:0,capacitySourceFrames:16000});`);
    assert.deepEqual(sent.map(m => m.type), ['playback.progress']);
    assert.equal(sent[0].data.played_source_frames, 8000);
});

test('overflow acknowledges rendered source prefix before authoritative cancellation', () => {
    const {sent, run} = browser();
    run(`currentGenerationID='g'; playerNode={port:{postMessage(){}}};
        handlePlaybackMessage({type:'playback.overflow',generationId:'g',sampleRate:48000,playedFrames:600,
            sourceRate:8000,playedSourceFrames:100,interruptionId:'p'});`);
    assert.deepEqual(sent.map(m => m.type), ['playback.progress', 'playback.overflow']);
    assert.equal(sent[0].data.played_source_frames, 100);
    assert.equal(run('currentGenerationID'), null);
});
