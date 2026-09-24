const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

function player() {
    const messages = [];
    let Player;
    const context = {
        sampleRate: 16000, Float32Array,
        AudioWorkletProcessor: class { constructor() { this.port = {postMessage: m => messages.push(m)}; } },
        registerProcessor: (_name, type) => { Player = type; }
    };
    vm.runInNewContext(fs.readFileSync(__dirname + '/audio-worklet.js', 'utf8'), context);
    const p = new Player();
    const render = count => {const out = new Float32Array(count); p.process([], [[out]]); return [...out];};
    return {p, messages, render};
}

test('pause retains queue and position; resume plays only unheard samples', () => {
    const {p, render, messages} = player();
    p.setGeneration('g1');
    p.enqueue('g1', new Float32Array([1, 2, 3, 4]));
    assert.deepEqual(render(2), [1, 2]);
    p.pause('g1', 'p1');
    p.enqueue('g1', new Float32Array([5, 6]));
    assert.deepEqual(render(2), [0, 0]);
    assert.equal(p.playedFrames, 2);
    assert.equal(messages.at(-1).bufferedFrames, 2);
    p.resume('g1', 'p1');
    assert.deepEqual(render(4), [3, 4, 5, 6]);
    assert.equal(p.playedFrames, 6);
    assert.equal(p.storedFrames, 0);
});

test('stale resume token and stale generation cannot resume or inject PCM', () => {
    const {p, render} = player();
    p.setGeneration('g1'); p.pause('g1', 'p1'); p.pause('g1', 'p2');
    p.enqueue('g1', new Float32Array([1]));
    p.resume('g1', 'p1');
    assert.deepEqual(render(1), [0]);
    p.setGeneration('g2'); p.pause('g2', 'p3');
    p.enqueue('g1', new Float32Array([9]));
    p.enqueue('g2', new Float32Array([2]));
    p.resume('g1', 'p2'); p.clear('g1');
    assert.deepEqual(render(1), [0]);
    p.resume('g2', 'p3');
    assert.deepEqual(render(1), [2]);
});

test('bounded pause queue overflows to cancellation and cannot be resurrected by resume', () => {
    const {p, render, messages} = player();
    p.maxStoredFrames = 4;
    p.setGeneration('g1'); p.pause('g1', 'p1');
    p.enqueue('g1', new Float32Array([1, 2, 3]));
    p.enqueue('g1', new Float32Array([4, 5]));
    assert.equal(p.activeGeneration, null);
    assert.equal(p.storedFrames, 0);
    assert.equal(messages.at(-1).type, 'playback.overflow');
    p.resume('g1', 'p1'); p.enqueue('g1', new Float32Array([6]));
    assert.deepEqual(render(1), [0]);
});

test('clear after pause releases all retained samples', () => {
    const {p, render} = player();
    p.setGeneration('g1'); p.enqueue('g1', new Float32Array([1, 2])); p.pause('g1', 'p1');
    p.clear('g1'); p.resume('g1', 'p1');
    assert.equal(p.queue.length, 0);
    assert.deepEqual(render(2), [0, 0]);
});


test('native source frames map partial playback conservatively and acknowledge exact packet completion', () => {
    const {p, render, messages} = player();
    p.setGeneration('g1');
    // Simulate resampling: hardware sample count differs from source PCM frames.
    p.enqueue('g1', new Float32Array([1, 2, 3, 4, 5]), 3);
    render(2); p.pause('g1', 'p1');
    assert.equal(messages.at(-1).playedSourceFrames, 1);
    p.resume('g1', 'p1'); render(3);
    assert.equal(messages.at(-1).type, 'playback.progress');
    assert.equal(messages.at(-1).playedSourceFrames, 3);
    p.enqueue('g1', new Float32Array([6]), 1); render(1);
    assert.equal(messages.at(-1).playedSourceFrames, 4);
    p.setGeneration('g2');
    assert.equal(p.playedSourceFrames, 0);
});
