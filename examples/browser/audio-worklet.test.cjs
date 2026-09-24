const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

function player(options = {}, rate = 16000) {
    const messages = [];
    let Player;
    const context = {
        sampleRate: rate, Float32Array, Float64Array, Int32Array,
        AudioWorkletProcessor: class { constructor() { this.port = {postMessage: m => messages.push(m)}; } },
        registerProcessor: (_name, type) => { Player = type; }
    };
    vm.runInNewContext(fs.readFileSync(__dirname + '/audio-worklet.js', 'utf8'), context);
    const p = new Player({processorOptions: {startupBufferMs: 0, lowWatermarkMs: 0, ...options}});
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
    assert.equal(messages.find(m => m.type === "playback.paused").bufferedFrames, 2);
    p.resume('g1', 'p1');
    assert.deepEqual(render(4), [3, 4, 5, 6]);
    assert.equal(p.playedFrames, 6);
    assert.equal(p.ring.used, 0);
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
    p.ring = new (p.ring.constructor)(4); p.resampler.ring = p.ring;
    p.setGeneration('g1'); p.pause('g1', 'p1');
    p.enqueue('g1', new Float32Array([1, 2, 3]));
    p.enqueue('g1', new Float32Array([4, 5]));
    assert.equal(p.activeGeneration, null);
    assert.equal(p.ring.used, 0);
    assert.ok(messages.some(m => m.type === 'playback.overflow'));
    p.resume('g1', 'p1'); p.enqueue('g1', new Float32Array([6]));
    assert.deepEqual(render(1), [0]);
});

test('clear after pause releases all retained samples', () => {
    const {p, render} = player();
    p.setGeneration('g1'); p.enqueue('g1', new Float32Array([1, 2])); p.pause('g1', 'p1');
    p.clear('g1'); p.resume('g1', 'p1');
    assert.equal(p.ring.used, 0);
    assert.deepEqual(render(2), [0, 0]);
});


test('source progress is conservative and final drain reaches the exact total', () => {
    const {p, render, messages} = player({}, 16000);
    p.setGeneration('g1');
    p.enqueue('g1', new Float32Array([1, 2, 3]), 9600);
    render(2); p.pause('g1', 'p1');
    assert.equal(messages.at(-1).playedSourceFrames, 1);
    p.finish('g1');
    assert.equal(p.completed, false);
    p.resume('g1', 'p1'); render(3);
    assert.equal(p.playedSourceFrames, 3);
    assert.equal(p.playedFrames, 5);
    assert.equal(messages.at(-1).type, 'playback.completed');
    p.setGeneration('g2');
    assert.equal(p.playedSourceFrames, 0);
    assert.equal(p.resampler.inputFrames, 0);
});

test('fixed ring wrap-around/full/empty never overwrites unread samples', () => {
    const {p} = player();
    const ring = new (p.ring.constructor)(4);
    const storage = ring.samples;
    for (let round = 0; round < 100000; round++) {
        ring.write(round); ring.write(round + 1); ring.write(round + 2);
        assert.equal(ring.read(), round);
        ring.write(round + 3); ring.write(round + 4);
        assert.equal(ring.free, 0);
        assert.throws(() => ring.write(999), /full/);
        for (let i = 1; i <= 4; i++) assert.equal(ring.read(), round + i);
        assert.equal(ring.used, 0);
    }
    assert.equal(ring.samples, storage);
});

test('packet boundaries produce exactly the same waveform as a single source stream', () => {
    for (const [sourceRate, outputRate] of [[8000, 48000], [8000, 44100], [44100, 48000], [48000, 16000]]) {
        const input = Float32Array.from({length: 1003}, (_, i) => Math.sin(i * 0.07));
        const batch = player({}, outputRate), split = player({}, outputRate);
        batch.p.setGeneration('g'); split.p.setGeneration('g');
        batch.p.enqueue('g', input, sourceRate); batch.p.finish('g');
        for (let i = 0; i < input.length; i += 7) split.p.enqueue('g', input.subarray(i, i + 7), sourceRate);
        split.p.finish('g');
        const count = Math.ceil(input.length * outputRate / sourceRate);
        const a = batch.render(count), b = split.render(count);
        assert.deepEqual(a, b);
        for (let i = 0; i < count; i++) {
            const pos = i * sourceRate / outputRate, index = Math.floor(pos);
            const left = input[Math.min(index, input.length - 1)], right = input[Math.min(index + 1, input.length - 1)];
            assert.ok(Math.abs(a[i] - (left + (right - left) * (pos - index))) < 1e-6);
        }
        assert.equal(split.p.playedSourceFrames, input.length);
    }
});

test('ten-minute streaming run has no per-packet frame drift and keeps storage fixed', () => {
    const {p} = player({}, 44100);
    p.setGeneration('long');
    const storage = p.ring.samples, metadata = p.metadataEnds;
    const input = new Float32Array(127).fill(0.25), output = new Float32Array(1024);
    const total = 8000 * 600;
    let previous = 0;
    for (let n = 0; n < total; n += input.length) {
        p.enqueue('long', input.subarray(0, Math.min(input.length, total - n)), 8000);
        p.process([], [[output]]);
        assert.ok(p.playedSourceFrames >= previous);
        previous = p.playedSourceFrames;
        assert.ok(p.metadataUsed < 3);
    }
    p.finish('long'); p.process([], [[output]]);
    assert.equal(p.playedSourceFrames, total);
    assert.equal(p.playedFrames, Math.ceil(total * 44100 / 8000));
    assert.equal(p.resampler.outputFrames, p.playedFrames);
    assert.equal(p.ring.samples, storage); assert.equal(p.metadataEnds, metadata);
    assert.equal(p.completed, true);
});

test('startup watermark, temporary underrun, rebuffering and done are distinct', () => {
    const {p, render, messages} = player({startupBufferMs: 30, lowWatermarkMs: 10}, 1000);
    p.setGeneration('g'); p.enqueue('g', new Float32Array(20).fill(1), 1000);
    assert.deepEqual(render(20), new Array(20).fill(0));
    p.enqueue('g', new Float32Array(10).fill(2), 1000);
    render(40);
    assert.equal(p.underruns, 1); assert.equal(p.completed, false);
    render(40); assert.equal(p.underruns, 1);
    p.enqueue('g', new Float32Array(5).fill(3), 1000);
    assert.deepEqual(render(5), new Array(5).fill(0));
    p.finish('g'); render(5);
    assert.equal(p.completed, true); assert.equal(p.playedSourceFrames, 35);
    assert.equal(messages.filter(m => m.type === 'playback.completed').length, 1);
    render(40); p.finish('g');
    assert.equal(messages.filter(m => m.type === 'playback.completed').length, 1);
});

test('short done tail bypasses startup and pause retains the tail and position', () => {
    const {p, render} = player({startupBufferMs: 30}, 48000);
    p.setGeneration('g'); p.pause('g', 'p');
    p.enqueue('g', new Float32Array([0.1]), 8000); p.finish('g');
    assert.equal(p.ring.used, 6);
    render(128); assert.equal(p.playedFrames, 0); assert.equal(p.completed, false);
    p.resume('g', 'p'); render(128);
    assert.equal(p.completed, true); assert.equal(p.playedSourceFrames, 1);
});

test('metadata is bounded and out-of-order packet or source offset cancels safely', () => {
    for (const bad of [{sourceStartFrame: 99}, {speechSequence: 0, audioSequence: 2}, {sourceFrames: 3}]) {
        const {p, messages} = player(); p.setGeneration('g');
        p.enqueue('g', new Float32Array([1]), 16000, bad);
        assert.equal(p.activeGeneration, null);
        assert.ok(messages.some(m => m.type === 'playback.overflow'));
    }
    const {p} = player(); p.setGeneration('g'); p.pause('g', 'p');
    for (let i = 0; i < 4097; i++) p.enqueue('g', new Float32Array([1]));
    assert.equal(p.activeGeneration, null); assert.equal(p.ring.used, 0);
});

test('generation switch resets resampler tail and rejects stale done/cancel/audio', () => {
    const {p, render} = player({}, 48000);
    p.setGeneration('old'); p.enqueue('old', new Float32Array([9]), 8000);
    p.setGeneration('new'); p.enqueue('old', new Float32Array([9]), 8000);
    p.finish('old'); p.clear('old'); p.resume('old', 'p');
    p.enqueue('new', new Float32Array([2]), 8000); p.finish('new');
    assert.deepEqual(render(6), new Array(6).fill(2));
    assert.equal(p.playedSourceFrames, 1);
});

test('no late audio accepted after done and all observability values are payload-free', () => {
    const {p, render, messages} = player(); p.setGeneration('g');
    p.enqueue('g', new Float32Array([1])); p.finish('g');
    p.enqueue('g', new Float32Array([2])); render(2);
    const m = messages.at(-1);
    for (const key of ['buffered_ms', 'startup_buffer_ms', 'underrun_count', 'overflow_count', 'playback_start_latency_ms',
        'drain_latency_ms', 'max_buffered_ms', 'resample_input_frames', 'resample_output_frames', 'generation_playback_duration_ms']) assert.equal(typeof m[key], 'number');
    assert.equal(m.resample_input_frames, 1); assert.equal(m.buffered_source_frames, undefined);
    assert.equal('samples' in m, false); assert.equal('text' in m, false);
});


test('done with missing PCM fails rather than completing a truncated generation', () => {
    const {p, messages} = player();p.setGeneration('g');
    p.enqueue('g', new Float32Array([1]));p.finish('g', 2);
    assert.equal(p.activeGeneration, null);
    assert.ok(messages.some(m => m.reason === 'incomplete_source_stream'));
    assert.equal(messages.some(m => m.type === 'playback.completed'), false);
});
