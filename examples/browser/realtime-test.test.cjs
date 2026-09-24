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
    assert.equal(run('controls.length'), 1);
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
        postedOutputFrames=30*16000;
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
    assert.equal(run('posted.at(-1).samples.length'), 17);
});
