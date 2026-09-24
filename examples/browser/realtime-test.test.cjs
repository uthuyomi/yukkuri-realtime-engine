const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

function browser() {
    const sent = [];
    const sandbox = {
        console, Float32Array, ArrayBuffer, DataView, Int16Array, Uint8Array, window: {},
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
    return {sent, sandbox, run: code => vm.runInContext(code, sandbox)};
}

test('continuous PCM precedes VAD end, with immediate independent barge-in', () => {
    const {sent, run} = browser();
    run('microphoneStarted = true; sendInputFrame(new Float32Array([0.5, -0.5]));');
    assert.ok(sent[0] instanceof ArrayBuffer);
    assert.equal(new DataView(sent[0]).getInt16(2, true), -16384);
    run(`var cleared = false; playerNode = {port: {postMessage() { cleared = true; }}};
        currentGenerationID = 'gen_old'; handleUserSpeechStart();`);
    assert.equal(run('cleared'), true);
    assert.deepEqual(sent.slice(1).map(e => e.type), ['generation.cancel', 'input_audio.speech_start']);
    run('handleUserSpeechEnd(new Float32Array([1, 1]));');
    assert.equal(sent.at(-1).type, 'input_audio.speech_end');
    assert.equal(sent.filter(e => e.type === 'input_audio.commit').length, 0);
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
