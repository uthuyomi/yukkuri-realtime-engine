// Mono output storage. Allocation happens once, never in process().
class PCMFrameRing {
    constructor(capacity) {
        this.samples = new Float32Array(capacity);
        this.capacity = capacity;
        this.clear();
    }
    clear() { this.readIndex = 0; this.writeIndex = 0; this.used = 0; }
    get free() { return this.capacity - this.used; }
    write(value) {
        if (!this.free) throw new Error("PCM ring full");
        this.samples[this.writeIndex] = value;
        this.writeIndex = (this.writeIndex + 1) % this.capacity;
        this.used++;
    }
    read() {
        const value = this.samples[this.readIndex];
        this.readIndex = (this.readIndex + 1) % this.capacity;
        this.used--;
        return value;
    }
}

// Integer output index determines fractional source phase; packet boundaries
// never round durations. Keep the previous sample for interpolation across them.
class StreamingLinearResampler {
    constructor(outputRate, ring) { this.outputRate = outputRate; this.ring = ring; this.reset(); }
    reset() { this.sourceRate = 0; this.inputFrames = 0; this.outputFrames = 0; this.previous = 0; }
    targetFrames(inputFrames = this.inputFrames) { return Math.ceil(inputFrames * this.outputRate / this.sourceRate); }
    push(samples) {
        for (let i = 0; i < samples.length; i++) {
            const index = this.inputFrames++;
            const value = samples[i];
            while (this.outputFrames * this.sourceRate <= index * this.outputRate) {
                const fraction = (this.outputFrames * this.sourceRate - (index - 1) * this.outputRate) / this.outputRate;
                this.ring.write(index === 0 ? value : this.previous + (value - this.previous) * fraction);
                this.outputFrames++;
            }
            this.previous = value;
        }
    }
    finish() {
        // Hold the last sample through the final fractional source interval.
        while (this.outputFrames < this.targetFrames()) { this.ring.write(this.previous); this.outputFrames++; }
    }
}

class PCMPlayerProcessor extends AudioWorkletProcessor {
    constructor(options = {}) {
        super();
        const config = options.processorOptions || {};
        const bounded = (value, fallback, lo, hi) => Number.isFinite(value) ? Math.max(lo, Math.min(hi, value)) : fallback;
        this.startupMs = bounded(config.startupBufferMs, 30, 0, 100);
        this.lowWatermarkMs = bounded(config.lowWatermarkMs, 10, 0, 100);
        this.ring = new PCMFrameRing(Math.max(1, Math.floor(sampleRate * bounded(config.capacityMs, 30000, 200, 30000) / 1000)));
        this.resampler = new StreamingLinearResampler(sampleRate, this.ring);
        // Parallel fixed metadata arrays: packet/chunk -> cumulative source end.
        this.metadataEnds = new Float64Array(4096);
        this.metadataChunks = new Int32Array(4096);
        this.metadataPackets = new Int32Array(4096);
        this.activeGeneration = null;
        this.clockFrames = 0;
        this.reset();
        this.port.onmessage = ({data: m}) => {
            switch (m.type) {
                case "generation": this.setGeneration(m.generationId); break;
                case "format": this.setFormat(m.generationId, m.sourceRate); break;
                case "audio": this.enqueue(m.generationId, m.samples, m.sourceRate, m); break;
                case "done": this.finish(m.generationId, m.sourceFrames); break;
                case "pause": this.pause(m.generationId, m.interruptionId); break;
                case "resume": this.resume(m.generationId, m.interruptionId); break;
                case "clear": this.clear(m.generationId); break;
            }
        };
    }
    reset() {
        this.ring.clear(); this.resampler.reset();
        this.metadataRead = 0; this.metadataWrite = 0; this.metadataUsed = 0;
        this.paused = false; this.pauseID = null;
        this.generationDone = false; this.completed = false;
        this.started = false; this.rebuffering = false;
        this.playedFrames = 0; this.playedSourceFrames = 0; this.lastReportedFrames = 0;
        this.underruns = 0; this.overflows = 0; this.maxBufferedFrames = 0;
        this.createdAt = this.clockFrames; this.startedAt = null; this.doneAt = null; this.completedAt = null;
        this.lastChunk = -1; this.lastPacket = -1;
    }
    setGeneration(id) {
        if (!id || id === this.activeGeneration) return;
        if (this.activeGeneration) this.report("playback.progress");
        this.reset(); this.activeGeneration = id;
    }
    setFormat(id, rate) {
        if (id !== this.activeGeneration || this.generationDone) return;
        if (!Number.isInteger(rate) || rate < 1000 || rate > 192000 || (this.resampler.sourceRate && this.resampler.sourceRate !== rate)) {
            this.fail("invalid_format"); return;
        }
        this.resampler.sourceRate = rate;
        this.report("playback.credit");
    }
    enqueue(id, samples, sourceRate = sampleRate, metadata = {}) {
        if (!id || id !== this.activeGeneration || this.generationDone) return;
        if (!(samples instanceof Float32Array) || !samples.length) return;
        if (!Number.isInteger(sourceRate) || sourceRate < 1000 || sourceRate > 192000 ||
            (this.resampler.sourceRate && sourceRate !== this.resampler.sourceRate) ||
            (metadata.sourceFrames != null && metadata.sourceFrames !== samples.length) ||
            (metadata.sourceStartFrame != null && metadata.sourceStartFrame !== this.resampler.inputFrames)) {
            this.fail("invalid_audio"); return;
        }
        if (metadata.speechSequence != null) {
            const chunk = metadata.speechSequence, packet = metadata.audioSequence;
            if (!Number.isSafeInteger(chunk) || !Number.isSafeInteger(packet) || chunk < 0 || packet < 0 ||
                (chunk === this.lastChunk ? packet !== this.lastPacket + 1 : chunk !== this.lastChunk + 1 || packet !== 0)) {
                this.fail("invalid_packet_order"); return;
            }
            this.lastChunk = chunk; this.lastPacket = packet;
        }
        this.resampler.sourceRate = sourceRate;
        const total = this.resampler.inputFrames + samples.length;
        // Reserve the eventual resampler tail too, even before generation.done.
        if (total > Math.floor(Number.MAX_SAFE_INTEGER / Math.max(sampleRate, sourceRate)) ||
            this.resampler.targetFrames(total) - this.playedFrames > this.ring.capacity || this.metadataUsed === this.metadataEnds.length) {
            this.fail("capacity"); return;
        }
        this.resampler.push(samples);
        this.metadataEnds[this.metadataWrite] = total;
        this.metadataChunks[this.metadataWrite] = metadata.speechSequence ?? -1;
        this.metadataPackets[this.metadataWrite] = metadata.audioSequence ?? -1;
        this.metadataWrite = (this.metadataWrite + 1) % this.metadataEnds.length;
        this.metadataUsed++;
        this.maxBufferedFrames = Math.max(this.maxBufferedFrames, this.resampler.targetFrames() - this.playedFrames);
        // Receipt snapshots are bounded by the server's credit window.
        this.report("playback.credit");
    }
    fail(reason) {
        this.overflows++;
        this.report("playback.overflow", reason);
        this.clear(this.activeGeneration);
    }
    finish(id, sourceFrames) {
        if (!id || id !== this.activeGeneration || this.generationDone) return;
        if (sourceFrames != null && sourceFrames !== this.resampler.inputFrames) { this.fail("incomplete_source_stream"); return; }
        this.generationDone = true; this.doneAt = this.clockFrames;
        if (this.resampler.inputFrames) this.resampler.finish();
        this.checkDrain();
    }
    clear(id) {
        if (id && id !== this.activeGeneration) return;
        this.report("playback.progress");
        this.reset(); this.activeGeneration = null;
    }
    pause(id, token) {
        if (!id || id !== this.activeGeneration || !token) return;
        this.paused = true; this.pauseID = token;
        this.report("playback.paused");
    }
    resume(id, token) {
        if (!id || id !== this.activeGeneration || !this.paused || token !== this.pauseID) return;
        this.paused = false; this.pauseID = null;
    }
    sourceProgress() {
        if (!this.resampler.sourceRate) return 0;
        if (this.generationDone && !this.ring.used) return this.resampler.inputFrames;
        return Math.min(this.resampler.inputFrames, Math.floor(this.playedFrames * this.resampler.sourceRate / sampleRate));
    }
    updateProgress() {
        this.playedSourceFrames = this.sourceProgress();
        while (this.metadataUsed && this.metadataEnds[this.metadataRead] <= this.playedSourceFrames) {
            this.metadataRead = (this.metadataRead + 1) % this.metadataEnds.length;
            this.metadataUsed--;
        }
    }
    report(type, reason) {
        if (!this.activeGeneration) return;
        this.updateProgress();
        this.lastReportedFrames = this.playedFrames;
        const rate = this.resampler.sourceRate;
        const end = this.completedAt ?? this.clockFrames;
        // A two-second default network window sits inside the 30s hard ring cap.
        const capacity = rate ? Math.min(rate * 2, Math.floor((this.ring.capacity - Math.ceil(sampleRate / rate) - 2) * rate / sampleRate)) : 0;
        this.port.postMessage({type, reason, generationId: this.activeGeneration, interruptionId: this.pauseID,
            playedFrames: this.playedFrames, sampleRate, sourceRate: rate,
            playedSourceFrames: this.playedSourceFrames, receivedSourceFrames: this.resampler.inputFrames,
            capacitySourceFrames: capacity, bufferedSourceFrames: this.resampler.inputFrames - this.playedSourceFrames,
            bufferedFrames: this.ring.used, buffered_ms: this.ring.used * 1000 / sampleRate,
            startup_buffer_ms: this.startupMs, low_watermark_ms: this.lowWatermarkMs,
            underrun_count: this.underruns, overflow_count: this.overflows,
            playback_start_latency_ms: this.startedAt === null ? null : (this.startedAt - this.createdAt) * 1000 / sampleRate,
            drain_latency_ms: this.completed ? (end - this.doneAt) * 1000 / sampleRate : null,
            max_buffered_ms: this.maxBufferedFrames * 1000 / sampleRate,
            resample_input_frames: this.resampler.inputFrames, resample_output_frames: this.resampler.outputFrames,
            generation_playback_duration_ms: this.startedAt === null ? 0 : (end - this.startedAt) * 1000 / sampleRate,
            playback_state: this.paused ? "paused" : this.completed ? "completed" : this.started ? "playing" : "starting",
            buffer_state: this.rebuffering ? "underrun" : this.ring.used ? "buffered" : "empty",
            generation_state: this.generationDone ? "done" : "generating"});
    }
    checkDrain() {
        if (this.generationDone && !this.ring.used && !this.completed) {
            this.completed = true; this.completedAt = this.clockFrames;
            this.report("playback.progress");
            this.report("playback.completed");
        }
    }
    process(inputs, outputs) {
        const output = outputs[0];
        if (!output || !output.length) return true;
        const frames = output[0].length;
        this.clockFrames += frames;
        for (let channel = 0; channel < output.length; channel++) output[channel].fill(0);
        if (!this.activeGeneration || this.paused || this.completed) return true;
        const threshold = Math.ceil(sampleRate * (this.rebuffering ? this.lowWatermarkMs : this.startupMs) / 1000);
        if ((!this.started || this.rebuffering) && !this.generationDone && this.ring.used < threshold) return true;
        if (this.ring.used) {
            if (!this.started) { this.started = true; this.startedAt = this.clockFrames; }
            this.rebuffering = false;
        }
        const count = Math.min(frames, this.ring.used);
        for (let i = 0; i < count; i++) {
            const value = this.ring.read();
            for (let channel = 0; channel < output.length; channel++) output[channel][i] = value;
        }
        this.playedFrames += count;
        this.updateProgress();
        if (this.started && count < frames && !this.generationDone && !this.rebuffering) {
            this.rebuffering = true; this.underruns++;
            this.report("playback.underrun");
        }
        if (count && (!this.ring.used || this.playedFrames - this.lastReportedFrames >= Math.max(128, sampleRate / 20))) this.report("playback.progress");
        this.checkDrain();
        return true;
    }
}
registerProcessor("pcm-player", PCMPlayerProcessor);
